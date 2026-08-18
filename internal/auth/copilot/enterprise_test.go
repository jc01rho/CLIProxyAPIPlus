package copilot

import (
	"testing"
)

func TestNormalizeEnterpriseDomain(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "empty", input: "", want: ""},
		{name: "whitespace", input: "   ", want: ""},
		{name: "bare domain", input: "company.ghe.com", want: "company.ghe.com"},
		{name: "https url", input: "https://company.ghe.com", want: "company.ghe.com"},
		{name: "url with path", input: "https://company.ghe.com/some/path", want: "company.ghe.com"},
		{name: "with surrounding spaces", input: "  company.ghe.com  ", want: "company.ghe.com"},
		{name: "invalid", input: "://not-a-url", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeEnterpriseDomain(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for input %q", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeEnterpriseDomain(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestDomainURLBuilders(t *testing.T) {
	if got := DeviceCodeURLForDomain(""); got != "https://github.com/login/device/code" {
		t.Errorf("default device code URL = %q", got)
	}
	if got := AccessTokenURLForDomain(""); got != "https://github.com/login/oauth/access_token" {
		t.Errorf("default access token URL = %q", got)
	}
	if got := UserInfoURLForDomain(""); got != "https://api.github.com/user" {
		t.Errorf("default userinfo URL = %q", got)
	}
	if got := CopilotAPITokenURLForDomain(""); got != "https://api.github.com/copilot_internal/v2/token" {
		t.Errorf("default copilot token URL = %q", got)
	}
	if got := DeviceCodeURLForDomain("company.ghe.com"); got != "https://company.ghe.com/login/device/code" {
		t.Errorf("enterprise device code URL = %q", got)
	}
	if got := AccessTokenURLForDomain("company.ghe.com"); got != "https://company.ghe.com/login/oauth/access_token" {
		t.Errorf("enterprise access token URL = %q", got)
	}
	if got := UserInfoURLForDomain("company.ghe.com"); got != "https://api.company.ghe.com/user" {
		t.Errorf("enterprise userinfo URL = %q", got)
	}
	if got := CopilotAPITokenURLForDomain("company.ghe.com"); got != "https://api.company.ghe.com/copilot_internal/v2/token" {
		t.Errorf("enterprise copilot token URL = %q", got)
	}
}

func TestFallbackAPIEndpointForDomain(t *testing.T) {
	if got := FallbackAPIEndpointForDomain(""); got != copilotAPIEndpoint {
		t.Errorf("default fallback = %q, want %q", got, copilotAPIEndpoint)
	}
	if got := FallbackAPIEndpointForDomain("github.com"); got != copilotAPIEndpoint {
		t.Errorf("github.com fallback = %q, want %q", got, copilotAPIEndpoint)
	}
	if got := FallbackAPIEndpointForDomain("company.ghe.com"); got != "https://copilot-api.company.ghe.com" {
		t.Errorf("enterprise fallback = %q", got)
	}
}

func TestIsAllowedCopilotHostForDomain(t *testing.T) {
	// Static allowlist applies regardless of domain.
	if !isAllowedCopilotHostForDomain("api.individual.githubcopilot.com", "") {
		t.Error("static allowlist host rejected")
	}
	// github.com credentials reject arbitrary hosts.
	if isAllowedCopilotHostForDomain("evil.example.com", "") {
		t.Error("untrusted host accepted for github.com")
	}
	if isAllowedCopilotHostForDomain("api.company.ghe.com", "github.com") {
		t.Error("enterprise host accepted for github.com credential")
	}
	// Enterprise credentials trust their derived hosts.
	for _, host := range []string{"api.company.ghe.com", "copilot-api.company.ghe.com", "proxy.company.ghe.com"} {
		if !isAllowedCopilotHostForDomain(host, "company.ghe.com") {
			t.Errorf("enterprise-derived host %q rejected", host)
		}
	}
	if isAllowedCopilotHostForDomain("api.evil.com", "company.ghe.com") {
		t.Error("foreign host accepted for enterprise credential")
	}
}

func TestResolveAPIBaseURL(t *testing.T) {
	auth := &CopilotAuth{}

	// Trusted endpoint from token response wins.
	token := &CopilotAPIToken{Token: "t"}
	token.Endpoints.API = "https://api.individual.githubcopilot.com/"
	if got := auth.ResolveAPIBaseURL(token); got != "https://api.individual.githubcopilot.com" {
		t.Errorf("ResolveAPIBaseURL = %q", got)
	}

	// Untrusted endpoint falls back to the default.
	bad := &CopilotAPIToken{Token: "t"}
	bad.Endpoints.API = "https://evil.example.com"
	if got := auth.ResolveAPIBaseURL(bad); got != copilotAPIEndpoint {
		t.Errorf("untrusted ResolveAPIBaseURL = %q", got)
	}

	// Enterprise credentials trust their own hosts and fall back accordingly.
	ent := &CopilotAuth{domain: "company.ghe.com"}
	entToken := &CopilotAPIToken{Token: "t"}
	entToken.Endpoints.API = "https://api.company.ghe.com"
	if got := ent.ResolveAPIBaseURL(entToken); got != "https://api.company.ghe.com" {
		t.Errorf("enterprise ResolveAPIBaseURL = %q", got)
	}
	if got := ent.ResolveAPIBaseURL(&CopilotAPIToken{Token: "t"}); got != "https://copilot-api.company.ghe.com" {
		t.Errorf("enterprise fallback ResolveAPIBaseURL = %q", got)
	}
}

func TestDeviceFlowClientDomainEndpoints(t *testing.T) {
	client := NewDeviceFlowClientForDomain(nil, "company.ghe.com")
	if got := client.deviceCodeEndpoint(); got != "https://company.ghe.com/login/device/code" {
		t.Errorf("deviceCodeEndpoint = %q", got)
	}
	if got := client.tokenEndpoint(); got != "https://company.ghe.com/login/oauth/access_token" {
		t.Errorf("tokenEndpoint = %q", got)
	}
	if got := client.userinfoEndpoint(); got != "https://api.company.ghe.com/user" {
		t.Errorf("userinfoEndpoint = %q", got)
	}

	def := NewDeviceFlowClient(nil)
	if got := def.deviceCodeEndpoint(); got != copilotDeviceCodeURL {
		t.Errorf("default deviceCodeEndpoint = %q", got)
	}
}
