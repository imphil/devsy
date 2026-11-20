package devcontainer

import (
	"fmt"
	"path/filepath"

	"github.com/devsy-org/devsy/pkg/devcontainer/config"
	"github.com/devsy-org/devsy/pkg/driver"
)

func (r *runner) requiresTerminalSecretEnvironment() bool {
	return r.workspaceConfig != nil &&
		len(r.workspaceConfig.CLIOptions.TerminalSecretEnvNames) > 0
}

func hasTerminalSecretEnvironmentMount(details *config.ContainerDetails) bool {
	return hasTmpfsMountAt(details, config.SecretsEnvDir)
}

func hasTmpfsMountAt(details *config.ContainerDetails, target string) bool {
	if details == nil {
		return false
	}
	for _, mount := range details.Mounts {
		if filepath.Clean(mount.Destination) == filepath.Clean(target) {
			return mount.Type == driver.MountTypeTmpfs
		}
	}
	return false
}

// needsSecretFileMountMigration reports whether the container lacks a mount
// at SecretsMountDir for file secrets. Devsy's own is a tmpfs; any other mount
// there came from devsy-config's mounts, which the in-container setup accepts
// only when the configuration declares it (setup.writeSecretFiles).
func needsSecretFileMountMigration(details *config.ContainerDetails, secretsMount []string) bool {
	return len(secretsMount) > 0 && !hasMountAt(details, config.SecretsMountDir)
}

func hasMountAt(details *config.ContainerDetails, target string) bool {
	if details == nil {
		return false
	}
	for _, mount := range details.Mounts {
		if filepath.Clean(mount.Destination) == filepath.Clean(target) {
			return true
		}
	}
	return false
}

// hasMountTarget reports whether mounts (to be created) include target.
func hasMountTarget(mounts []*config.Mount, target string) bool {
	for _, mount := range mounts {
		if mount != nil && filepath.Clean(mount.Target) == filepath.Clean(target) {
			return true
		}
	}
	return false
}

func (r *runner) validateTerminalSecretEnvironmentSupport() error {
	if !r.requiresTerminalSecretEnvironment() ||
		driver.DriverSupportsMountType(r.driver, driver.MountTypeTmpfs) {
		return nil
	}
	return terminalSecretEnvironmentUnsupportedError()
}

func (r *runner) needsTerminalSecretEnvironmentMigration(
	details *config.ContainerDetails,
) bool {
	return r.requiresTerminalSecretEnvironment() &&
		!hasTerminalSecretEnvironmentMount(details)
}

func terminalSecretEnvironmentUnsupportedError() error {
	return fmt.Errorf(
		"the current provider does not support securely injecting workspace secrets into terminal sessions",
	)
}
