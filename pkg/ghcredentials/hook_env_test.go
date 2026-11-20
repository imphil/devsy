package ghcredentials

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHookEnvSingleEnterpriseAccount(t *testing.T) {
	secrets, plain := HookEnv([]Account{{Host: "github.ibm.com", User: "dev", Token: "ghe"}})
	require.Equal(t, []string{"GH_ENTERPRISE_TOKEN=ghe"}, secrets)
	require.Equal(t, []string{"GH_HOST=github.ibm.com"}, plain)
}

func TestHookEnvGitHubComAndEnterprise(t *testing.T) {
	secrets, plain := HookEnv([]Account{
		{Host: "github.com", User: "me", Token: "gh"},
		{Host: "github.ibm.com", User: "dev", Token: "ghe"},
		{Host: "ghe.example.com", User: "dev", Token: "second"},
		{Host: "empty.example.com", User: "dev"},
	})
	require.Equal(t, []string{"GH_TOKEN=gh", "GH_ENTERPRISE_TOKEN=ghe"}, secrets)
	require.Empty(t, plain, "with several accounts gh keeps its own default host")
}

func TestHookEnvNoAccounts(t *testing.T) {
	secrets, plain := HookEnv(nil)
	require.Empty(t, secrets)
	require.Empty(t, plain)
}
