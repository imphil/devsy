package devcontainer

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/devsy-org/devsy/pkg/devcontainer/config"
)

// defaultConsistency is applied to the workspace mount on non-Linux hosts, where
// the file sharing layer benefits from an explicit consistency mode.
const defaultConsistency = "consistent"

func needsDefaultConsistency() bool {
	return runtime.GOOS != goosLinux
}

// contentVolumeMount identifies the named docker volume (and subpath within
// it) backing a --clone-into-volume workspace's content, for getWorkspace to
// mount instead of a host bind path.
type contentVolumeMount struct {
	name    string
	subpath string
}

// getWorkspace resolves the workspace mount string and the container-side
// mount folder. The mount string is empty when the config suppresses the
// mount. volume is nil for a plain host-bind-mounted workspace and non-nil
// for a --clone-into-volume workspace, whose content lives in a docker
// volume rather than on host disk (see runner.contentVolumeMount).
func getWorkspace(
	workspaceFolder, workspaceID string,
	conf *config.DevContainerConfig,
	volume *contentVolumeMount,
) (mount, containerFolder string) {
	if conf.WorkspaceMount == nil {
		containerFolder = containerMountFolder(conf, workspaceID)
		if volume != nil {
			// A named volume is a daemon-level object, not a host path: it has
			// no host<->container filesystem translation layer, so the
			// consistency option (a bind-mount-only Docker Desktop knob) does
			// not apply here the way it does below for a host bind mount.
			return fmt.Sprintf(
				"type=volume,source=%s,target=%s,volume-subpath=%s",
				volume.name, containerFolder, volume.subpath,
			), containerFolder
		}
		return withDefaultConsistency(fmt.Sprintf(
			"type=bind,source=%s,target=%s",
			workspaceFolder,
			containerFolder,
		)), containerFolder
	}

	if *conf.WorkspaceMount == "" {
		return "", containerMountFolder(conf, workspaceID)
	}

	mount = withDefaultConsistency(*conf.WorkspaceMount)
	return mount, config.ParseMount(mount).Target
}

// contentVolumeMount returns the content volume/subpath to mount as the
// workspace folder when this invocation is running inside the disposable
// bootstrap container for a --clone-into-volume workspace (see
// provider.AgentWorkspaceInfo.ContentVolumeMounted), or nil for a plain
// host-bind-mounted workspace.
func (r *runner) contentVolumeMount() *contentVolumeMount {
	wc := r.workspaceConfig
	if wc == nil || !wc.ContentVolumeMounted || wc.Workspace == nil {
		return nil
	}
	return &contentVolumeMount{
		name:    wc.Workspace.ContentVolumeName,
		subpath: wc.ContentVolumeSubpath,
	}
}

func containerMountFolder(conf *config.DevContainerConfig, workspaceID string) string {
	if conf.WorkspaceFolder != "" {
		return conf.WorkspaceFolder
	}
	return "/workspaces/" + workspaceID
}

// withDefaultConsistency adds the default consistency mode to a mount string on
// hosts that need it, unless the mount already specifies one.
func withDefaultConsistency(mount string) string {
	if !needsDefaultConsistency() || mountHasConsistency(mount) {
		return mount
	}
	return mount + ",consistency='" + defaultConsistency + "'"
}

func mountHasConsistency(mount string) bool {
	for part := range strings.SplitSeq(mount, ",") {
		if strings.HasPrefix(part, "consistency=") {
			return true
		}
	}
	return false
}

// mountSetConsistency returns the mount string with its consistency option set
// to value, replacing any existing value or appending one when absent.
func mountSetConsistency(mount, value string) string {
	// An empty mount is the "suppress the workspace mount" signal; keep it empty
	// rather than synthesizing a malformed consistency-only mount.
	if mount == "" {
		return ""
	}

	quoted := "consistency='" + value + "'"

	replaced := false
	var parts []string
	for part := range strings.SplitSeq(mount, ",") {
		if strings.HasPrefix(part, "consistency=") {
			parts = append(parts, quoted)
			replaced = true
		} else {
			parts = append(parts, part)
		}
	}
	if !replaced {
		parts = append(parts, quoted)
	}
	return strings.Join(parts, ",")
}
