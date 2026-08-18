package copilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

func TestEnableModelPolicyRequestShape(t *testing.T) {
	var (
		gotPath    string
		gotMethod  string
		gotHeaders http.Header
		gotBody    string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotHeaders = r.Header.Clone()
		buf := new(strings.Builder)
		_, _ = jsonCopy(buf, r)
		gotBody = buf.String()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	auth := &CopilotAuth{httpClient: newTestClient(srv)}
	token := &CopilotAPIToken{Token: "api-token"}
	if err := auth.EnableModelPolicy(context.Background(), token, "claude-sonnet-4.5"); err != nil {
		t.Fatalf("EnableModelPolicy error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/models/claude-sonnet-4.5/policy" {
		t.Errorf("path = %q", gotPath)
	}
	if got := gotHeaders.Get("Authorization"); got != "Bearer api-token" {
		t.Errorf("Authorization = %q", got)
	}
	if got := gotHeaders.Get("Openai-Intent"); got != "chat-policy" {
		t.Errorf("Openai-Intent = %q", got)
	}
	if got := gotHeaders.Get("X-Interaction-Type"); got != "chat-policy" {
		t.Errorf("X-Interaction-Type = %q", got)
	}
	if got := gotHeaders.Get("User-Agent"); got != copilotUserAgent {
		t.Errorf("User-Agent = %q", got)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatalf("body not JSON: %v (%q)", err, gotBody)
	}
	if body["state"] != "enabled" {
		t.Errorf("body state = %q, want enabled", body["state"])
	}
}

func TestEnableModelPolicyFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"policy managed by organization"}`))
	}))
	defer srv.Close()

	auth := &CopilotAuth{httpClient: newTestClient(srv)}
	err := auth.EnableModelPolicy(context.Background(), &CopilotAPIToken{Token: "t"}, "grok-4.5")
	if err == nil {
		t.Fatal("expected error for non-2xx response")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error should carry status: %v", err)
	}
}

func TestEnableAllModelPoliciesBestEffort(t *testing.T) {
	var (
		mu      sync.Mutex
		enabled []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/models/"), "/policy")
		mu.Lock()
		enabled = append(enabled, id)
		mu.Unlock()
		if id == "locked-model" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	auth := &CopilotAuth{httpClient: newTestClient(srv)}
	count := auth.EnableAllModelPolicies(context.Background(), &CopilotAPIToken{Token: "t"},
		[]string{"claude-sonnet-4.5", "locked-model", "grok-4.5", "", "  "})
	if count != 2 {
		t.Fatalf("enabled count = %d, want 2", count)
	}
	sort.Strings(enabled)
	if len(enabled) != 3 || enabled[0] != "claude-sonnet-4.5" || enabled[1] != "grok-4.5" || enabled[2] != "locked-model" {
		t.Fatalf("attempted models = %v", enabled)
	}
}

func jsonCopy(buf *strings.Builder, r *http.Request) (string, error) {
	dec := json.NewDecoder(r.Body)
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", err
	}
	out, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	buf.Write(out)
	return string(out), nil
}
