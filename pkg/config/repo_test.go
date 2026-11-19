package config

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
)

type RepoTestSuite struct {
	suite.Suite
	originalEnv string
}

func (s *RepoTestSuite) SetupTest() {
	s.originalEnv = os.Getenv(EnvAgentURL)
}

func (s *RepoTestSuite) TearDownTest() {
	if s.originalEnv != "" {
		_ = os.Setenv(EnvAgentURL, s.originalEnv)
	} else {
		_ = os.Unsetenv(EnvAgentURL)
	}
}

func (s *RepoTestSuite) TestDefaultAgentDownloadURL_NoTrailingSlash() {
	_ = os.Setenv(EnvAgentURL, "https://example.com/releases/latest/download")
	s.Equal("https://example.com/releases/latest/download", DefaultAgentDownloadURL())
}

func (s *RepoTestSuite) TestDefaultAgentDownloadURL_SingleTrailingSlash() {
	_ = os.Setenv(EnvAgentURL, "https://example.com/releases/latest/download/")
	s.Equal("https://example.com/releases/latest/download", DefaultAgentDownloadURL())
}

func (s *RepoTestSuite) TestDefaultAgentDownloadURL_MultipleTrailingSlashes() {
	_ = os.Setenv(EnvAgentURL, "https://example.com/releases/latest/download///")
	s.Equal("https://example.com/releases/latest/download", DefaultAgentDownloadURL())
}

func (s *RepoTestSuite) TestReleaseRepo_DefaultsToUpstream() {
	s.Equal(RepoSlug, ReleaseRepo())
	s.Equal(GitHubReleasesURL, ReleasesURL())
}

func (s *RepoTestSuite) TestReleaseRepo_Override() {
	s.T().Cleanup(func() { releaseRepo = RepoSlug })
	_ = os.Unsetenv(EnvAgentURL)

	releaseRepo = "someone/devsy"
	s.Equal("someone/devsy", ReleaseRepo())
	s.Equal("https://github.com/someone/devsy/releases/download/", AgentDownloadBaseURL())
	s.Equal("https://github.com/someone/devsy/releases/latest/download", AgentLatestDownloadURL())
	s.True(
		strings.HasPrefix(DefaultAgentDownloadURL(), "https://github.com/someone/devsy/releases/"),
	)

	releaseRepo = ""
	s.Equal(RepoSlug, ReleaseRepo(), "an empty build-time value keeps upstream")
}

func TestRepoSuite(t *testing.T) {
	suite.Run(t, new(RepoTestSuite))
}
