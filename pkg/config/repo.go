package config

import (
	"os"
	"strings"

	"github.com/devsy-org/devsy/pkg/version"
)

const (
	RepoOwner         = "devsy-org"
	RepoName          = "devsy"
	RepoSlug          = RepoOwner + "/" + RepoName
	GitHubRepoURL     = "https://github.com/" + RepoSlug
	GitHubReleasesURL = GitHubRepoURL + "/releases"
	GitHubAPIUserURL  = "https://api.github.com/users/" + RepoOwner
	ProviderPrefix    = RepoName + "-provider-"

	// PlatformReleaseName is the Helm release / product name for Devsy Platform.
	PlatformReleaseName = RepoName + "-pro"

	// BinaryName is the CLI binary base name used in downloads and SSH host suffixes.
	BinaryName = RepoName

	// SSHHostSuffix is appended to workspace IDs for SSH config host entries.
	SSHHostSuffix = "." + BinaryName

	// WebsiteBaseURL is the project website used for asset URLs.
	WebsiteBaseURL = "https://" + RepoName + ".sh"

	// DownloadBaseURL is the object storage host serving release artifacts and image assets.
	DownloadBaseURL = "https://dl." + RepoName + ".sh"

	// WebsiteAssetsURL is the base URL for icon/image assets.
	WebsiteAssetsURL = DownloadBaseURL + "/assets"
)

// releaseRepo is the GitHub repository (owner/name) whose releases publish
// this build: the agent binaries and the CLI that self-update installs. It
// defaults to RepoSlug; builds from a fork set it with
//
//	-ldflags "-X github.com/devsy-org/devsy/pkg/config.releaseRepo=<owner>/<name>"
//
// so that they install their own agent and updates instead of upstream's.
var releaseRepo = RepoSlug

// ReleaseRepo returns the GitHub repository (owner/name) that publishes this
// build's releases. See releaseRepo.
func ReleaseRepo() string {
	if releaseRepo == "" {
		return RepoSlug
	}
	return releaseRepo
}

// ReleasesURL returns the GitHub releases page of ReleaseRepo.
func ReleasesURL() string {
	return "https://github.com/" + ReleaseRepo() + "/releases"
}

// AgentDownloadBaseURL returns the prefix under which versioned agent
// binaries are published.
func AgentDownloadBaseURL() string {
	return ReleasesURL() + "/download/"
}

// AgentLatestDownloadURL returns the URL of the floating "latest" agent
// release.
func AgentLatestDownloadURL() string {
	return ReleasesURL() + "/latest/download"
}

// DefaultAgentDownloadURL returns the URL the host should download the agent
// binary from. Honors the DEVSY_AGENT_URL override; otherwise uses the
// version-pinned release URL, falling back to "latest" in dev builds.
func DefaultAgentDownloadURL() string {
	if override := os.Getenv(EnvAgentURL); override != "" {
		return strings.TrimRight(override, "/")
	}
	if version.GetVersion() == version.DevVersion {
		return AgentLatestDownloadURL()
	}
	return AgentDownloadBaseURL() + version.GetVersion()
}
