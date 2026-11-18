package x11

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	// DefaultSocketDir is where Xlib/libxcb look for the unix socket of
	// display ":N".
	DefaultSocketDir = "/tmp/.X11-unix"

	// ChannelType is the SSH channel a server opens per forwarded X11
	// connection (RFC 4254 section 6.3.2).
	ChannelType = "x11"

	// firstDisplay matches OpenSSH's X11DisplayOffset default, keeping
	// forwarded displays clear of local X servers on :0, :1, ...
	firstDisplay = 10
	maxDisplays  = 1000
	tcpPortBase  = 6000
)

// Request is a parsed SSH x11-req.
type Request struct {
	SingleConnection bool
	AuthProtocol     string
	AuthCookie       string // hex-encoded
	ScreenNumber     int
}

// Validate reports whether the request is one devsy can serve.
func (r Request) Validate() error {
	if r.AuthProtocol != AuthProtoMITMagicCookie {
		return fmt.Errorf("unsupported X11 auth protocol %q", r.AuthProtocol)
	}
	if r.ScreenNumber < 0 {
		return fmt.Errorf("invalid X11 screen %d", r.ScreenNumber)
	}
	_, err := ParseCookie(r.AuthCookie)
	return err
}

// ChannelOpener opens channels towards the SSH client; ssh.Conn implements it.
type ChannelOpener interface {
	OpenChannel(name string, data []byte) (ssh.Channel, <-chan *ssh.Request, error)
}

// ServerOptions configures StartDisplay. Zero values pick the defaults.
type ServerOptions struct {
	// SocketDir holds the display's unix socket (default DefaultSocketDir).
	SocketDir string
	// XAuthorityPath is the file the session's cookie is added to,
	// normally the user's ~/.Xauthority.
	XAuthorityPath string
	// FallbackDir receives a per-session Xauthority file if XAuthorityPath
	// cannot be written (e.g. a read-only home).
	FallbackDir string
	// Hostname is the Xauthority address of local entries (default
	// os.Hostname, which is what libxcb looks up).
	Hostname string
	// Chown, if set, gives created files to the session user.
	Chown func(path string) error
}

// Display is a forwarded display serving one SSH session.
type Display struct {
	// Env holds the DISPLAY and XAUTHORITY assignments for the session.
	Env []string

	listener  net.Listener
	cleanups  []func()
	closeOnce sync.Once
}

// Close stops accepting X11 connections and removes the display's socket
// and cookie. Connections already forwarded keep running.
func (d *Display) Close() {
	d.closeOnce.Do(func() {
		_ = d.listener.Close()
		for i := len(d.cleanups) - 1; i >= 0; i-- {
			d.cleanups[i]()
		}
	})
}

// StartDisplay allocates a display, registers the request's cookie for it
// and forwards every X11 client connection to it over conn.
func StartDisplay(conn ChannelOpener, req Request, opts ServerOptions) (*Display, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	cookie, _ := ParseCookie(req.AuthCookie)
	opts, err := withDefaults(opts)
	if err != nil {
		return nil, err
	}

	listener, number, displayHost, cleanupSocket, err := listenDisplay(opts)
	if err != nil {
		return nil, err
	}
	d := &Display{listener: listener, cleanups: []func(){cleanupSocket}}

	entry := Entry{
		Family:  FamilyLocal,
		Address: opts.Hostname,
		Number:  strconv.Itoa(number),
		Name:    AuthProtoMITMagicCookie,
		Data:    cookie,
	}
	xauthPath, cleanupAuth, err := registerCookie(entry, opts)
	if err != nil {
		d.Close()
		return nil, err
	}
	d.cleanups = append(d.cleanups, cleanupAuth)

	d.Env = []string{
		fmt.Sprintf("DISPLAY=%s:%d.%d", displayHost, number, req.ScreenNumber),
		"XAUTHORITY=" + xauthPath,
	}
	go serve(listener, conn, req.SingleConnection)
	return d, nil
}

func withDefaults(opts ServerOptions) (ServerOptions, error) {
	if opts.SocketDir == "" {
		opts.SocketDir = DefaultSocketDir
	}
	if opts.Hostname == "" {
		hostname, err := os.Hostname()
		if err != nil {
			return opts, fmt.Errorf("hostname: %w", err)
		}
		opts.Hostname = hostname
	}
	if opts.Chown == nil {
		opts.Chown = func(string) error { return nil }
	}
	return opts, nil
}

// listenDisplay listens on the first free display's unix socket. If the
// socket directory is unusable (e.g. a read-only bind mount), it falls back
// to loopback TCP, which libxcb authenticates with the same local entry.
func listenDisplay(opts ServerOptions) (net.Listener, int, string, func(), error) {
	unixErr := ensureSocketDir(opts.SocketDir)
	if unixErr == nil {
		l, n, cleanup, err := listenUnix(opts)
		if err == nil {
			return l, n, "", cleanup, nil
		}
		unixErr = err
	}
	l, n, err := listenTCP()
	if err != nil {
		return nil, 0, "", nil, errors.Join(unixErr, err)
	}
	return l, n, "localhost", func() {}, nil
}

func ensureSocketDir(dir string) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		// Mkdir is subject to the umask; set the shared sticky mode directly.
		if err := os.Chmod(dir, 0o777|os.ModeSticky); err != nil { // #nosec G302 -- shared like /tmp
			return err
		}
		info, err = os.Lstat(dir)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	return nil
}

func listenUnix(opts ServerOptions) (net.Listener, int, func(), error) {
	var lastErr error
	for n := firstDisplay; n < firstDisplay+maxDisplays; n++ {
		path := filepath.Join(opts.SocketDir, "X"+strconv.Itoa(n))
		if displayInUse(path, n) {
			continue
		}
		l, err := net.Listen("unix", path)
		if err != nil {
			// Only a display taken in the meantime is worth skipping; any
			// other error (permissions, read-only file system) affects
			// every display number alike.
			if !errors.Is(err, syscall.EADDRINUSE) {
				return nil, 0, nil, err
			}
			lastErr = err
			continue
		}
		l.(*net.UnixListener).SetUnlinkOnClose(true)
		if err := opts.Chown(path); err != nil {
			_ = l.Close()
			return nil, 0, nil, fmt.Errorf("chown %s: %w", path, err)
		}
		return l, n, func() {}, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no free X11 display")
	}
	return nil, 0, nil, lastErr
}

// displayInUse reports whether display n already has a live socket or an X
// server lock file (/tmp/.X<n>-lock). A socket nobody listens on is left
// over from a killed forwarder and is removed.
func displayInUse(socketPath string, n int) bool {
	if info, err := os.Lstat(socketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return true
		}
		c, err := net.DialTimeout("unix", socketPath, time.Second)
		if err == nil {
			_ = c.Close()
			return true
		}
		if !errors.Is(err, syscall.ECONNREFUSED) || os.Remove(socketPath) != nil {
			return true
		}
	}
	_, err := os.Lstat(filepath.Join(filepath.Dir(filepath.Dir(socketPath)), ".X"+strconv.Itoa(n)+"-lock"))
	return err == nil
}

func listenTCP() (net.Listener, int, error) {
	var lastErr error
	for n := firstDisplay; n < firstDisplay+maxDisplays; n++ {
		l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(tcpPortBase+n)))
		if err == nil {
			return l, n, nil
		}
		lastErr = err
	}
	return nil, 0, lastErr
}

// registerCookie adds entry to the user's Xauthority file, or to a fresh
// per-session file if that is not writable. It returns the file in use and
// a cleanup that removes the entry again.
func registerCookie(entry Entry, opts ServerOptions) (string, func(), error) {
	if opts.XAuthorityPath != "" {
		existed := fileExists(opts.XAuthorityPath)
		err := UpsertEntry(opts.XAuthorityPath, entry)
		if err == nil && !existed {
			err = opts.Chown(opts.XAuthorityPath)
		}
		if err == nil {
			path := opts.XAuthorityPath
			return path, func() { _ = RemoveEntry(path, entry.Family, entry.Address, entry.Number) }, nil
		}
		if opts.FallbackDir == "" {
			return "", nil, fmt.Errorf("update %s: %w", opts.XAuthorityPath, err)
		}
	}
	if opts.FallbackDir == "" {
		return "", nil, errors.New("no xauthority location")
	}
	dir, err := os.MkdirTemp(opts.FallbackDir, "devsy-x11-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "Xauthority")
	if err := UpsertEntry(path, entry); err != nil {
		cleanup()
		return "", nil, err
	}
	if err := errors.Join(opts.Chown(dir), opts.Chown(path)); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// x11ChannelOpen is the x11 channel's open payload (RFC 4254 6.3.2).
type x11ChannelOpen struct {
	OriginatorAddress string
	OriginatorPort    uint32
}

func serve(l net.Listener, conn ChannelOpener, single bool) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go forward(c, conn)
		if single {
			_ = l.Close()
			return
		}
	}
}

func forward(c net.Conn, conn ChannelOpener) {
	defer func() { _ = c.Close() }()
	payload := ssh.Marshal(x11ChannelOpen{OriginatorAddress: "127.0.0.1"})
	ch, reqs, err := conn.OpenChannel(ChannelType, payload)
	if err != nil {
		return
	}
	defer func() { _ = ch.Close() }()
	go ssh.DiscardRequests(reqs)
	Pipe(c, ch)
}

// halfCloser is implemented by connections that support shutting down
// their write side (unix and TCP connections, SSH channels).
type halfCloser interface {
	CloseWrite() error
}

// Pipe copies between a and b in both directions until both are done,
// half-closing each side when its source reaches EOF.
func Pipe(a, b io.ReadWriter) {
	var wg sync.WaitGroup
	wg.Go(func() { copyHalf(a, b) })
	wg.Go(func() { copyHalf(b, a) })
	wg.Wait()
}

func copyHalf(dst io.Writer, src io.Reader) {
	_, _ = io.Copy(dst, src)
	if hc, ok := dst.(halfCloser); ok {
		_ = hc.CloseWrite()
	}
}
