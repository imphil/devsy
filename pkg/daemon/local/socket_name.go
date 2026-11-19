package local

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/devsy-org/devsy/pkg/config"
)

// socketHashLen is the number of hex characters of the home-path hash used in
// the socket name. 12 hex chars (48 bits) keeps collisions between homes on one
// machine negligible while leaving the unix socket path far below the 104-byte
// sun_path limit on macOS.
const socketHashLen = 12

// socketName returns the daemon socket (unix) or pipe (windows) name for the
// active Devsy home.
//
// Without DEVSY_HOME the historical name is kept, so existing clients keep
// finding the default daemon. With DEVSY_HOME every home gets its own daemon:
// otherwise a second home (e.g. a frontend running with an isolated home) would
// connect to the default home's daemon and read the wrong workspaces.
//
// Clients that talk to the daemon directly must derive the same name: sha256
// over the absolute, cleaned home path, first socketHashLen hex characters.
func socketName() string {
	return socketNameForHome(os.Getenv(config.EnvHome))
}

func socketNameForHome(home string) string {
	if home == "" {
		return socketSuffix
	}

	if abs, err := filepath.Abs(home); err == nil {
		home = abs
	}

	sum := sha256.Sum256([]byte(filepath.Clean(home)))

	return fmt.Sprintf("devsy-local-%s.sock", hex.EncodeToString(sum[:])[:socketHashLen])
}
