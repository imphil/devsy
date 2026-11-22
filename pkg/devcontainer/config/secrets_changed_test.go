package config

import (
	"encoding/json"
	"testing"

	"github.com/devsy-org/devsy/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const secretsChangedKey = "onSecretsChangedCommand"

func TestSecretsChangedCommandsFromDevcontainerJSON(t *testing.T) {
	var cfg DevContainerConfig
	require.NoError(t, json.Unmarshal([]byte(`{
		"image": "alpine",
		"customizations": {
			"devsy": { "onSecretsChangedCommand": "update-credentials" },
			"vscode": { "extensions": ["a.b"] }
		}
	}`), &cfg))

	merged, err := MergeConfiguration(&cfg, nil)
	require.NoError(t, err)
	assert.Equal(t,
		[]types.LifecycleHook{{"": {"update-credentials"}}},
		SecretsChangedCommands(merged))
}

func TestSecretsChangedCommandsForms(t *testing.T) {
	merged := &MergedDevContainerConfig{UpdatedConfigProperties: UpdatedConfigProperties{
		Customizations: map[string][]any{"devsy": {
			map[string]any{secretsChangedKey: []any{"sh", "-c", "a"}},
			map[string]any{"prebuildRepository": "x"},
			map[string]any{secretsChangedKey: 42},
			map[string]any{secretsChangedKey: map[string]any{"netrc": "b", "jfrog": "c"}},
		}},
	}}
	assert.Equal(t, []types.LifecycleHook{
		{"": {"sh", "-c", "a"}},
		{"netrc": {"b"}, "jfrog": {"c"}},
	}, SecretsChangedCommands(merged))

	assert.Nil(t, SecretsChangedCommands(&MergedDevContainerConfig{}))
	assert.Nil(t, SecretsChangedCommands(nil))
}
