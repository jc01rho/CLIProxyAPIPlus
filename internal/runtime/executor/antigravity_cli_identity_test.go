package executor

import (
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAntigravityRuntimeUserAgentDefaultsToAgyCLIIdentity(t *testing.T) {
	for name, got := range map[string]string{
		"content request": resolveUserAgent(nil),
		"loadCodeAssist":  resolveLoadCodeAssistUserAgent(nil),
	} {
		if !strings.HasPrefix(got, "antigravity/cli/1.5.8 (aidev_client; os_type=") || !strings.HasSuffix(got, "; cl=1020628950; auth_method=consumer)") {
			t.Fatalf("%s User-Agent = %q, want the agy CLI identity", name, got)
		}
	}
}

func TestAntigravityRuntimeUserAgentHonorsPerAuthValue(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"user_agent": "operator-ua/1.0"}}
	if got := resolveUserAgent(auth); got != "operator-ua/1.0" {
		t.Fatalf("resolveUserAgent() = %q, want the configured value", got)
	}
}
