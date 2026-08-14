package config

import "testing"

func TestDefaultPanelGitHubRepositoryUsesFork(t *testing.T) {
	const want = "https://github.com/jc01rho/Cli-Proxy-API-Management-Center"
	if DefaultPanelGitHubRepository != want {
		t.Fatalf("DefaultPanelGitHubRepository = %q, want %q", DefaultPanelGitHubRepository, want)
	}
}
