package setup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devsy-org/devsy/pkg/devcontainer/config"
	"github.com/stretchr/testify/require"
)

const testFileSecret = "FILE_SECRET=sentinel-value"

func TestWriteSecretFilesRequiresTmpfsMount(t *testing.T) {
	mountErr := errors.New("required tmpfs mount is missing")
	originalValidator := validateSecretFileRuntime
	validateSecretFileRuntime = func(string) error { return mountErr }
	t.Cleanup(func() { validateSecretFileRuntime = originalValidator })

	err := writeSecretFiles(&ContainerSetupConfig{
		SecretsMount: []string{testFileSecret},
	})
	require.ErrorIs(t, err, mountErr)
}

func TestWriteSecretFilesUsesTheConfiguredSecretsMountCheck(t *testing.T) {
	tmpfsErr := errors.New("tmpfs check must not run for a configured mount")
	configuredErr := errors.New("configured mount check ran")
	originalTmpfs, originalConfigured := validateSecretFileRuntime, validateConfiguredSecretFileRuntime
	validateSecretFileRuntime = func(string) error { return tmpfsErr }
	validateConfiguredSecretFileRuntime = func(string) error { return configuredErr }
	t.Cleanup(func() {
		validateSecretFileRuntime, validateConfiguredSecretFileRuntime = originalTmpfs, originalConfigured
	})

	setupInfo := &config.Result{MergedConfig: &config.MergedDevContainerConfig{}}
	setupInfo.MergedConfig.Mounts = []*config.Mount{{
		Type: "volume", Source: "electric-vapor-secrets", Target: config.SecretsMountDir,
	}}
	err := writeSecretFiles(&ContainerSetupConfig{
		SetupInfo:    setupInfo,
		SecretsMount: []string{testFileSecret},
	})
	require.ErrorIs(t, err, configuredErr)

	setupInfo.MergedConfig.Mounts = nil
	err = writeSecretFiles(&ContainerSetupConfig{
		SetupInfo:    setupInfo,
		SecretsMount: []string{testFileSecret},
	})
	require.ErrorIs(t, err, tmpfsErr)
}

func TestWriteSecretFileInReplacesInOneStep(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ARTIFACTORY_TOKEN")

	require.NoError(t, writeSecretFileIn(dir, "ARTIFACTORY_TOKEN", "old", ""))
	before, err := os.Stat(target)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), before.Mode().Perm())

	// A link planted at the target is replaced, not written through.
	outside := filepath.Join(t.TempDir(), "outside")
	require.NoError(t, os.WriteFile(outside, []byte("keep"), 0o600))
	require.NoError(t, os.Remove(target))
	require.NoError(t, os.Symlink(outside, target))

	require.NoError(t, writeSecretFileIn(dir, "ARTIFACTORY_TOKEN", "new", ""))
	got, err := os.ReadFile(target) //nolint:gosec // test file from TempDir
	require.NoError(t, err)
	require.Equal(t, "new", string(got))
	info, err := os.Lstat(target)
	require.NoError(t, err)
	require.True(t, info.Mode().IsRegular(), "the link itself was replaced")
	kept, err := os.ReadFile(outside) //nolint:gosec // test file from TempDir
	require.NoError(t, err)
	require.Equal(t, "keep", string(kept))

	// No temporary files are left behind.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestWriteSecretFileInKeepsReadersWhole(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "netrc")
	a, b := strings.Repeat("a", 64*1024), strings.Repeat("b", 64*1024)
	require.NoError(t, writeSecretFileIn(dir, "netrc", a, ""))

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 200 {
			value := a
			if i%2 == 0 {
				value = b
			}
			if err := writeSecretFileIn(dir, "netrc", value, ""); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for {
		select {
		case <-done:
			return
		default:
		}
		got, err := os.ReadFile(target) //nolint:gosec // test file from TempDir
		require.NoError(t, err, "the file never disappears")
		require.True(t, string(got) == a || string(got) == b, "never a partial file")
	}
}
