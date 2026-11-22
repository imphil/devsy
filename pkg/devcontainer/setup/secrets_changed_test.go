package setup

import (
	"context"
	"os/user"
	"testing"

	"github.com/devsy-org/devsy/pkg/devcontainer/config"
	"github.com/devsy-org/devsy/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	secretsTestDir = "/workspaces/test"
	secretA1       = "A=1"
	secretB2       = "B=2"
)

func secretsChangedResult(customizations ...any) *config.Result {
	return &config.Result{
		MergedConfig: &config.MergedDevContainerConfig{
			UpdatedConfigProperties: config.UpdatedConfigProperties{
				Customizations: map[string][]any{"devsy": customizations},
			},
		},
		ContainerDetails:    &config.ContainerDetails{},
		SubstitutionContext: &config.SubstitutionContext{ContainerWorkspaceFolder: secretsTestDir},
	}
}

func TestSecretsDigestIgnoresOrderAndNoticesChanges(t *testing.T) {
	a := secretsDigest([]string{secretA1, secretB2})
	assert.Equal(t, a, secretsDigest([]string{secretB2, secretA1}))
	assert.NotEqual(t, a, secretsDigest([]string{secretA1, "B=3"}))
	assert.NotEqual(t, a, secretsDigest([]string{secretA1}), "a removed secret is a change")
	// The separator keeps "A=1", "B=2" apart from "A=1B=2".
	joined := secretsDigest([]string{"A=1B=2"})
	assert.NotEqual(t, a, joined)
	assert.Len(t, a, 64)
}

func TestSecretsChangedPhaseNeedsCommandAndSecrets(t *testing.T) {
	env := lifecycleEnv{remoteUser: "dev", workspaceFolder: secretsTestDir}
	hook := map[string]any{string(PhaseSecretsChanged): "update-config"}

	ctx := context.Background()
	assert.Nil(t, secretsChangedPhase(ctx, secretsChangedResult(), env, []string{secretA1}),
		"no command configured")
	assert.Nil(t, secretsChangedPhase(ctx, secretsChangedResult(hook), env, nil),
		"no mount secrets passed")

	ph := secretsChangedPhase(ctx, secretsChangedResult(hook), env, []string{secretA1})
	require.NotNil(t, ph)
	assert.Equal(t, PhaseSecretsChanged, ph.phase)
	assert.Equal(t, "onSecretsChangedCommand", ph.params.hook)
	assert.Equal(t, []types.LifecycleHook{{"": {"update-config"}}}, ph.params.commands)
	assert.Equal(t, secretsDigest([]string{secretA1}), ph.params.content,
		"the run marker is the digest, so the hook runs once per set of values")
	assert.True(t, ph.continueOnError)
}

func TestSecretsChangedFailureDoesNotStopLaterPhases(t *testing.T) {
	currentUser, err := user.Current()
	require.NoError(t, err)
	env := lifecycleEnv{remoteUser: currentUser.Username, workspaceFolder: t.TempDir()}
	failing := phaseHook{
		phase: PhaseSecretsChanged,
		params: hookRunParams{
			ctx:      context.Background(),
			commands: []types.LifecycleHook{{"": {"sh", "-c", "exit 3"}}},
			env:      env,
			name:     "onSecretsChangedCommands",
		},
		continueOnError: true,
	}
	assert.NoError(t, runPhaseHook(failing))

	failing.continueOnError = false
	assert.Error(t, runPhaseHook(failing), "other phases still stop on failure")
}
