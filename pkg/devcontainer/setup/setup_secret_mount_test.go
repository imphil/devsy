package setup

import (
	"errors"
	"testing"

	"github.com/devsy-org/devsy/pkg/devcontainer/config"
	"github.com/stretchr/testify/require"
)

func TestWriteSecretFilesRequiresTmpfsMount(t *testing.T) {
	mountErr := errors.New("required tmpfs mount is missing")
	originalValidator := validateSecretFileRuntime
	validateSecretFileRuntime = func(string) error { return mountErr }
	t.Cleanup(func() { validateSecretFileRuntime = originalValidator })

	err := writeSecretFiles(&ContainerSetupConfig{
		SecretsMount: []string{"FILE_SECRET=sentinel-value"},
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
		SecretsMount: []string{"FILE_SECRET=sentinel-value"},
	})
	require.ErrorIs(t, err, configuredErr)

	setupInfo.MergedConfig.Mounts = nil
	err = writeSecretFiles(&ContainerSetupConfig{
		SetupInfo:    setupInfo,
		SecretsMount: []string{"FILE_SECRET=sentinel-value"},
	})
	require.ErrorIs(t, err, tmpfsErr)
}
