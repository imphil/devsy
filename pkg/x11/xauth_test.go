package x11

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarshalEntries_Format(t *testing.T) {
	data, err := MarshalEntries([]Entry{{
		Family: FamilyLocal, Address: "h", Number: "10", Name: "M", Data: []byte{0xab, 0xcd},
	}})
	require.NoError(t, err)
	assert.Equal(t, []byte{
		0x01, 0x00, // family 256
		0x00, 0x01, 'h',
		0x00, 0x02, '1', '0',
		0x00, 0x01, 'M',
		0x00, 0x02, 0xab, 0xcd,
	}, data)
}

func TestParseEntries_RoundTrip(t *testing.T) {
	entries := []Entry{
		{Family: FamilyLocal, Address: "host", Number: "10", Name: AuthProtoMITMagicCookie, Data: []byte{1, 2, 3}},
		{Family: 0, Address: "\x7f\x00\x00\x01", Number: "0", Name: AuthProtoMITMagicCookie, Data: []byte{4}},
	}
	data, err := MarshalEntries(entries)
	require.NoError(t, err)
	parsed, err := ParseEntries(data)
	require.NoError(t, err)
	assert.Equal(t, entries, parsed)

	_, err = ParseEntries(data[:len(data)-1])
	assert.Error(t, err, "truncated input must be rejected")
}

func TestParseCookie(t *testing.T) {
	cookie, err := ParseCookie("00ff10")
	require.NoError(t, err)
	assert.Equal(t, []byte{0x00, 0xff, 0x10}, cookie)

	for _, bad := range []string{"", "abc", "zz", string(make([]byte, 2*maxCookieBytes+2))} {
		_, err := ParseCookie(bad)
		assert.Error(t, err, "cookie %q", bad)
	}
}

func TestUpsertAndRemoveEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".Xauthority")
	other := Entry{Family: FamilyLocal, Address: "host", Number: "0", Name: AuthProtoMITMagicCookie, Data: []byte{9}}
	require.NoError(t, UpsertEntry(path, other))

	ours := Entry{Family: FamilyLocal, Address: "host", Number: "10", Name: AuthProtoMITMagicCookie, Data: []byte{1}}
	require.NoError(t, UpsertEntry(path, ours))
	ours.Data = []byte{2}
	require.NoError(t, UpsertEntry(path, ours), "upsert replaces the entry for the same display")

	entries, err := ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, []Entry{other, ours}, entries)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	require.NoError(t, RemoveEntry(path, FamilyLocal, "host", "10"))
	entries, err = ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, []Entry{other}, entries, "only our entry is removed")

	for _, lock := range []string{path + "-c", path + "-l"} {
		_, err := os.Lstat(lock)
		assert.True(t, os.IsNotExist(err), "lock file %s left behind", lock)
	}
}

func TestUpsertEntry_BreaksStaleLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".Xauthority")
	stale := path + "-c"
	require.NoError(t, os.WriteFile(stale, nil, 0o600))
	old := staleTime()
	require.NoError(t, os.Chtimes(stale, old, old))

	require.NoError(t, UpsertEntry(path, Entry{Family: FamilyLocal, Number: "10"}))
}

func TestRemoveEntry_MissingFile(t *testing.T) {
	assert.NoError(t, RemoveEntry(filepath.Join(t.TempDir(), "none"), FamilyLocal, "h", "10"))
}
