package devcontainer

import (
	"context"
	"errors"
	"fmt"

	"github.com/devsy-org/devsy/pkg/agent/delivery"
	"github.com/devsy-org/devsy/pkg/devcontainer/config"
	"github.com/devsy-org/devsy/pkg/log"
	"github.com/devsy-org/devsy/pkg/provider"
)

type teardownStep struct {
	name     string
	required bool
	run      func(ctx context.Context) error
}

type teardownPlan struct {
	steps []teardownStep
}

func (t *teardownPlan) add(name string, required bool, run func(ctx context.Context) error) {
	t.steps = append(t.steps, teardownStep{name: name, required: required, run: run})
}

func (t *teardownPlan) execute(ctx context.Context) error {
	var errs []error
	for _, step := range t.steps {
		err := step.run(ctx)
		if err == nil {
			continue
		}
		if !step.required {
			log.Warnf("%s: %v", step.name, err)
			continue
		}
		errs = append(errs, fmt.Errorf("%s: %w", step.name, err))
	}
	return errors.Join(errs...)
}

func (r *runner) buildTeardownPlan(
	details *config.ContainerDetails,
	options DeleteOptions,
) *teardownPlan {
	plan := &teardownPlan{}

	if r.isImportedWorkspace() {
		log.Debug("skipping container deletion, since it was not created by Devsy")
	} else if details != nil {
		r.addContainerTeardown(plan, details, options)
	}
	r.addLeftoverTeardown(plan, options)

	return plan
}

func (r *runner) addContainerTeardown(
	plan *teardownPlan,
	details *config.ContainerDetails,
	options DeleteOptions,
) {
	if isCompose, projectName := getDockerComposeProject(details); isCompose {
		plan.add("docker compose project", true, func(ctx context.Context) error {
			return r.deleteDockerCompose(ctx, projectName, options.RemoveVolumes)
		})
		return
	}

	if options.RemoveVolumes {
		log.Debugf("--remove-volumes only removes declared volumes for docker compose workspaces")
	}
	if details.State.Status == config.ContainerStatusRunning {
		plan.add("stop devcontainer", true, func(ctx context.Context) error {
			return r.driver.StopDevContainer(ctx, r.id)
		})
	}
	plan.add("delete devcontainer", true, func(ctx context.Context) error {
		return r.driver.DeleteDevContainer(ctx, r.id)
	})
}

func (r *runner) addLeftoverTeardown(plan *teardownPlan, options DeleteOptions) {
	plan.add("delivery volume cleanup", false, func(ctx context.Context) error {
		return r.cleanupDeliveryVolume(ctx, options.PreserveContentVolume)
	})
	plan.add("imported devcontainer cleanup", false, func(ctx context.Context) error {
		return r.cleanupImportedDevContainer()
	})
}

// cleanupDeliveryVolume removes the delivery's managed volumes for this
// workspace. Cleanup never touches a --clone-into-volume content volume,
// which is shared by every workspace of the same repository; a true delete
// (not preserveContent, as used by a recreate) additionally releases this
// workspace's worktree in it, removing the volume once no worktree is left.
func (r *runner) cleanupDeliveryVolume(ctx context.Context, preserveContent bool) error {
	strategy := r.newAgentDelivery()
	err := strategy.Cleanup(ctx, r.id)
	if preserveContent {
		return err
	}
	ws := r.cloneIntoVolumeWorkspace()
	if ws == nil {
		return err
	}
	releaser, ok := strategy.(delivery.ContentVolumeReleaser)
	if !ok {
		return err
	}
	return errors.Join(err, releaser.ReleaseContentWorktree(
		ctx, ws.ContentVolumeName, provider.ContentVolumeSubpath(r.id),
	))
}

// cloneIntoVolumeWorkspace returns the runner's workspace if its content
// lives in a shared content volume, or nil otherwise.
func (r *runner) cloneIntoVolumeWorkspace() *provider.Workspace {
	if r.workspaceConfig == nil || r.workspaceConfig.Workspace == nil {
		return nil
	}
	ws := r.workspaceConfig.Workspace
	if !ws.CloneIntoVolume || ws.ContentVolumeName == "" {
		return nil
	}
	return ws
}

func (r *runner) findTeardownContainer(
	ctx context.Context,
) (*config.ContainerDetails, error) {
	if r.isImportedWorkspace() {
		return nil, nil
	}
	return r.driver.FindDevContainer(ctx, r.id)
}

func (r *runner) isImportedWorkspace() bool {
	return r.workspaceConfig != nil &&
		r.workspaceConfig.Workspace != nil &&
		r.workspaceConfig.Workspace.Source.Container != ""
}
