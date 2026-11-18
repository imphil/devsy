package x11

import (
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDisplay(t *testing.T) {
	localSocket := []string{"/tmp/.X11-unix/X1"}
	if runtime.GOOS == "linux" {
		localSocket = []string{"@/tmp/.X11-unix/X1", "/tmp/.X11-unix/X1"}
	}
	tests := []struct {
		display string
		want    LocalDisplay
	}{
		{":1", LocalDisplay{Number: 1, Network: "unix", Addresses: localSocket, local: true}},
		{"unix:1.2", LocalDisplay{Number: 1, Screen: 2, Network: "unix", Addresses: localSocket, local: true}},
		{
			"/private/tmp/com.apple.launchd.abc/org.xquartz:0",
			LocalDisplay{
				Network: "unix", Addresses: []string{"/private/tmp/com.apple.launchd.abc/org.xquartz:0"}, local: true,
			},
		},
		{"localhost:10.0", LocalDisplay{Number: 10, Network: "tcp", Addresses: []string{"localhost:6010"}, local: true, host: "localhost"}},
		{"127.0.0.1:0", LocalDisplay{Network: "tcp", Addresses: []string{"127.0.0.1:6000"}, local: true, host: "127.0.0.1"}},
		{"192.168.1.5:3", LocalDisplay{Number: 3, Network: "tcp", Addresses: []string{"192.168.1.5:6003"}, host: "192.168.1.5"}},
	}
	for _, tt := range tests {
		t.Run(tt.display, func(t *testing.T) {
			got, err := ParseDisplay(tt.display)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
	for _, bad := range []string{"", "1", ":", ":x", ":1.x", ":-1", "host:1.-2"} {
		_, err := ParseDisplay(bad)
		assert.Error(t, err, "DISPLAY %q", bad)
	}
}

func TestFindCookie(t *testing.T) {
	local, err := ParseDisplay(":1")
	require.NoError(t, err)
	remote, err := ParseDisplay("192.168.1.5:1")
	require.NoError(t, err)
	cookie := func(b byte) []byte { return []byte{b} }
	entry := func(family uint16, addr, number string, b byte) Entry {
		return Entry{Family: family, Address: addr, Number: number, Name: AuthProtoMITMagicCookie, Data: cookie(b)}
	}

	entries := []Entry{
		entry(FamilyLocal, "otherhost", "1", 1),
		entry(FamilyLocal, "myhost", "0", 2),
		{Family: FamilyLocal, Address: "myhost", Number: "1", Name: "XDM-AUTHORIZATION-1", Data: cookie(3)},
		entry(FamilyLocal, "myhost", "1", 4),
		entry(FamilyInternet, "\xc0\xa8\x01\x05", "1", 5),
	}
	got, ok := FindCookie(entries, local, "myhost")
	assert.True(t, ok)
	assert.Equal(t, cookie(4), got, "exact host, number and protocol")

	got, ok = FindCookie(entries, remote, "myhost")
	assert.True(t, ok)
	assert.Equal(t, cookie(5), got, "TCP displays use the internet family")

	got, ok = FindCookie([]Entry{entry(FamilyWild, "", "1", 6)}, local, "myhost")
	assert.True(t, ok)
	assert.Equal(t, cookie(6), got, "wildcard family matches any address")

	got, ok = FindCookie([]Entry{entry(FamilyLocal, "myhost", "", 7)}, local, "myhost")
	assert.True(t, ok)
	assert.Equal(t, cookie(7), got, "empty number matches any display")

	_, ok = FindCookie(entries[:3], local, "myhost")
	assert.False(t, ok)
}

// setupPacket builds an X11 connection setup request.
func setupPacket(order binary.ByteOrder, name string, data []byte) []byte {
	s := &x11Setup{order: order}
	s.header[0] = 'l'
	if order == binary.BigEndian {
		s.header[0] = 'B'
	}
	order.PutUint16(s.header[2:4], 11) // protocol major version
	return s.encode(name, data)
}

func TestSetupRoundTrip(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.BigEndian, binary.LittleEndian} {
		pkt := setupPacket(order, AuthProtoMITMagicCookie, []byte{1, 2, 3})
		assert.Zero(t, len(pkt)%4, "padded to 4 bytes")

		s, err := readSetup(bytesReader(pkt))
		require.NoError(t, err)
		assert.Equal(t, AuthProtoMITMagicCookie, s.authName)
		assert.Equal(t, []byte{1, 2, 3}, s.authData)
		assert.Equal(t, uint16(11), order.Uint16(s.header[2:4]), "header preserved")

		rewritten, err := readSetup(bytesReader(s.encode("", nil)))
		require.NoError(t, err)
		assert.Empty(t, rewritten.authName)
		assert.Empty(t, rewritten.authData)
	}
	_, err := readSetup(bytesReader([]byte("x23456789012")))
	assert.Error(t, err, "invalid byte order")
}

func bytesReader(b []byte) io.Reader {
	r, w := io.Pipe()
	go func() { _, _ = w.Write(b); _ = w.Close() }()
	return r
}

// fakeXServer listens like a local X server and reports the setup request
// of each client.
func fakeXServer(t *testing.T) (display string, setups <-chan *x11Setup) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "xsock:0")
	l, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	ch := make(chan *x11Setup, 4)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				s, err := readSetup(c)
				if err != nil {
					return
				}
				ch <- s
				_, _ = c.Write([]byte("accepted"))
			}()
		}
	}()
	return path, ch
}

func TestForwarding_ClientSubstitutesRealCookie(t *testing.T) {
	serverConn, clientChans := sshPair(t)
	display, setups := fakeXServer(t)
	hostname, err := os.Hostname()
	require.NoError(t, err)

	real := []byte("real-cookie-0123")
	clientXAuth := filepath.Join(t.TempDir(), ".Xauthority")
	require.NoError(t, UpsertEntry(clientXAuth, Entry{
		Family: FamilyLocal, Address: hostname, Number: "0", Name: AuthProtoMITMagicCookie, Data: real,
	}))

	f, err := NewClientForwarder(ClientOptions{Display: display, XAuthorityPath: clientXAuth, Hostname: hostname})
	require.NoError(t, err)
	go f.Serve(clientChans)

	// Server half: the container side gets only the fake cookie.
	opts := testOptions(t)
	d, err := StartDisplay(serverConn, f.Request(false), opts)
	require.NoError(t, err)
	t.Cleanup(d.Close)
	containerEntries, err := ReadFile(opts.XAuthorityPath)
	require.NoError(t, err)
	fake, ok := Lookup(containerEntries, FamilyLocal, "testhost", "10")
	require.True(t, ok)
	assert.NotEqual(t, real, fake.Data, "the real cookie never reaches the container")

	dialContainerDisplay := func(cookie []byte) net.Conn {
		x, err := net.Dial("unix", filepath.Join(opts.SocketDir, "X10"))
		require.NoError(t, err)
		_, err = x.Write(setupPacket(binary.LittleEndian, AuthProtoMITMagicCookie, cookie))
		require.NoError(t, err)
		return x
	}

	// An X client in the container presents the fake cookie...
	x := dialContainerDisplay(fake.Data)
	select {
	case s := <-setups:
		// ...and the local X server receives the real one.
		assert.Equal(t, AuthProtoMITMagicCookie, s.authName)
		assert.Equal(t, real, s.authData)
	case <-time.After(5 * time.Second):
		t.Fatal("local X server never got the connection")
	}
	reply, err := io.ReadAll(x)
	require.NoError(t, err)
	assert.Equal(t, "accepted", string(reply))
	_ = x.Close()

	// A client with the wrong cookie never reaches the local display.
	bad := dialContainerDisplay([]byte("wrong-cookie-000"))
	reply, _ = io.ReadAll(bad)
	assert.Empty(t, reply)
	_ = bad.Close()
	select {
	case <-setups:
		t.Fatal("connection with a wrong cookie was forwarded")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestNewClientForwarder_NoDisplay(t *testing.T) {
	t.Setenv("DISPLAY", "")
	_, err := NewClientForwarder(ClientOptions{})
	assert.ErrorIs(t, err, ErrNoDisplay)
}

func TestNewClientForwarder_NoCookieForwardsWithoutAuth(t *testing.T) {
	display, _ := fakeXServer(t)
	f, err := NewClientForwarder(ClientOptions{
		Display: display, XAuthorityPath: filepath.Join(t.TempDir(), "missing"), Hostname: "h",
	})
	require.NoError(t, err)
	assert.Nil(t, f.realCookie)
	assert.Len(t, f.fakeCookie, fakeCookieBytes)
}
