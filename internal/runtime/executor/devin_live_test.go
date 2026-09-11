package executor

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// TestDevinLiveEndToEnd hits the real Devin API with the locally logged-in
// credential. It only runs when DEVIN_LIVE_TEST=1 so CI stays offline.
func TestDevinLiveEndToEnd(t *testing.T) {
	if os.Getenv("DEVIN_LIVE_TEST") != "1" {
		t.Skip("set DEVIN_LIVE_TEST=1 to run against the live Devin API")
	}
	raw, err := os.ReadFile(os.Getenv("HOME") + "/.local/share/devin/credentials.toml")
	if err != nil {
		t.Fatalf("read credentials: %v", err)
	}
	m := regexp.MustCompile(`(?m)^windsurf_api_key\s*=\s*"([^"]+)"`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("no windsurf_api_key in credentials")
	}
	token := strings.TrimSpace(string(m[1]))

	auth := &cliproxyauth.Auth{
		ID:       "devin-live",
		Provider: "devin",
		Attributes: map[string]string{
			"api_key": token,
		},
	}
	ex := NewDevinExecutor(nil)
	resp, err := ex.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "swe-1-6-fast",
		Payload: []byte(`{"model":"swe-1-6-fast","messages":[` +
			`{"role":"system","content":"You are a session title generator. Given the user's first message, produce a short, descriptive title."},` +
			`{"role":"user","content":"say hi to me"}]}`),
	}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("live Execute failed: %v", err)
	}
	t.Logf("live payload: %s", resp.Payload)
}
