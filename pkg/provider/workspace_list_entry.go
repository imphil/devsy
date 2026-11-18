package provider

import "os"

type WorkspaceListEntry struct {
	*Workspace

	DisplayName          string                   `json:"displayName"`
	SSHHostAlias         string                   `json:"sshHostAlias"`
	EffectiveDisplayName string                   `json:"effectiveDisplayName"`
	PreferredSSHHost     string                   `json:"preferredSSHHost"`
	CanonicalSSHHost     string                   `json:"canonicalSSHHost"`
	SourceLocation       *WorkspaceSourceLocation `json:"sourceLocation,omitempty"`
}

// Kinds of WorkspaceSourceLocation.
const (
	// SourceLocationVolume: a --clone-into-volume clone, in VolumeName at
	// Path (the workspace's worktree inside the shared content volume).
	SourceLocationVolume = "volume"
	// SourceLocationHostFolder: a git clone in a folder on this machine
	// (Path), bind-mounted into the container.
	SourceLocationHostFolder = "hostFolder"
	// SourceLocationLocalFolder: the user's own folder (Path), used as the
	// source directly rather than cloned.
	SourceLocationLocalFolder = "localFolder"
	// SourceLocationRemote: the source lives on the provider's machine or
	// platform, not on this machine.
	SourceLocationRemote = "remote"
)

// WorkspaceSourceLocation says where a workspace's source code lives.
type WorkspaceSourceLocation struct {
	Kind       string `json:"kind"`
	Path       string `json:"path,omitempty"`
	VolumeName string `json:"volumeName,omitempty"`
}

func NewWorkspaceListEntry(ws *Workspace) *WorkspaceListEntry {
	return &WorkspaceListEntry{
		Workspace:            ws,
		DisplayName:          ws.DisplayName,
		SSHHostAlias:         ws.SSHHostAlias,
		EffectiveDisplayName: ws.EffectiveDisplayName(),
		PreferredSSHHost:     ws.PreferredSSHHost(),
		CanonicalSSHHost:     ws.CanonicalSSHHost(),
		SourceLocation:       ResolveSourceLocation(ws),
	}
}

func NewWorkspaceListEntries(workspaces []*Workspace) []*WorkspaceListEntry {
	if workspaces == nil {
		return nil
	}

	entries := make([]*WorkspaceListEntry, 0, len(workspaces))
	for _, ws := range workspaces {
		entries = append(entries, NewWorkspaceListEntry(ws))
	}

	return entries
}

// ResolveSourceLocation returns where ws's source lives, or nil if there is
// none (image and container sources) or it is not known yet: a git clone on
// this machine is reported once its folder exists, which also tells it apart
// from providers that clone elsewhere without a machine of their own.
func ResolveSourceLocation(ws *Workspace) *WorkspaceSourceLocation {
	switch {
	case ws.CloneIntoVolume && ws.ContentVolumeName != "":
		return &WorkspaceSourceLocation{
			Kind:       SourceLocationVolume,
			VolumeName: ws.ContentVolumeName,
			Path:       ContentVolumeSubpath(ws.RunnerID()),
		}
	case ws.Source.LocalFolder != "" && !ws.IsPlatform():
		return &WorkspaceSourceLocation{
			Kind: SourceLocationLocalFolder,
			Path: ws.Source.LocalFolder,
		}
	case !hasSourceCode(ws):
		return nil
	case clonesRemotely(ws):
		return &WorkspaceSourceLocation{Kind: SourceLocationRemote}
	default:
		return hostFolderSourceLocation(ws)
	}
}

func hasSourceCode(ws *Workspace) bool {
	return ws.Source.GitRepository != "" || ws.Source.LocalFolder != ""
}

// clonesRemotely reports whether the source is cloned on a provider machine
// or by the platform rather than on this machine.
func clonesRemotely(ws *Workspace) bool {
	return ws.IsPlatform() || ws.Machine.ID != ""
}

func hostFolderSourceLocation(ws *Workspace) *WorkspaceSourceLocation {
	dir, err := GetWorkspaceContentDir(ws.Context, ws.ID)
	if err != nil {
		return nil
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil
	}
	return &WorkspaceSourceLocation{Kind: SourceLocationHostFolder, Path: dir}
}
