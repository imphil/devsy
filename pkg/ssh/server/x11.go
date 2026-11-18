package server

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sync"

	"github.com/devsy-org/devsy/pkg/config"
	copypkg "github.com/devsy-org/devsy/pkg/copy"
	"github.com/devsy-org/devsy/pkg/log"
	"github.com/devsy-org/devsy/pkg/x11"
	"github.com/devsy-org/ssh"
	gossh "golang.org/x/crypto/ssh"
)

func x11Request(req ssh.X11) x11.Request {
	return x11.Request{
		SingleConnection: req.SingleConnection,
		AuthProtocol:     req.AuthProtocol,
		AuthCookie:       req.AuthData,
		ScreenNumber:     req.ScreenNumber,
	}
}

// x11ForwardingCallback accepts the x11-req of every session whose request
// devsy can serve. Set explicitly: the ssh library accepts any request when
// the callback is nil, even though nothing would then serve the display.
func x11ForwardingCallback(_ ssh.Context, req ssh.X11) bool {
	if err := x11Request(req).Validate(); err != nil {
		log.Debugf("rejecting x11 forwarding request: %v", err)
		return false
	}
	return true
}

// setupX11 serves a forwarded display for sess if the client requested X11
// forwarding, adding DISPLAY and XAUTHORITY to cmd's environment. The
// display belongs to userName, whose ~/.Xauthority receives the session's
// cookie; chown gives created files to that user when the server runs as
// someone else. Failures only lose the display, never the session. The
// returned cleanup is nil if no display was set up.
func setupX11(
	sess ssh.Session, cmd *exec.Cmd, userName string, chown bool, displays *x11Displays,
) func() {
	req, ok := sess.X11()
	if !ok {
		return nil
	}
	conn, ok := sess.Context().Value(ssh.ContextKeyConn).(gossh.Conn)
	if !ok {
		log.Debugf("x11 forwarding: no ssh connection in session context")
		return nil
	}

	opts := x11.ServerOptions{XAuthorityPath: xauthorityPath(userName)}
	if runtimeDir, err := config.DefaultPathManager().RuntimeDir(); err == nil {
		if err := os.MkdirAll(runtimeDir, 0o755); err == nil { // #nosec G301 -- same as agent sockets
			opts.FallbackDir = runtimeDir
		}
	}
	if chown && userName != "" {
		opts.Chown = func(path string) error { return copypkg.Chown(path, userName) }
	}

	display, err := x11.StartDisplay(conn, x11Request(req), opts)
	if err != nil {
		log.Warnf("x11 forwarding unavailable: %v", err)
		return nil
	}
	log.Debugf("x11 forwarding enabled: %v", display.Env)
	cmd.Env = append(cmd.Env, display.Env...)
	return displays.add(display)
}

// xauthorityPath returns userName's ~/.Xauthority, or "" if the home
// directory is unknown (a per-session file is used then).
func xauthorityPath(userName string) string {
	var home string
	if userName != "" {
		if u, err := user.Lookup(userName); err == nil {
			home = u.HomeDir
		}
	}
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil && userName == "" {
			home = h
		}
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".Xauthority")
}

// x11Displays tracks a server's active forwarded displays so they can be
// cleaned up when the server stops serving. A stdio server's process exits
// right after Serve returns, which would otherwise kill session handlers
// halfway through removing their socket and ~/.Xauthority entry.
type x11Displays struct {
	mu       sync.Mutex
	displays map[*x11.Display]struct{}
}

func (t *x11Displays) add(d *x11.Display) func() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.displays == nil {
		t.displays = map[*x11.Display]struct{}{}
	}
	t.displays[d] = struct{}{}
	return func() {
		d.Close()
		t.mu.Lock()
		delete(t.displays, d)
		t.mu.Unlock()
	}
}

// closeAll closes every active display and waits until each is cleaned up.
func (t *x11Displays) closeAll() {
	t.mu.Lock()
	displays := make([]*x11.Display, 0, len(t.displays))
	for d := range t.displays {
		displays = append(displays, d)
	}
	t.mu.Unlock()
	for _, d := range displays {
		d.Close()
	}
}
