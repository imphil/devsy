package local

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/devsy-org/devsy/pkg/config"
)

func TestSocketName_DefaultHomeKeepsHistoricalName(t *testing.T) {
	t.Setenv(config.EnvHome, "")

	if got := socketName(); got != socketSuffix {
		t.Fatalf("socketName() = %q, want %q", got, socketSuffix)
	}
}

func TestSocketName_HomeOverrideIsIsolatedAndStable(t *testing.T) {
	homeA := t.TempDir()
	homeB := t.TempDir()

	t.Setenv(config.EnvHome, homeA)
	first := socketName()
	second := socketName()

	if first != second {
		t.Fatalf("socketName() not stable: %q vs %q", first, second)
	}
	if first == socketSuffix {
		t.Fatalf("socketName() with DEVSY_HOME must differ from the default %q", socketSuffix)
	}
	if !strings.HasPrefix(first, "devsy-local-") || !strings.HasSuffix(first, ".sock") {
		t.Fatalf("unexpected socket name format %q", first)
	}

	t.Setenv(config.EnvHome, homeB)
	if other := socketName(); other == first {
		t.Fatalf("different homes share socket name %q", first)
	}
}

func TestSocketName_EquivalentPathsMatch(t *testing.T) {
	home := t.TempDir()

	want := socketNameForHome(home)
	for _, variant := range []string{
		home + string(filepath.Separator),
		filepath.Join(home, "sub", ".."),
	} {
		if got := socketNameForHome(variant); got != want {
			t.Errorf("socketNameForHome(%q) = %q, want %q", variant, got, want)
		}
	}
}

func TestSocketName_KnownValue(t *testing.T) {
	// Keep in sync with desktop/src/main/__tests__/daemon-client.test.ts.
	if filepath.Separator != '/' {
		t.Skip("known value uses a unix path")
	}

	const want = "devsy-local-c0ea3dbb9f45.sock"
	if got := socketNameForHome("/home/user/.silicon-harbor"); got != want {
		t.Fatalf("socketNameForHome() = %q, want %q", got, want)
	}
}
