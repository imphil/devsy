package delivery

import (
	"context"
	"fmt"
	"io"

	"github.com/devsy-org/devsy/pkg/devcontainer/config"
	"github.com/devsy-org/devsy/pkg/driver"
)

type DeliveryPhase int

const (
	PhasePreStart DeliveryPhase = iota
	PhasePostStart
)

func (p DeliveryPhase) String() string {
	switch p {
	case PhasePreStart:
		return "pre-start"
	case PhasePostStart:
		return "post-start"
	default:
		return fmt.Sprintf("unknown(%d)", int(p))
	}
}

type BinarySourceFunc func(ctx context.Context, arch string) (io.ReadCloser, error)

type PreStartOptions struct {
	WorkspaceID  string
	RunOptions   *driver.RunOptions
	BinarySource BinarySourceFunc
	Arch         string
}

type PostStartOptions struct {
	WorkspaceID               string
	ContainerDetails          *config.ContainerDetails
	BinarySource              BinarySourceFunc
	Arch                      string
	DownloadURL               string
	PreferInContainerDownload bool
	// SkipVersionCheck permits custom agent versions while retaining transfer
	// completeness and executable validation.
	SkipVersionCheck bool
}

// Cleaner removes the resources a delivery created for a workspace. Cleanup is
// best-effort and safe to call when nothing was created.
type Cleaner interface {
	Cleanup(ctx context.Context, workspaceID string) error
}

// ContentVolumeReleaser is implemented by deliveries that manage the shared
// content volume of --clone-into-volume workspaces (see VolumeRoleContent).
// Every workspace cloned from the same repository keeps its worktree in its
// own subpath of one volume, so a workspace delete releases only that
// subpath, and the volume goes away once no worktree is left. Cleaner never
// removes a content volume.
type ContentVolumeReleaser interface {
	ReleaseContentWorktree(ctx context.Context, volumeName, subpath string) error
}

type AgentDelivery interface {
	Cleaner
	Phase() DeliveryPhase
	DeliverPreStart(ctx context.Context, opts PreStartOptions) error
	DeliverPostStart(ctx context.Context, opts PostStartOptions) error
}

// BinarySourcePolicy reports whether delivery consumes the supplied binary source
// and architecture. Shell injection resolves architecture inside the container.
type BinarySourcePolicy interface {
	UsesBinarySource() bool
}

// UsesBinarySource preserves binary streaming for strategies without an explicit policy.
func UsesBinarySource(strategy AgentDelivery) bool {
	if policy, ok := strategy.(BinarySourcePolicy); ok {
		return policy.UsesBinarySource()
	}
	return true
}
