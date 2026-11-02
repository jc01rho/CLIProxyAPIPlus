package executor

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestOpenCodeAffinitySession_StablePerConversation(t *testing.T) {
	meta := map[string]any{cliproxyexecutor.CanonicalSessionIDMetadataKey: "ctx:v1:conversation-a"}
	first := openCodeAffinitySession(context.Background(), meta, nil)
	second := openCodeAffinitySession(context.Background(), nil, meta)
	if first == "" {
		t.Fatal("a canonical session identity must produce an affinity session")
	}
	if first != second {
		t.Fatalf("the same conversation must keep one session id: %q vs %q", first, second)
	}
	if !strings.HasPrefix(first, "ses_") {
		t.Fatalf("session %q must use the CLI ses_ shape", first)
	}
	decodeOpenCodeStamp(t, first, true)
	if strings.Contains(first, "conversation-a") {
		t.Fatalf("session %q must not leak the raw identity", first)
	}

	other := openCodeAffinitySession(context.Background(), map[string]any{cliproxyexecutor.CanonicalSessionIDMetadataKey: "ctx:v1:conversation-b"})
	if other == first {
		t.Fatal("different conversations must not share a session id")
	}
}

func TestOpenCodeAffinitySession_SkipsLCPRoutingIdentity(t *testing.T) {
	lcpOnly := map[string]any{
		cliproxyexecutor.LCPAffinitySessionIDMetadataKey: "lcp:v1:routing-only",
		cliproxyexecutor.CanonicalSessionIDMetadataKey:   "lcp:v1:routing-only",
	}
	if got := openCodeAffinitySession(context.Background(), lcpOnly); got != "" {
		t.Fatalf("LCP routing identity must not become a provider session: %q", got)
	}

	ctx := util.WithSessionID(context.Background(), "codex:fallback-session")
	if got := openCodeAffinitySession(ctx, lcpOnly); got != opencodeHashSession("opencode-session:codex:fallback-session") {
		t.Fatalf("context session must be used when metadata carries only the LCP id: %q", got)
	}
}

func TestOpenCodeAffinitySession_PrefersCanonicalIdentity(t *testing.T) {
	meta := map[string]any{
		cliproxyexecutor.CanonicalSessionIDMetadataKey: "canonical",
		cliproxyexecutor.DerivedSessionIDMetadataKey:   "derived",
	}
	if got := openCodeAffinitySession(context.Background(), meta); got != opencodeHashSession("opencode-session:canonical") {
		t.Fatalf("canonical identity must win over derived: %q", got)
	}
}

func TestApplyOpenCodeHeaders_UsesAffinitySession(t *testing.T) {
	session := opencodeHashSession("opencode-session:ctx:v1:conversation-a")
	req, err := http.NewRequest(http.MethodPost, "https://opencode.ai/zen/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	applyOpenCodeHeaders(req, "oc_sk_example", true, session)

	for _, header := range []string{"x-opencode-session", "x-opencode-session-id", "x-session-affinity", "X-Session-Id"} {
		if got := req.Header.Get(header); got != session {
			t.Fatalf("%s = %q, want the affinity session %q", header, got, session)
		}
	}
	if got := req.Header.Get("x-opencode-request"); got == session {
		t.Fatalf("x-opencode-request must stay per-request, got %q", got)
	}
}

func TestOpencodeSessionValuePrefersAffinitySession(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`)
	headers := http.Header{"X-Session-Id": {"client-session"}}
	affinity := opencodeHashSession("opencode-session:ctx:v1:conversation-a")

	if got := opencodeSessionValue(headers, body, "seed", affinity); got != affinity {
		t.Fatalf("affinity session must win over client headers and body: %q", got)
	}
	fallback := opencodeSessionValue(headers, body, "seed", "")
	if fallback == affinity {
		t.Fatal("without an affinity session the client identity must be used")
	}
	if fallback != opencodeHashSession("client-session:client-session") {
		t.Fatalf("client header fallback = %q", fallback)
	}
}
