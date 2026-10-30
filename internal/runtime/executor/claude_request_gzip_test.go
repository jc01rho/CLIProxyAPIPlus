package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

type claudeGzipCapture struct {
	encoding string
	body     []byte
}

type claudeGzipTransport struct {
	mu       sync.Mutex
	sends    []claudeGzipCapture
	rejectGz bool
}

func (rt *claudeGzipTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	raw, _ := io.ReadAll(req.Body)
	encoding := req.Header.Get("Content-Encoding")
	rt.mu.Lock()
	rt.sends = append(rt.sends, claudeGzipCapture{encoding: encoding, body: raw})
	rt.mu.Unlock()
	if encoding == "gzip" && rt.rejectGz {
		return &http.Response{StatusCode: http.StatusUnsupportedMediaType, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("unsupported")), Request: req}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}, "Request-Id": {"req_gzip"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"msg_gzip","type":"message","model":"claude-opus-5","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`)),
		Request:    req,
	}, nil
}

func runClaudeGzipRequest(t *testing.T, gzipEnabled bool, transport *claudeGzipTransport, prompt string) {
	t.Helper()
	cfg := &config.Config{}
	cfg.Claude.RequestGzip = gzipEnabled
	auth := &cliproxyauth.Auth{ID: "gzip-oauth", Provider: "claude", Metadata: claudeOAuthTestMetadata(), Attributes: map[string]string{"api_key": "sk-ant-oat-gzip"}}
	payload := []byte(`{"model":"claude-opus-5","max_tokens":64,"messages":[{"role":"user","content":` + quoteJSON(prompt) + `}]}`)
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	_, err := NewClaudeExecutor(cfg).Execute(ctx, auth, cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}

func quoteJSON(text string) string {
	var buf bytes.Buffer
	buf.WriteByte('"')
	buf.WriteString(strings.ReplaceAll(text, `"`, `\"`))
	buf.WriteByte('"')
	return buf.String()
}

func gunzipClaudeBody(t *testing.T, body []byte) []byte {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	return out
}

func TestClaudeRequestGzip_OffByDefault(t *testing.T) {
	helps.ResetClaudeRequestGzipLatchForTest()
	transport := &claudeGzipTransport{}
	runClaudeGzipRequest(t, false, transport, strings.Repeat("a", 6000))
	if len(transport.sends) != 1 || transport.sends[0].encoding != "" {
		t.Fatalf("sends = %+v, want one identity send with gzip off", transport.sends)
	}
}

func TestClaudeRequestGzip_CompressesLargeOAuthBody(t *testing.T) {
	helps.ResetClaudeRequestGzipLatchForTest()
	transport := &claudeGzipTransport{}
	runClaudeGzipRequest(t, true, transport, strings.Repeat("a", 6000))
	if len(transport.sends) != 1 || transport.sends[0].encoding != "gzip" {
		t.Fatalf("sends = %d, encoding = %q, want one gzip send", len(transport.sends), transport.sends[0].encoding)
	}
	plain := gunzipClaudeBody(t, transport.sends[0].body)
	if body := bytes.TrimRight(plain, " \t\n"); !gjson.ValidBytes(body) {
		t.Fatalf("decompressed body is not JSON: %q", plain[:min(len(plain), 200)])
	}
	if billing := gjson.GetBytes(bytes.TrimRight(plain, " \t\n"), "system.0.text").String(); !strings.Contains(billing, "cch=00000;") {
		t.Fatalf("gzipped billing header = %q, want cch reset to 00000", billing)
	}
	if transport.sends[0].body[9] != 0xff {
		t.Fatalf("gzip OS byte = %#x, want 0xff", transport.sends[0].body[9])
	}
}

func TestClaudeRequestGzip_SmallBodyStaysIdentity(t *testing.T) {
	helps.ResetClaudeRequestGzipLatchForTest()
	transport := &claudeGzipTransport{}
	runClaudeGzipRequest(t, true, transport, "hi")
	if len(transport.sends) != 1 || transport.sends[0].encoding != "" {
		t.Fatalf("sends = %+v, want identity below 4096 characters", transport.sends)
	}
}

func TestClaudeRequestGzip_RejectedBodyIsResentUncompressedAndLatched(t *testing.T) {
	helps.ResetClaudeRequestGzipLatchForTest()
	t.Cleanup(helps.ResetClaudeRequestGzipLatchForTest)
	transport := &claudeGzipTransport{rejectGz: true}
	runClaudeGzipRequest(t, true, transport, strings.Repeat("b", 6000))
	if len(transport.sends) != 2 || transport.sends[0].encoding != "gzip" || transport.sends[1].encoding != "" {
		t.Fatalf("sends = %d, want gzip then identity resend", len(transport.sends))
	}
	resend := transport.sends[1].body
	if billing := gjson.GetBytes(resend, "system.0.text").String(); strings.Contains(billing, "cch=00000;") || !strings.Contains(billing, "cch=") {
		t.Fatalf("resend billing header = %q, want the signed identity body", billing)
	}
	if !helps.ClaudeRequestGzipLatched() {
		t.Fatal("gzip not latched off after a rejection")
	}
	runClaudeGzipRequest(t, true, transport, strings.Repeat("c", 6000))
	if last := transport.sends[len(transport.sends)-1]; last.encoding != "" {
		t.Fatalf("send after latch encoding = %q, want identity", last.encoding)
	}
}

func TestClaudeRequestGzip_CustomContentEncodingHeaderIsDropped(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages?beta=true", nil)
	auth := &cliproxyauth.Auth{ID: "ce", Attributes: map[string]string{"header:Content-Encoding": "gzip"}, Metadata: map[string]any{"access_token": "sk-ant-oat-ce"}}
	if err := applyClaudeHeaders(req, auth, "sk-ant-oat-ce", false, nil, []byte(`{"model":"claude-opus-5"}`), &config.Config{}, nil, false); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want dropped; only the send boundary may set it", got)
	}
}

func TestClassifyClaudeGzipRejection(t *testing.T) {
	anthropic := http.Header{"Request-Id": {"req_1"}}
	for _, tc := range []struct {
		name   string
		status int
		header http.Header
		body   string
		want   string
	}{
		{name: "proxy 415", status: 415, header: http.Header{}, want: helps.ClaudeGzipRejectionNonAnthropic},
		{name: "anthropic invalid json", status: 400, header: anthropic, body: `{"type":"error","error":{"type":"invalid_request_error","message":"The request body is not valid JSON"}}`, want: helps.ClaudeGzipRejectionInvalidJSON},
		{name: "ordinary api 400", status: 400, header: anthropic, body: `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: required"}}`},
		{name: "rate limit", status: 429, header: http.Header{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := helps.ClassifyClaudeGzipRejection(tc.status, tc.header, []byte(tc.body)); got != tc.want {
				t.Fatalf("ClassifyClaudeGzipRejection = %q, want %q", got, tc.want)
			}
		})
	}
}
