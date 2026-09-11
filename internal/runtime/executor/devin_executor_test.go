package executor

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func devinTestAuth() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       "devin-test-1",
		Provider: "devin",
		Attributes: map[string]string{
			"api_key": "test-token-abc",
		},
	}
}

func devinTestFrame(text string) []byte {
	var msg []byte
	msg = append(msg, devinEncodeField(nil, 9, 2, devinEncodeString(text))...)
	msg = append(msg, devinEncodeField(nil, 17, 2, devinEncodeString("turn-uuid-1"))...)
	return msg
}

func devinEnvelope(payload []byte) []byte {
	out := []byte{0x00}
	var ln [4]byte
	binary.BigEndian.PutUint32(ln[:], uint32(len(payload)))
	out = append(out, ln[:]...)
	return append(out, payload...)
}

func TestDevinAuthHeader(t *testing.T) {
	got := devinAuthHeader("tok123")
	if got != "Basic tok123-tok123" {
		t.Fatalf("auth header = %q, want %q", got, "Basic tok123-tok123")
	}
}

func TestDevinDecodeFrames(t *testing.T) {
	var body []byte
	body = append(body, devinEnvelope(devinTestFrame("hello "))...)
	body = append(body, devinEnvelope(devinTestFrame("world"))...)
	trailer := []byte(`{"error":{"code":"ok"}}`)
	body = append(body, 0x02)
	var ln [4]byte
	binary.BigEndian.PutUint32(ln[:], uint32(len(trailer)))
	body = append(body, ln[:]...)
	body = append(body, trailer...)

	data, tr, err := devinDecodeFrames(body)
	if err != nil {
		t.Fatalf("decode frames: %v", err)
	}
	if len(data) != 2 {
		t.Fatalf("data frames = %d, want 2", len(data))
	}
	if string(tr) != string(trailer) {
		t.Fatalf("trailer = %q", tr)
	}
}

func TestDevinDecodeFramesTruncated(t *testing.T) {
	_, _, err := devinDecodeFrames([]byte{0x00, 0x00, 0x00, 0x00, 0x05, 0x01})
	if err == nil {
		t.Fatal("expected truncation error")
	}
}

func TestDevinExtractTextDelta(t *testing.T) {
	text, ok := devinExtractTextDelta(devinTestFrame("hello"))
	if !ok || text != "hello" {
		t.Fatalf("extract = %q,%v, want hello,true", text, ok)
	}
	// Frame without f9 must not yield text.
	var meta []byte
	meta = append(meta, devinEncodeField(nil, 17, 2, devinEncodeString("turn-uuid-1"))...)
	if _, ok := devinExtractTextDelta(meta); ok {
		t.Fatal("metadata-only frame must not yield text")
	}
}

func TestDevinBuildChatRequest(t *testing.T) {
	body := devinBuildChatRequest("tok", "swe-1-6-fast", "sys prompt", "say hi", "sess-uuid-1", "")
	if len(body) < 6 || body[0] != 0x00 {
		t.Fatalf("missing connect envelope prefix: %x", body[:minDevinTestLen(body)])
	}
	ln := int(binary.BigEndian.Uint32(body[1:5]))
	if ln != len(body)-5 {
		t.Fatalf("envelope length %d != body %d", ln, len(body)-5)
	}
	inner := body[5:]
	if !strings.Contains(string(inner), "swe-1-6-fast") {
		t.Fatal("model UID missing from request")
	}
	if !strings.Contains(string(inner), "say hi") {
		t.Fatal("user text missing from request")
	}
	if !strings.Contains(string(inner), "sys prompt") {
		t.Fatal("system prompt missing from request")
	}
	if !strings.Contains(string(inner), "sess-uuid-1") {
		t.Fatal("session UUID missing from request")
	}
	if !strings.Contains(string(inner), "devin-cli") {
		t.Fatal("client context missing from request")
	}
}

func minDevinTestLen(b []byte) int {
	if len(b) < 5 {
		return len(b)
	}
	return 5
}

func TestDevinExecuteAgainstLocalServer(t *testing.T) {
	var gotAuth, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("authorization")
		gotCT = r.Header.Get("content-type")
		var body []byte
		body = append(body, devinEnvelope(devinTestFrame("hi "))...)
		body = append(body, devinEnvelope(devinTestFrame("there"))...)
		w.Header().Set("content-type", "application/connect+proto")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	auth := devinTestAuth()
	auth.Attributes["base_url"] = srv.URL
	ex := &DevinExecutor{client: srv.Client()}
	resp, err := ex.Execute(context.Background(), auth,
		cliproxyexecutor.Request{
			Model:   "swe-1-6-fast",
			Payload: []byte(`{"model":"swe-1-6-fast","messages":[{"role":"user","content":"say hi"}]}`),
		}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotAuth != "Basic test-token-abc-test-token-abc" {
		t.Fatalf("auth header = %q", gotAuth)
	}
	if gotCT != "application/connect+proto" {
		t.Fatalf("content-type = %q", gotCT)
	}
	if !strings.Contains(string(resp.Payload), "hi there") {
		t.Fatalf("payload missing concatenated text: %s", resp.Payload)
	}
	if !strings.Contains(string(resp.Payload), "chat.completion") {
		t.Fatalf("payload not OpenAI-shaped: %s", resp.Payload)
	}
}

func TestDevinExecuteStreamAgainstLocalServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != devinGetChatMessagePath {
			http.NotFound(w, r)
			return
		}
		var body []byte
		body = append(body, devinEnvelope(devinTestFrame("stream "))...)
		body = append(body, devinEnvelope(devinTestFrame("ok"))...)
		w.Header().Set("content-type", "application/connect+proto")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	auth := devinTestAuth()
	auth.Attributes["base_url"] = srv.URL
	ex := &DevinExecutor{client: srv.Client()}
	res, err := ex.ExecuteStream(context.Background(), auth,
		cliproxyexecutor.Request{
			Model:   "swe-1-6-fast",
			Payload: []byte(`{"model":"swe-1-6-fast","messages":[{"role":"user","content":"hi"}]}`),
		}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	var sb strings.Builder
	sawDone := false
	for chunk := range res.Chunks {
		if chunk.Err != nil {
			t.Fatalf("chunk err: %v", chunk.Err)
		}
		s := string(chunk.Payload)
		if strings.Contains(s, "[DONE]") {
			sawDone = true
			continue
		}
		// Extract content deltas crudely.
		if i := strings.Index(s, `"content":`); i >= 0 {
			rest := s[i+len(`"content":`):]
			if len(rest) > 0 && rest[0] == '"' {
				end := strings.Index(rest[1:], `"`)
				if end >= 0 {
					sb.WriteString(rest[1 : 1+end])
				}
			}
		}
	}
	if !sawDone {
		t.Fatal("stream missing [DONE] terminator")
	}
	if sb.String() != "stream ok" {
		t.Fatalf("streamed text = %q, want %q", sb.String(), "stream ok")
	}
}

func TestDevinExecuteMissingKey(t *testing.T) {
	ex := &DevinExecutor{}
	_, err := ex.Execute(context.Background(), &cliproxyauth.Auth{Provider: "devin"},
		cliproxyexecutor.Request{Model: "swe-1-6-fast"}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("expected missing-key error")
	}
}

func TestDevinAPIKeyResolution(t *testing.T) {
	cases := []struct {
		name string
		auth *cliproxyauth.Auth
		want string
	}{
		{
			name: "from attributes api_key",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "k1"}},
			want: "k1",
		},
		{
			name: "from attributes session_token",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"session_token": "s1"}},
			want: "s1",
		},
		{
			name: "from attributes windsurf_api_key",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"windsurf_api_key": "w1"}},
			want: "w1",
		},
		{
			name: "from metadata api_key",
			auth: &cliproxyauth.Auth{Metadata: map[string]any{"api_key": "m_k1"}},
			want: "m_k1",
		},
		{
			name: "from metadata session_token",
			auth: &cliproxyauth.Auth{Metadata: map[string]any{"session_token": "m_s1"}},
			want: "m_s1",
		},
		{
			name: "from metadata windsurf_api_key",
			auth: &cliproxyauth.Auth{Metadata: map[string]any{"windsurf_api_key": "m_w1"}},
			want: "m_w1",
		},
		{
			name: "from metadata token",
			auth: &cliproxyauth.Auth{Metadata: map[string]any{"token": "m_t1"}},
			want: "m_t1",
		},
		{
			name: "from metadata access_token",
			auth: &cliproxyauth.Auth{Metadata: map[string]any{"access_token": "m_a1"}},
			want: "m_a1",
		},
		{
			name: "nil auth",
			auth: nil,
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := devinAPIKey(tc.auth)
			if got != tc.want {
				t.Fatalf("devinAPIKey() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDevinServerURLResolution(t *testing.T) {
	cases := []struct {
		name string
		auth *cliproxyauth.Auth
		want string
	}{
		{
			name: "default",
			auth: &cliproxyauth.Auth{},
			want: devinDefaultAPIServerURL,
		},
		{
			name: "from attributes base_url",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://custom.server.com/"}},
			want: "https://custom.server.com",
		},
		{
			name: "from metadata api_server_url",
			auth: &cliproxyauth.Auth{Metadata: map[string]any{"api_server_url": "https://meta.server.com"}},
			want: "https://meta.server.com",
		},
		{
			name: "from metadata base_url",
			auth: &cliproxyauth.Auth{Metadata: map[string]any{"base_url": "https://meta-base.server.com/"}},
			want: "https://meta-base.server.com",
		},
		{
			name: "schemeless custom URL gets https",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "custom.codeium.com"}},
			want: "https://custom.codeium.com",
		},
		{
			name: "app.devin.ai without scheme falls back to default connect server",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "app.devin.ai"}},
			want: devinDefaultAPIServerURL,
		},
		{
			name: "https://app.devin.ai falls back to default connect server",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://app.devin.ai"}},
			want: devinDefaultAPIServerURL,
		},
		{
			name: "https://api.devin.ai falls back to default connect server",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://api.devin.ai"}},
			want: devinDefaultAPIServerURL,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := devinServerURL(tc.auth)
			if got != tc.want {
				t.Fatalf("devinServerURL() = %q, want %q", got, tc.want)
			}
		})
	}
}
