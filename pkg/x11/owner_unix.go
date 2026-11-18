//go:build !windows

package x11

import (
	"os"
	"syscall"
)

// preserveOwner gives the replacement file tmp the owner of the file it is
// about to replace, so a root server updating a user's ~/.Xauthority does
// not take it over.
func preserveOwner(tmp, original string) error {
	info, err := os.Lstat(original)
	if err != nil {
		return nil //nolint:nilerr // nothing to preserve for a new file
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (int(st.Uid) == os.Geteuid() && int(st.Gid) == os.Getegid()) {
		return nil
	}
	return os.Lchown(tmp, int(st.Uid), int(st.Gid))
}
