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
	applyOpencodeZenFingerprint(req, "openai-compatible-opencode-free", nil)

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
	applyOpencodeZenFingerprint(req2, "openai-compatible-openrouter", nil)
	if req2.Header.Get("x-opencode-session") != "" {
		t.Error("fingerprint must not apply to non-opencode upstreams")
	}
	if got := req2.Header.Get("User-Agent"); got != "cli-proxy-openai-compat" {
		t.Errorf("non-zen UA changed: %q", got)
	}
}

func TestOpencodeSessionFingerprintStableAcrossTurns(t *testing.T) {
	// PR 12719 pattern: consecutive agent turns of one conversation share a
	// session. Turn 2 appends a new user turn AFTER the first; the first user
	// message, model, system prompt and tools are unchanged, so the session id
	// must match.
	turn1 := []byte(`{"model":"muse-spark-1.3-contributor-free","system":"You are opencode.","messages":[{"role":"user","content":"fix the bug"}],"tools":[{"type":"function","function":{"name":"bash","parameters":{"type":"object"}}}]}`)
	turn2 := []byte(`{"model":"muse-spark-1.3-contributor-free","system":"You are opencode.","messages":[{"role":"user","content":"fix the bug"},{"role":"assistant","content":"done"},{"role":"user","content":"also add tests"}],"tools":[{"type":"function","function":{"name":"bash","parameters":{"type":"object"}}}]}`)
	s1 := opencodeSessionFingerprint(turn1)
	s2 := opencodeSessionFingerprint(turn2)
	if s1 == "" || s1 != s2 {
		t.Fatalf("session drift across turns: %q vs %q", s1, s2)
	}

	// A different conversation (different first user message) gets a different id.
	turnOther := []byte(`{"model":"muse-spark-1.3-contributor-free","system":"You are opencode.","messages":[{"role":"user","content":"write a poem"}],"tools":[]}`)
	if s3 := opencodeSessionFingerprint(turnOther); s3 == s1 {
		t.Fatal("different conversations must not share a session id")
	}

	// Empty body falls back to a random UUID v4.
	fallback := opencodeSessionFingerprint(nil)
	if fallback == "" {
		t.Fatal("empty body must still yield a session id")
	}
}

func TestApplyOpencodeZenFingerprintSessionStability(t *testing.T) {
	body := []byte(`{"model":"muse-spark-1.3-contributor-free","messages":[{"role":"user","content":"hi"}]}`)
	req1, _ := http.NewRequest(http.MethodPost, "https://opencode.ai/zen/v1/responses", nil)
	req2, _ := http.NewRequest(http.MethodPost, "https://opencode.ai/zen/v1/responses", nil)
	applyOpencodeZenFingerprint(req1, "openai-compatible-opencode-free", body)
	applyOpencodeZenFingerprint(req2, "openai-compatible-opencode-free", body)
	if req1.Header.Get("x-opencode-session") != req2.Header.Get("x-opencode-session") {
		t.Fatal("same conversation body must produce the same x-opencode-session")
	}
	if req1.Header.Get("x-opencode-request") == req2.Header.Get("x-opencode-request") {
		t.Fatal("x-opencode-request must be fresh per request")
	}

	// Explicit client session header wins and is forwarded untouched.
	req3, _ := http.NewRequest(http.MethodPost, "https://opencode.ai/zen/v1/responses", nil)
	req3.Header.Set("x-opencode-session", "client-session-keep-me")
	applyOpencodeZenFingerprint(req3, "openai-compatible-opencode-free", body)
	if got := req3.Header.Get("x-opencode-session"); got != "client-session-keep-me" {
		t.Fatalf("explicit session header rewritten: %q", got)
	}
}

func TestOpencodeSessionFingerprintShapes(t *testing.T) {
	base := func(model, system, user, tools string) []byte {
		var b strings.Builder
		b.WriteString(`{"model":"` + model + `"`)
		if system != "" {
			b.WriteString(`,"system":"` + system + `"`)
		}
		b.WriteString(`,"messages":[`)
		if user != "" {
			b.WriteString(`{"role":"user","content":` + user + `}`)
		}
		b.WriteString(`]`)
		if tools != "" {
			b.WriteString(`,"tools":` + tools)
		}
		b.WriteString(`}`)
		return []byte(b.String())
	}

	// content as array-of-blocks must hash equal to its plain-string twin.
	plain := base("muse-spark-1.3-contributor-free", "sys", `"hello"`, "")
	blocks := base("muse-spark-1.3-contributor-free", "sys", `[{"type":"text","text":"hel"},{"type":"text","text":"lo"}]`, "")
	if got := opencodeSessionFingerprint(plain); got != opencodeSessionFingerprint(blocks) {
		t.Fatalf("content-array shape diverged: %q vs %q", got, opencodeSessionFingerprint(blocks))
	}

	// system as role:"system" message equals top-level system string.
	topSystem := base("muse-spark-1.3-contributor-free", "sys prompt", `"hi"`, "")
	msgSystem := []byte(`{"model":"muse-spark-1.3-contributor-free","messages":[{"role":"system","content":"sys prompt"},{"role":"user","content":"hi"}]}`)
	if got := opencodeSessionFingerprint(topSystem); got != opencodeSessionFingerprint(msgSystem) {
		t.Fatalf("system dual-shape diverged: %q vs %q", got, opencodeSessionFingerprint(msgSystem))
	}

	// tools: function.name and name shapes hash the same set.
	toolsFn := base("m", "", `"hi"`, `[{"type":"function","function":{"name":"bash"}}]`)
	toolsName := base("m", "", `"hi"`, `[{"name":"bash"}]`)
	if got := opencodeSessionFingerprint(toolsFn); got != opencodeSessionFingerprint(toolsName) {
		t.Fatalf("tool name shapes diverged: %q vs %q", got, opencodeSessionFingerprint(toolsName))
	}

	// No user message (system+assistant only) must not panic and still yields an id.
	noUser := []byte(`{"model":"m","messages":[{"role":"system","content":"s"},{"role":"assistant","content":"a"}]}`)
	if got := opencodeSessionFingerprint(noUser); got == "" {
		t.Fatal("no-user-message body must still yield a session id")
	}

	// Same conversation prefix but different model must diverge.
	m1 := base("muse-a", "sys", `"hi"`, "")
	m2 := base("muse-b", "sys", `"hi"`, "")
	if got := opencodeSessionFingerprint(m1); got == opencodeSessionFingerprint(m2) {
		t.Fatal("different models must not share a session id")
	}
}
