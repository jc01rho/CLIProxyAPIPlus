package executor

import (
	"net/http"
	"strings"
	"testing"
)

func TestLooksLikeOpencodeZenBaseURL(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://opencode.ai/zen/v1", true},
		{"https://api.opencode.ai/v1", true},
		{"https://OPENCODE.AI/zen/v1", true},
		{"http://localhost:8317/v1", false},
		{"https://api.anthropic.com", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := looksLikeOpencodeZenBaseURL(tc.url); got != tc.want {
			t.Errorf("looksLikeOpencodeZenBaseURL(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

func TestProviderKeyLooksOpencode(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"openai-compatible-opencode-free", true},
		{"openai-compatible-opencode", true},
		{"opencode-free", true},
		{"openai-compatible-openrouter", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := providerKeyLooksOpencode(tc.key); got != tc.want {
			t.Errorf("providerKeyLooksOpencode(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}

func TestNewOpencodeRequestIDIsUUIDv4(t *testing.T) {
	id := newOpencodeRequestID()
	parts := strings.Split(id, "-")
	if len(parts) != 5 || len(parts[0]) != 8 || len(parts[1]) != 4 || len(parts[2]) != 4 || len(parts[3]) != 4 || len(parts[4]) != 12 {
		t.Fatalf("id shape = %q, want 8-4-4-4-12", id)
	}
	if !strings.HasPrefix(parts[2], "4") {
		t.Fatalf("version nibble = %q, want 4 (UUID v4)", parts[2])
	}
	switch parts[3][0] {
	case '8', '9', 'a', 'b':
	default:
		t.Fatalf("variant nibble = %q, want 8/9/a/b", parts[3][0])
	}
	if newOpencodeRequestID() == id {
		t.Fatal("request ids must be unique per call")
	}
}

func TestApplyOpencodeZenFingerprint(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://opencode.ai/zen/v1/responses", nil)
	req.Header.Set("User-Agent", "cli-proxy-openai-compat")
	applyOpencodeZenFingerprint(req, "openai-compatible-opencode-free")

	for _, h := range []string{"x-opencode-session", "x-opencode-request", "x-opencode-client"} {
		if req.Header.Get(h) == "" {
			t.Errorf("header %q missing after fingerprint", h)
		}
	}
	if got := req.Header.Get("User-Agent"); got != opencodeFingerprintUserAgent {
		t.Errorf("User-Agent = %q, want %q", got, opencodeFingerprintUserAgent)
	}
	if got := req.Header.Get("x-opencode-client"); got != "cli" {
		t.Errorf("x-opencode-client = %q, want cli", got)
	}

	// Non-Zen upstream stays untouched.
	req2, _ := http.NewRequest(http.MethodPost, "https://api.openrouter.ai/v1/chat/completions", nil)
	req2.Header.Set("User-Agent", "cli-proxy-openai-compat")
	applyOpencodeZenFingerprint(req2, "openai-compatible-openrouter")
	if req2.Header.Get("x-opencode-session") != "" {
		t.Error("fingerprint must not apply to non-opencode upstreams")
	}
	if got := req2.Header.Get("User-Agent"); got != "cli-proxy-openai-compat" {
		t.Errorf("non-zen UA changed: %q", got)
	}
}
