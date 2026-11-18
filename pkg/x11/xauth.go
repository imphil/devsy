// Package x11 implements the pieces of SSH X11 forwarding devsy needs without
// depending on the xauth binary: reading and updating Xauthority files and
// serving a forwarded display inside a workspace container.
package x11

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"
)

const (
	// FamilyLocal is the Xauthority address family for local (unix socket,
	// and to libxcb also loopback TCP) connections; its address is the
	// machine's hostname.
	FamilyLocal uint16 = 256

	// AuthProtoMITMagicCookie is the only X11 authentication protocol
	// devsy forwards.
	AuthProtoMITMagicCookie = "MIT-MAGIC-COOKIE-1"

	maxCookieBytes = 256

	// Wait longer for the lock than it takes a lock to count as stale, so a
	// lock left behind by a killed process is always broken in time.
	lockRetries   = 100
	lockRetryWait = 100 * time.Millisecond
	// staleLockAge mirrors libXau's XauLockAuth "dead" timeout: a lock file
	// older than this is assumed to belong to a killed process. Updates
	// hold the lock for milliseconds.
	staleLockAge = 5 * time.Second
)

// Entry is one Xauthority record.
type Entry struct {
	Family  uint16
	Address string
	Number  string
	Name    string
	Data    []byte
}

// ParseCookie decodes a hex-encoded MIT-MAGIC-COOKIE-1 value as carried in an
// SSH x11-req.
func ParseCookie(hexCookie string) ([]byte, error) {
	cookie, err := hex.DecodeString(hexCookie)
	if err != nil {
		return nil, fmt.Errorf("invalid X11 cookie: %w", err)
	}
	if len(cookie) == 0 || len(cookie) > maxCookieBytes {
		return nil, fmt.Errorf("invalid X11 cookie length %d", len(cookie))
	}
	return cookie, nil
}

// MarshalEntries encodes entries in the Xauthority file format: per entry a
// big-endian uint16 family followed by address, number, name and data, each
// as a big-endian uint16 length and the bytes.
func MarshalEntries(entries []Entry) ([]byte, error) {
	var out []byte
	for _, e := range entries {
		out = binary.BigEndian.AppendUint16(out, e.Family)
		for _, field := range [][]byte{[]byte(e.Address), []byte(e.Number), []byte(e.Name), e.Data} {
			if len(field) > math.MaxUint16 {
				return nil, fmt.Errorf("xauthority field too long")
			}
			out = binary.BigEndian.AppendUint16(out, uint16(len(field))) // #nosec G115 -- checked above
			out = append(out, field...)
		}
	}
	return out, nil
}

// ParseEntries decodes an Xauthority file.
func ParseEntries(data []byte) ([]Entry, error) {
	var entries []Entry
	for len(data) > 0 {
		if len(data) < 2 {
			return nil, errors.New("truncated xauthority entry")
		}
		e := Entry{Family: binary.BigEndian.Uint16(data)}
		data = data[2:]
		fields := make([][]byte, 4)
		for i := range fields {
			if len(data) < 2 {
				return nil, errors.New("truncated xauthority entry")
			}
			n := int(binary.BigEndian.Uint16(data))
			data = data[2:]
			if len(data) < n {
				return nil, errors.New("truncated xauthority entry")
			}
			fields[i] = data[:n]
			data = data[n:]
		}
		e.Address, e.Number, e.Name = string(fields[0]), string(fields[1]), string(fields[2])
		e.Data = append([]byte(nil), fields[3]...)
		entries = append(entries, e)
	}
	return entries, nil
}

// Lookup returns the cookie for the given family, address and display number.
func Lookup(entries []Entry, family uint16, address, number string) (Entry, bool) {
	for _, e := range entries {
		if e.Family == family && e.Address == address && e.Number == number {
			return e, true
		}
	}
	return Entry{}, false
}

// ReadFile reads and parses an Xauthority file. A missing file has no entries.
func ReadFile(path string) ([]Entry, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- caller-chosen xauthority path
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return ParseEntries(data)
}

// UpsertEntry replaces any entry with the same family, address and number in
// the Xauthority file at path with e, creating the file if needed. It holds
// the libXau lock while doing so and replaces the file atomically.
func UpsertEntry(path string, e Entry) error {
	return updateLocked(path, func(entries []Entry) []Entry {
		return append(withoutEntry(entries, e.Family, e.Address, e.Number), e)
	})
}

// RemoveEntry removes the entry with the given family, address and number
// from the Xauthority file at path, if present.
func RemoveEntry(path string, family uint16, address, number string) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return updateLocked(path, func(entries []Entry) []Entry {
		return withoutEntry(entries, family, address, number)
	})
}

func withoutEntry(entries []Entry, family uint16, address, number string) []Entry {
	out := entries[:0:0]
	for _, e := range entries {
		if e.Family == family && e.Address == address && e.Number == number {
			continue
		}
		out = append(out, e)
	}
	return out
}

func updateLocked(path string, update func([]Entry) []Entry) error {
	unlock, err := lockFile(path)
	if err != nil {
		return err
	}
	defer unlock()

	entries, err := ReadFile(path)
	if err != nil {
		return err
	}
	data, err := MarshalEntries(update(entries))
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := preserveOwner(tmp.Name(), path); err != nil {
		return err
	}
	// CreateTemp already creates the file 0600, matching xauth.
	return os.Rename(tmp.Name(), path)
}

// lockFile takes the lock libXau's XauLockAuth uses: create "<path>-c"
// exclusively, then hard-link it to "<path>-l". Stale locks are broken.
func lockFile(path string) (func(), error) {
	creat, link := path+"-c", path+"-l"
	for range lockRetries {
		breakStaleLock(creat)
		breakStaleLock(link)
		f, err := os.OpenFile(creat, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304
		if err == nil {
			_ = f.Close()
			if err := os.Link(creat, link); err == nil {
				return func() {
					_ = os.Remove(link)
					_ = os.Remove(creat)
				}, nil
			}
			_ = os.Remove(creat)
		} else if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		time.Sleep(lockRetryWait)
	}
	return nil, fmt.Errorf("lock %s: timed out", path)
}

func breakStaleLock(path string) {
	info, err := os.Lstat(path)
	if err == nil && time.Since(info.ModTime()) > staleLockAge {
		_ = os.Remove(path)
	}
}
