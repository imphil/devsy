package ghcredentials

import "strings"

// HookEnv turns the host's GitHub CLI accounts into the environment GitHub
// CLI reads in lifecycle hooks, where the SSH-session wrapper is not
// available: GH_TOKEN for github.com and GH_ENTERPRISE_TOKEN for a GitHub
// Enterprise host (gh has one such variable, so the first enterprise account
// wins). secrets holds the token entries, which callers must mask in output.
// plain sets GH_HOST when there is exactly one account, so gh commands
// without --hostname (such as `gh api user`) use it as they do on the host.
func HookEnv(accounts []Account) (secrets, plain []string) {
	var hosts []string
	enterprise := ""
	for _, account := range accounts {
		if account.Host == "" || account.Token == "" {
			continue
		}
		hosts = append(hosts, account.Host)
		if strings.EqualFold(account.Host, "github.com") {
			secrets = append(secrets, "GH_TOKEN="+account.Token)
		} else if enterprise == "" {
			enterprise = account.Token
		}
	}
	if enterprise != "" {
		secrets = append(secrets, "GH_ENTERPRISE_TOKEN="+enterprise)
	}
	if len(hosts) == 1 {
		plain = append(plain, "GH_HOST="+hosts[0])
	}
	return secrets, plain
}
