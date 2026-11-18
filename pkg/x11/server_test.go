package x11

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

const testCookie = "0123456789abcdef0123456789abcdef"

func staleTime() time.Time { return time.Now().Add(-2 * staleLockAge) }

// sshPair connects an in-memory SSH server and client. The server side is
// returned as the ChannelOpener a session would use; x11 channels the
// server opens arrive on the returned channel.
func sshPair(t *testing.T) (ssh.Conn, <-chan ssh.NewChannel) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(priv)
	require.NoError(t, err)

	serverCfg := &ssh.ServerConfig{NoClientAuth: true}
	serverCfg.AddHostKey(signer)
	clientCfg := &ssh.ClientConfig{
		User:            "test",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // #nosec G106 -- in-memory test pair
	}

	// A real socket rather than net.Pipe: both SSH sides write their version
	// banner before reading, which deadlocks on an unbuffered pipe.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	b, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	a, err := ln.Accept()
	require.NoError(t, err)
	type result struct {
		conn ssh.Conn
		err  error
	}
	serverDone := make(chan result, 1)
	go func() {
		conn, chans, reqs, err := ssh.NewServerConn(a, serverCfg)
		if err == nil {
			go ssh.DiscardRequests(reqs)
			go func() {
				for ch := range chans {
					_ = ch.Reject(ssh.Prohibited, "test")
				}
			}()
		}
		serverDone <- result{conn, err}
	}()
	clientConn, chans, reqs, err := ssh.NewClientConn(b, "pipe", clientCfg)
	require.NoError(t, err)
	client := ssh.NewClient(clientConn, chans, reqs)
	x11Chans := client.HandleChannelOpen(ChannelType)
	server := <-serverDone
	require.NoError(t, server.err)
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.conn.Close()
	})
	return server.conn, x11Chans
}

func testOptions(t *testing.T) ServerOptions {
	t.Helper()
	dir := t.TempDir()
	return ServerOptions{
		SocketDir:      filepath.Join(dir, ".X11-unix"),
		XAuthorityPath: filepath.Join(dir, ".Xauthority"),
		FallbackDir:    dir,
		Hostname:       "testhost",
	}
}

func envValue(env []string, key string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v
		}
	}
	return ""
}

// echoClientSide accepts one forwarded x11 channel and echoes what it
// receives prefixed with "re:".
func echoClientSide(t *testing.T, chans <-chan ssh.NewChannel) {
	t.Helper()
	go func() {
		newCh := <-chans
		var payload x11ChannelOpen
		assert.NoError(t, ssh.Unmarshal(newCh.ExtraData(), &payload))
		ch, reqs, err := newCh.Accept()
		if !assert.NoError(t, err) {
			return
		}
		go ssh.DiscardRequests(reqs)
		buf := make([]byte, 5)
		_, err = io.ReadFull(ch, buf)
		assert.NoError(t, err)
		_, _ = ch.Write(append([]byte("re:"), buf...))
		_ = ch.CloseWrite()
	}()
}

func TestStartDisplay_ForwardsUnixConnections(t *testing.T) {
	conn, chans := sshPair(t)
	opts := testOptions(t)

	d, err := StartDisplay(conn, Request{
		AuthProtocol: AuthProtoMITMagicCookie, AuthCookie: testCookie, ScreenNumber: 0,
	}, opts)
	require.NoError(t, err)
	t.Cleanup(d.Close)

	assert.Equal(t, ":10.0", envValue(d.Env, "DISPLAY"))
	assert.Equal(t, opts.XAuthorityPath, envValue(d.Env, "XAUTHORITY"))

	entries, err := ReadFile(opts.XAuthorityPath)
	require.NoError(t, err)
	entry, ok := Lookup(entries, FamilyLocal, "testhost", "10")
	require.True(t, ok, "cookie registered for display 10")
	cookie, _ := ParseCookie(testCookie)
	assert.Equal(t, cookie, entry.Data)

	info, err := os.Lstat(opts.SocketDir)
	require.NoError(t, err)
	assert.Equal(t, os.ModeSticky|0o777, info.Mode()&(os.ModeSticky|os.ModePerm))

	echoClientSide(t, chans)
	x, err := net.Dial("unix", filepath.Join(opts.SocketDir, "X10"))
	require.NoError(t, err)
	_, err = x.Write([]byte("hello"))
	require.NoError(t, err)
	reply, err := io.ReadAll(x)
	require.NoError(t, err)
	assert.Equal(t, "re:hello", string(reply))
	_ = x.Close()

	d.Close()
	_, err = os.Lstat(filepath.Join(opts.SocketDir, "X10"))
	assert.True(t, os.IsNotExist(err), "socket removed on close")
	entries, err = ReadFile(opts.XAuthorityPath)
	require.NoError(t, err)
	_, ok = Lookup(entries, FamilyLocal, "testhost", "10")
	assert.False(t, ok, "cookie removed on close")
}

func TestStartDisplay_SkipsDisplaysInUse(t *testing.T) {
	conn, _ := sshPair(t)
	opts := testOptions(t)
	require.NoError(t, os.MkdirAll(opts.SocketDir, 0o755))
	live, err := net.Listen("unix", filepath.Join(opts.SocketDir, "X10"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = live.Close() })
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(opts.SocketDir), ".X11-lock"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(opts.SocketDir, "X12"), nil, 0o600))

	d, err := StartDisplay(conn, Request{AuthProtocol: AuthProtoMITMagicCookie, AuthCookie: testCookie}, opts)
	require.NoError(t, err)
	t.Cleanup(d.Close)
	assert.Equal(t, ":13.0", envValue(d.Env, "DISPLAY"),
		"skips a live socket, an X server lock file and a non-socket file")
	_, err = os.Lstat(filepath.Join(opts.SocketDir, "X12"))
	assert.NoError(t, err, "a non-socket file is never removed")
}

func TestStartDisplay_ReusesStaleSocket(t *testing.T) {
	conn, _ := sshPair(t)
	opts := testOptions(t)
	require.NoError(t, os.MkdirAll(opts.SocketDir, 0o755))
	// A forwarder killed without cleanup leaves its socket file behind.
	stale, err := net.Listen("unix", filepath.Join(opts.SocketDir, "X10"))
	require.NoError(t, err)
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	require.NoError(t, stale.Close())

	d, err := StartDisplay(conn, Request{AuthProtocol: AuthProtoMITMagicCookie, AuthCookie: testCookie}, opts)
	require.NoError(t, err)
	t.Cleanup(d.Close)
	assert.Equal(t, ":10.0", envValue(d.Env, "DISPLAY"))
}

func TestStartDisplay_SingleConnection(t *testing.T) {
	conn, chans := sshPair(t)
	opts := testOptions(t)
	d, err := StartDisplay(conn, Request{
		SingleConnection: true, AuthProtocol: AuthProtoMITMagicCookie, AuthCookie: testCookie,
	}, opts)
	require.NoError(t, err)
	t.Cleanup(d.Close)

	echoClientSide(t, chans)
	socket := filepath.Join(opts.SocketDir, "X10")
	first, err := net.Dial("unix", socket)
	require.NoError(t, err)
	_, _ = first.Write([]byte("hello"))
	_, _ = io.ReadAll(first)
	_ = first.Close()

	assert.Eventually(t, func() bool {
		c, err := net.Dial("unix", socket)
		if err == nil {
			_ = c.Close()
		}
		return err != nil
	}, 2*time.Second, 20*time.Millisecond, "second connection must be refused")
}

func TestStartDisplay_FallsBackToTCPAndSessionXAuthority(t *testing.T) {
	conn, _ := sshPair(t)
	opts := testOptions(t)
	// A file where the socket directory should be makes unix sockets unusable.
	require.NoError(t, os.WriteFile(opts.SocketDir, nil, 0o600))
	// An Xauthority path in a missing directory cannot be updated.
	opts.XAuthorityPath = filepath.Join(opts.FallbackDir, "missing", ".Xauthority")

	d, err := StartDisplay(conn, Request{AuthProtocol: AuthProtoMITMagicCookie, AuthCookie: testCookie, ScreenNumber: 1}, opts)
	require.NoError(t, err)

	display := envValue(d.Env, "DISPLAY")
	assert.True(t, strings.HasPrefix(display, "localhost:"), display)
	assert.True(t, strings.HasSuffix(display, ".1"), display)

	xauth := envValue(d.Env, "XAUTHORITY")
	assert.True(t, strings.HasPrefix(xauth, opts.FallbackDir+string(os.PathSeparator)+"devsy-x11-"), xauth)
	entries, err := ReadFile(xauth)
	require.NoError(t, err)
	assert.Len(t, entries, 1)

	d.Close()
	_, err = os.Lstat(filepath.Dir(xauth))
	assert.True(t, os.IsNotExist(err), "per-session xauthority dir removed on close")
}

func TestStartDisplay_RejectsInvalidRequests(t *testing.T) {
	conn, _ := sshPair(t)
	for _, req := range []Request{
		{AuthProtocol: "XDM-AUTHORIZATION-1", AuthCookie: testCookie},
		{AuthProtocol: AuthProtoMITMagicCookie, AuthCookie: "not-hex"},
		{AuthProtocol: AuthProtoMITMagicCookie, AuthCookie: testCookie, ScreenNumber: -1},
	} {
		_, err := StartDisplay(conn, req, testOptions(t))
		assert.Error(t, err, "%+v", req)
	}
}
