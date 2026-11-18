package provider

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/devsy-org/devsy/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	sourceLocationTestRepo   = "https://github.com/org/source-location"
	sourceLocationTestFolder = "/srv/source-location"
)

func TestResolveSourceLocation(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	git := WorkspaceSource{GitRepository: sourceLocationTestRepo}
	volumeName := ContentVolumeName(sourceLocationTestRepo)

	volumeWorkspace := &Workspace{
		ID: "vol", UID: "default-vo-12345", Source: git,
		CloneIntoVolume: true, ContentVolumeName: volumeName,
	}
	assert.Equal(t, &WorkspaceSourceLocation{
		Kind: SourceLocationVolume, VolumeName: volumeName, Path: "worktrees/default-vo-12345",
	}, ResolveSourceLocation(volumeWorkspace), "the worktree is named after the runner ID (UID)")
	volumeWorkspace.UID = "legacy"
	assert.Equal(t, "worktrees/vol", ResolveSourceLocation(volumeWorkspace).Path,
		"legacy UIDs fall back to the workspace ID")

	local := ResolveSourceLocation(&Workspace{
		ID: "loc", Source: WorkspaceSource{LocalFolder: sourceLocationTestFolder},
	})
	assert.Equal(t, &WorkspaceSourceLocation{
		Kind: SourceLocationLocalFolder, Path: sourceLocationTestFolder,
	}, local)

	remote := ResolveSourceLocation(&Workspace{
		ID: "rem", Source: git, Machine: WorkspaceMachineConfig{ID: "vm"},
	})
	assert.Equal(t, &WorkspaceSourceLocation{Kind: SourceLocationRemote}, remote)

	ws := &Workspace{ID: "host", Context: "ctx", Source: git}
	assert.Nil(t, ResolveSourceLocation(ws), "unknown until the clone folder exists")
	dir, err := GetWorkspaceContentDir(ws.Context, ws.ID)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o750))
	assert.Equal(t,
		&WorkspaceSourceLocation{Kind: SourceLocationHostFolder, Path: dir},
		ResolveSourceLocation(ws))

	assert.Nil(
		t,
		ResolveSourceLocation(&Workspace{ID: "img", Source: WorkspaceSource{Image: "ubuntu"}}),
	)
}

func TestWorkspaceListEntryJSONIncludesSourceLocation(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	out, err := json.Marshal(NewWorkspaceListEntry(&Workspace{
		ID: "loc", Source: WorkspaceSource{LocalFolder: sourceLocationTestFolder},
	}))
	require.NoError(t, err)
	assert.Contains(t, string(out),
		`"sourceLocation":{"kind":"localFolder","path":"`+sourceLocationTestFolder+`"}`)
}
