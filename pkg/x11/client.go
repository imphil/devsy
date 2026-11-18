package x11

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	// FamilyInternet and FamilyInternet6 are Xauthority address families
	// for TCP displays; FamilyWild entries match any address.
	FamilyInternet  uint16 = 0
	FamilyInternet6 uint16 = 6
	FamilyWild      uint16 = 65535

	fakeCookieBytes = 16
	dialTimeout     = 10 * time.Second
	// setupHeaderLen is the fixed part of an X11 connection setup request.
	setupHeaderLen = 12
)

// LocalDisplay is a parsed DISPLAY value of the machine running the SSH
// client.
type LocalDisplay struct {
	// Number and Screen are the display and screen numbers.
	Number int
	Screen int
	// Network and Addresses are dialed in order ("unix" or "tcp").
	Network   string
	Addresses []string
	// local reports whether xauth entries use FamilyLocal (unix sockets
	// and, as in libxcb, loopback TCP); host is the TCP host otherwise.
	local bool
	host  string
}

// ParseDisplay parses a DISPLAY value: ":N[.S]" or "unix:N[.S]" (unix
// socket), "/path/to/socket:N[.S]" (e.g. XQuartz's launchd socket) or
// "host:N[.S]" (TCP port 6000+N).
func ParseDisplay(display string) (LocalDisplay, error) {
	i := strings.LastIndex(display, ":")
	if i < 0 {
		return LocalDisplay{}, fmt.Errorf("invalid DISPLAY %q", display)
	}
	host, rest := display[:i], display[i+1:]
	numStr, screenStr, hasScreen := strings.Cut(rest, ".")
	number, err := strconv.Atoi(numStr)
	if err != nil || number < 0 {
		return LocalDisplay{}, fmt.Errorf("invalid DISPLAY %q", display)
	}
	d := LocalDisplay{Number: number}
	if hasScreen {
		if d.Screen, err = strconv.Atoi(screenStr); err != nil || d.Screen < 0 {
			return LocalDisplay{}, fmt.Errorf("invalid DISPLAY %q", display)
		}
	}

	switch {
	case host == "" || host == "unix":
		path := filepath.Join(DefaultSocketDir, "X"+numStr)
		d.Network, d.local = "unix", true
		if runtime.GOOS == "linux" {
			// X servers on Linux also listen on the abstract socket.
			d.Addresses = append(d.Addresses, "@"+path)
		}
		d.Addresses = append(d.Addresses, path)
	case strings.HasPrefix(host, "/"):
		// The socket path includes the ":N" suffix (XQuartz).
		d.Network, d.local = "unix", true
		d.Addresses = []string{host + ":" + numStr}
	default:
		d.Network, d.host = "tcp", host
		d.local = host == "localhost" || isLoopback(host)
		d.Addresses = []string{net.JoinHostPort(host, strconv.Itoa(tcpPortBase+number))}
	}
	return d, nil
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (d LocalDisplay) dial() (net.Conn, error) {
	var errs []error
	for _, addr := range d.Addresses {
		c, err := net.DialTimeout(d.Network, addr, dialTimeout)
		if err == nil {
			return c, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

type authAddress struct {
	family  uint16
	address string
}

// authAddresses returns the Xauthority family/address pairs that identify
// this display.
func (d LocalDisplay) authAddresses(hostname string) []authAddress {
	if d.local {
		return []authAddress{{FamilyLocal, hostname}}
	}
	var out []authAddress
	ips, _ := net.LookupIP(d.host)
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			out = append(out, authAddress{FamilyInternet, string(v4)})
		} else {
			out = append(out, authAddress{FamilyInternet6, string(ip.To16())})
		}
	}
	return out
}

// FindCookie returns the MIT-MAGIC-COOKIE-1 for display from entries the way
// libXau's XauGetBestAuthByAddr does: an exact address match or a wildcard
// entry, for the display's number or for any number.
func FindCookie(entries []Entry, display LocalDisplay, hostname string) ([]byte, bool) {
	number := strconv.Itoa(display.Number)
	for _, addr := range display.authAddresses(hostname) {
		for _, e := range entries {
			if e.Name != AuthProtoMITMagicCookie || (e.Number != number && e.Number != "") {
				continue
			}
			if e.Family == FamilyWild || (e.Family == addr.family && e.Address == addr.address) {
				return e.Data, true
			}
		}
	}
	return nil, false
}

// ClientOptions configures NewClientForwarder. Zero values pick defaults.
type ClientOptions struct {
	// Display is the local DISPLAY (default $DISPLAY).
	Display string
	// XAuthorityPath holds the real cookie (default $XAUTHORITY or
	// ~/.Xauthority).
	XAuthorityPath string
	// Hostname is the address of local Xauthority entries (default
	// os.Hostname).
	Hostname string
}

// ClientForwarder is the SSH client end of X11 forwarding: it sends the
// server a fake cookie and, for every forwarded connection, checks the fake
// cookie and substitutes the real one before connecting to the local
// display, so the real cookie never leaves this machine.
type ClientForwarder struct {
	display    LocalDisplay
	realCookie []byte // nil if the local display needs no cookie
	fakeCookie []byte
}

// ErrNoDisplay means there is no local display to forward to.
var ErrNoDisplay = errors.New("no local X11 display (DISPLAY is not set)")

// NewClientForwarder prepares forwarding to the local display.
func NewClientForwarder(opts ClientOptions) (*ClientForwarder, error) {
	if opts.Display == "" {
		opts.Display = os.Getenv("DISPLAY")
	}
	if opts.Display == "" {
		return nil, ErrNoDisplay
	}
	display, err := ParseDisplay(opts.Display)
	if err != nil {
		return nil, err
	}
	if opts.XAuthorityPath == "" {
		opts.XAuthorityPath = os.Getenv("XAUTHORITY")
	}
	if opts.XAuthorityPath == "" {
		if home, err := os.UserHomeDir(); err == nil {
			opts.XAuthorityPath = filepath.Join(home, ".Xauthority")
		}
	}
	if opts.Hostname == "" {
		if opts.Hostname, err = os.Hostname(); err != nil {
			return nil, fmt.Errorf("hostname: %w", err)
		}
	}

	f := &ClientForwarder{display: display, fakeCookie: make([]byte, fakeCookieBytes)}
	if _, err := rand.Read(f.fakeCookie); err != nil {
		return nil, err
	}
	if opts.XAuthorityPath != "" {
		entries, err := ReadFile(opts.XAuthorityPath)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", opts.XAuthorityPath, err)
		}
		// No cookie is fine: the display may allow the user by other means
		// (e.g. xhost +SI:localuser), so connections are then forwarded
		// without authorization data.
		f.realCookie, _ = FindCookie(entries, display, opts.Hostname)
	}
	return f, nil
}

// Request returns the x11-req to send for this forwarder.
func (f *ClientForwarder) Request(single bool) Request {
	return Request{
		SingleConnection: single,
		AuthProtocol:     AuthProtoMITMagicCookie,
		AuthCookie:       hex.EncodeToString(f.fakeCookie),
		ScreenNumber:     f.display.Screen,
	}
}

// Serve forwards each incoming x11 channel to the local display until chans
// is closed.
func (f *ClientForwarder) Serve(chans <-chan ssh.NewChannel) {
	for newCh := range chans {
		go f.handle(newCh)
	}
}

func (f *ClientForwarder) handle(newCh ssh.NewChannel) {
	ch, reqs, err := newCh.Accept()
	if err != nil {
		return
	}
	defer func() { _ = ch.Close() }()
	go ssh.DiscardRequests(reqs)

	setup, err := readSetup(ch)
	if err != nil {
		return
	}
	if setup.authName != AuthProtoMITMagicCookie ||
		subtle.ConstantTimeCompare(setup.authData, f.fakeCookie) != 1 {
		// Not a client the server handed our display to.
		return
	}
	local, err := f.display.dial()
	if err != nil {
		return
	}
	defer func() { _ = local.Close() }()

	name, data := AuthProtoMITMagicCookie, f.realCookie
	if data == nil {
		name = ""
	}
	if _, err := local.Write(setup.encode(name, data)); err != nil {
		return
	}
	Pipe(local, ch)
}

// x11Setup is the connection setup request an X11 client sends first.
type x11Setup struct {
	order    binary.ByteOrder
	header   [setupHeaderLen]byte
	authName string
	authData []byte
}

func pad4(n int) int { return (4 - n%4) % 4 }

func readSetup(r io.Reader) (*x11Setup, error) {
	s := &x11Setup{}
	if _, err := io.ReadFull(r, s.header[:]); err != nil {
		return nil, err
	}
	switch s.header[0] {
	case 'B':
		s.order = binary.BigEndian
	case 'l':
		s.order = binary.LittleEndian
	default:
		return nil, fmt.Errorf("invalid X11 byte order %#x", s.header[0])
	}
	nameLen := int(s.order.Uint16(s.header[6:8]))
	dataLen := int(s.order.Uint16(s.header[8:10]))
	buf := make([]byte, nameLen+pad4(nameLen)+dataLen+pad4(dataLen))
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	s.authName = string(buf[:nameLen])
	off := nameLen + pad4(nameLen)
	s.authData = buf[off : off+dataLen]
	return s, nil
}

// encode returns the setup request with its authorization replaced.
func (s *x11Setup) encode(name string, data []byte) []byte {
	out := make([]byte, 0, setupHeaderLen+len(name)+len(data)+6)
	header := s.header
	s.order.PutUint16(header[6:8], uint16(len(name)))  // #nosec G115 -- protocol name
	s.order.PutUint16(header[8:10], uint16(len(data))) // #nosec G115 -- <= maxCookieBytes
	out = append(out, header[:]...)
	out = append(out, name...)
	out = append(out, make([]byte, pad4(len(name)))...)
	out = append(out, data...)
	return append(out, make([]byte, pad4(len(data)))...)
}

// RequestForwarding asks the server to forward X11 for session and serves
// the forwarded connections on client. It must run before the session
// starts its command.
func RequestForwarding(client *ssh.Client, session *ssh.Session, opts ClientOptions) error {
	f, err := NewClientForwarder(opts)
	if err != nil {
		return err
	}
	chans := client.HandleChannelOpen(ChannelType)
	if chans == nil {
		return errors.New("x11 channels are already handled on this connection")
	}
	req := f.Request(false)
	payload := ssh.Marshal(struct {
		SingleConnection bool
		AuthProtocol     string
		AuthCookie       string
		ScreenNumber     uint32
	}{req.SingleConnection, req.AuthProtocol, req.AuthCookie, uint32(req.ScreenNumber)}) // #nosec G115 -- parsed non-negative
	ok, err := session.SendRequest("x11-req", true, payload)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("server refused X11 forwarding")
	}
	go f.Serve(chans)
	return nil
}
