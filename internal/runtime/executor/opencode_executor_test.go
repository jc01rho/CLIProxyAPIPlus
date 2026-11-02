package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// decodeOpenCodeStamp returns the (milliseconds, counter) pair packed in an
// OpenCode identifier, undoing the bit inversion of descending ids.
func decodeOpenCodeStamp(t *testing.T, id string, descending bool) (uint64, uint64) {
	t.Helper()
	_, body, ok := strings.Cut(id, "_")
	if !ok || len(body) != 26 {
		t.Fatalf("identifier %q must be <prefix>_<26 characters>", id)
	}
	stamp, err := strconv.ParseUint(body[:12], 16, 64)
	if err != nil || strings.ToLower(body[:12]) != body[:12] {
		t.Fatalf("timestamp segment %q must be lowercase hex: %v", body[:12], err)
	}
	for _, char := range body[12:] {
		if !strings.ContainsRune(openCodeIDTailAlphabet, char) {
			t.Fatalf("tail %q contains %q outside the base62 alphabet", body[12:], char)
		}
	}
	if descending {
		stamp = ^stamp & 0xffffffffffff
	}
	return stamp >> 12, stamp & 0xfff
}

// openCodeStampDelta returns the absolute distance between a decoded stamp
// and a millisecond time, both taken modulo 2^36 (the segment keeps only the
// low 36 bits of the time).
func openCodeStampDelta(stamp, ms uint64) uint64 {
	const mask = 1<<36 - 1
	forward := (stamp - ms) & mask
	return min(forward, (ms-stamp)&mask)
}

func openCodeTestAuth(attrs map[string]string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:         "opencode-test",
		Provider:   "opencode",
		Attributes: attrs,
	}
}

func TestApplyOpenCodeHeaders_MirrorsCLIIdentity(t *testing.T) {
	tests := []struct {
		name       string
		apiKey     string
		wantBearer string
	}{
		{name: "configured key", apiKey: "oc_sk_example", wantBearer: "Bearer oc_sk_example"},
		{name: "anonymous key", apiKey: "", wantBearer: "Bearer public"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, "https://opencode.ai/zen/v1/chat/completions", nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}

			applyOpenCodeHeaders(req, tc.apiKey, true, "")

			if got := req.Header.Get("Authorization"); got != tc.wantBearer {
				t.Fatalf("Authorization = %q, want %q", got, tc.wantBearer)
			}
			if got := req.Header.Get("User-Agent"); got != "opencode/latest/"+openCodeVersion+"/cli" {
				t.Fatalf("User-Agent = %q, want the CLI's opencode/<channel>/<version>/<client> form", got)
			}
			if got := req.Header.Get("x-opencode-client"); got == "" {
				t.Fatal("x-opencode-client must be set")
			}
			if got := req.Header.Get("x-opencode-project"); got == "" {
				t.Fatal("x-opencode-project must be set")
			}
			session := req.Header.Get("x-opencode-session")
			if !strings.HasPrefix(session, "ses_") {
				t.Fatalf("x-opencode-session = %q, want an ses_ identifier", session)
			}
			// The free-tier gate rejects ascending session ids: the CLI creates
			// sessions with descending ids, so the decoded time must be now.
			if ms, _ := decodeOpenCodeStamp(t, session, true); openCodeStampDelta(ms, uint64(time.Now().UnixMilli())) > 5_000 {
				t.Fatalf("x-opencode-session %q is not a descending identifier for the current time", session)
			}
			for _, header := range []string{"x-opencode-session-id", "x-session-affinity", "X-Session-Id"} {
				if got := req.Header.Get(header); got != session {
					t.Fatalf("%s = %q, want the session id %q", header, got, session)
				}
			}
			if got := req.Header.Get("x-opencode-request"); !strings.HasPrefix(got, "msg_") {
				t.Fatalf("x-opencode-request = %q, want an msg_ identifier", got)
			}
			decodeOpenCodeStamp(t, req.Header.Get("x-opencode-request"), false)
			if got := req.Header.Get("Accept"); got != "text/event-stream" {
				t.Fatalf("Accept = %q, want text/event-stream for streaming calls", got)
			}
		})
	}
}

func TestNewOpenCodeIdentifier_MatchesSchemaShape(t *testing.T) {
	now := time.Now().UnixMilli()
	// The 48 bit segment packs (milliseconds << 12 | counter), so the high
	// 36 bits must track the current time modulo 2^36.
	want := ((uint64(now) << 12) & 0xffffffffffff) >> 12
	for _, descending := range []bool{false, true} {
		first := newOpenCodeIdentifier("ses", descending)
		second := newOpenCodeIdentifier("ses", descending)
		if first == second {
			t.Fatalf("identifiers must not repeat: %q", first)
		}
		for _, id := range []string{first, second} {
			ms, counter := decodeOpenCodeStamp(t, id, descending)
			if delta := int64(ms) - int64(want); delta < -5_000 || delta > 5_000 {
				t.Fatalf("%q (descending=%v) encodes %d, want a value near %d", id, descending, ms, want)
			}
			if counter == 0 {
				t.Fatalf("%q (descending=%v) must carry a counter starting at 1", id, descending)
			}
		}
	}
}

func TestOpenCodeExecutor_SystemOneAltUsesSystemOneRoute(t *testing.T) {
	var gotPath, gotModel, gotSession string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSession = r.Header.Get("x-opencode-session")
		body, _ := io.ReadAll(r.Body)
		gotModel = gjson.GetBytes(body, "model").String()
		if !gjson.GetBytes(body, "questions.q1").Exists() || gjson.GetBytes(body, "messages").Exists() {
			t.Errorf("System One payload must be forwarded without chat translation: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-1.13-free","answers":{"q1":{"type":"noul","noul":0.88}},"usage":{"input_tokens":276,"output_tokens":21}}`))
	}))
	defer server.Close()

	cfg := &config.Config{OpenCodeKey: []config.OpenCodeKey{{
		APIKey: "public",
		Models: []config.OpenCodeModel{{Name: "jev-1.13-free", Alias: "jev-latest"}},
	}}}
	auth := openCodeTestAuth(map[string]string{"api_key": "public", "base_url": server.URL})
	resp, err := NewOpenCodeExecutor(cfg).Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "jev-latest",
		Payload: []byte(`{"model":"jev-latest","state":"The sky is blue.","questions":{"q1":{"type":"noul","instructions":"Is it true?"}}}`),
	}, cliproxyexecutor.Options{Alt: "systemone"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotPath != "/systemone" {
		t.Fatalf("path = %q, want /systemone", gotPath)
	}
	if gotModel != "jev-1.13-free" {
		t.Fatalf("model = %q, want the configured upstream name jev-1.13-free", gotModel)
	}
	if !strings.HasPrefix(gotSession, "ses_") {
		t.Fatalf("x-opencode-session = %q, want the CLI identity headers", gotSession)
	}
	if got := gjson.GetBytes(resp.Payload, "answers.q1.noul").Float(); got != 0.88 {
		t.Fatalf("response must be returned verbatim, got %s", resp.Payload)
	}
}

func TestIsOpenCodeFreeModel(t *testing.T) {
	tests := map[string]bool{
		"big-pickle":                      true,
		"deepseek-v4-flash-free":          true,
		"mimo-v2.5-free":                  true,
		"nemotron-3-ultra-free":           true,
		"claude-sonnet-4-6":               false,
		"deepseek-v4.1-flash":             false,
		"free":                            false,
		"":                                false,
		"MIMO-V2.5-FREE":                  true,
		"muse-spark-1.2-contributor-free": true,
	}

	for model, want := range tests {
		if got := isOpenCodeFreeModel(model); got != want {
			t.Errorf("isOpenCodeFreeModel(%q) = %v, want %v", model, got, want)
		}
	}
}

func TestEnsureOpenCodeFreeTools_InjectsGateToolEnvelope(t *testing.T) {
	bashTool := `{"type":"function","function":{"name":"bash","parameters":{"type":"object"}}}`
	readTool := `{"type":"function","function":{"name":"read","parameters":{"type":"object"}}}`

	tests := []struct {
		name           string
		model          string
		payload        string
		wantBash       bool
		wantRead       bool
		wantUnmodified bool
	}{
		{
			name:     "free model without tools gains both tools",
			model:    "big-pickle",
			payload:  `{"model":"big-pickle","messages":[{"role":"user","content":"hi"}]}`,
			wantBash: true,
			wantRead: true,
		},
		{
			name:     "free model with unrelated tools gains both tools",
			model:    "mimo-v2.5-free",
			payload:  `{"model":"mimo-v2.5-free","tools":[{"type":"function","function":{"name":"grep"}}]}`,
			wantBash: true,
			wantRead: true,
		},
		{
			name:     "free model keeping existing bash only gains read",
			model:    "big-pickle",
			payload:  `{"model":"big-pickle","tools":[` + bashTool + `]}`,
			wantBash: true,
			wantRead: true,
		},
		{
			name:           "free model that already declares both tools is untouched",
			model:          "big-pickle",
			payload:        `{"model":"big-pickle","tools":[` + bashTool + `,` + readTool + `]}`,
			wantBash:       true,
			wantRead:       true,
			wantUnmodified: true,
		},
		{
			name:           "paid model is untouched",
			model:          "claude-sonnet-4-6",
			payload:        `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`,
			wantUnmodified: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ensureOpenCodeFreeTools([]byte(tc.payload), tc.model)

			if tc.wantUnmodified {
				if string(got) != tc.payload {
					t.Fatalf("payload was rewritten but should not be:\n got %s\nwant %s", got, tc.payload)
				}
				return
			}

			names := map[string]int{}
			tools := gjson.GetBytes(got, "tools")
			if !tools.IsArray() {
				t.Fatalf("tools missing from rewritten payload: %s", got)
			}
			tools.ForEach(func(_, value gjson.Result) bool {
				names[value.Get("function.name").String()]++
				return true
			})
			if tc.wantBash && names["bash"] == 0 {
				t.Fatalf("bash tool missing: %s", got)
			}
			if tc.wantRead && names["read"] == 0 {
				t.Fatalf("read tool missing: %s", got)
			}
			if names["bash"] > 1 || names["read"] > 1 {
				t.Fatalf("gate tools must not be duplicated: %s", got)
			}
		})
	}
}

func TestForceOpenCodeFreeStream(t *testing.T) {
	tests := []struct {
		name           string
		model          string
		payload        string
		wantStream     bool
		wantUnmodified bool
	}{
		{
			name:       "free model without stream flag is forced to stream",
			model:      "big-pickle",
			payload:    `{"model":"big-pickle","messages":[]}`,
			wantStream: true,
		},
		{
			name:       "free model with stream:false is forced to stream",
			model:      "mimo-v2.5-free",
			payload:    `{"model":"mimo-v2.5-free","stream":false}`,
			wantStream: true,
		},
		{
			name:           "free model already streaming is untouched",
			model:          "big-pickle",
			payload:        `{"model":"big-pickle","stream":true}`,
			wantStream:     true,
			wantUnmodified: true,
		},
		{
			name:           "paid model keeps buffered requests",
			model:          "claude-sonnet-4-6",
			payload:        `{"model":"claude-sonnet-4-6","stream":false}`,
			wantUnmodified: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := forceOpenCodeFreeStream([]byte(tc.payload), tc.model)
			if tc.wantUnmodified && string(got) != tc.payload {
				t.Fatalf("payload was rewritten but should not be:\n got %s\nwant %s", got, tc.payload)
			}
			if tc.wantStream && !gjson.GetBytes(got, "stream").Bool() {
				t.Fatalf("stream = %s, want true", gjson.GetBytes(got, "stream").Raw)
			}
		})
	}
}

func TestOpenCodeURLs_DefaultAndGoTier(t *testing.T) {
	if got := openCodeChatURL(openCodeTestAuth(nil)); got != "https://opencode.ai/zen/v1/chat/completions" {
		t.Fatalf("default chat URL = %q", got)
	}
	goTier := openCodeTestAuth(map[string]string{"api_key": "oc_go_key", "base_url": "https://opencode.ai/zen/go/v1/"})
	if got := openCodeChatURL(goTier); got != "https://opencode.ai/zen/go/v1/chat/completions" {
		t.Fatalf("go tier chat URL = %q", got)
	}
	if got := openCodeAPIKey(goTier); got != "oc_go_key" {
		t.Fatalf("api key = %q", got)
	}
}

func TestResolveOpenCodeModelName_MapsConfiguredAliases(t *testing.T) {
	cfg := &config.Config{
		OpenCodeKey: []config.OpenCodeKey{{
			APIKey: "public",
			Models: []config.OpenCodeModel{{Name: "big-pickle", Alias: "pickle"}},
		}},
	}
	auth := openCodeTestAuth(map[string]string{"api_key": "public"})

	if got := resolveOpenCodeModelName(cfg, auth, "pickle"); got != "big-pickle" {
		t.Fatalf("alias resolution = %q, want big-pickle", got)
	}
	if got := resolveOpenCodeModelName(cfg, auth, "big-pickle"); got != "big-pickle" {
		t.Fatalf("unknown alias should pass through, got %q", got)
	}
}

func TestFetchOpenCodeModels_MergesLiveCatalogAndHidesRegionalAliases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer public" {
			t.Errorf("Authorization = %q, want the anonymous credential", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[
			{"id":"big-pickle","object":"model"},
			{"id":"brand-new-free","object":"model"},
			{"id":"claude-sonnet-4-6:global","object":"model"}
		]}`))
	}))
	defer server.Close()

	auth := openCodeTestAuth(map[string]string{"api_key": "public", "base_url": server.URL})
	models := FetchOpenCodeModels(context.Background(), auth, &config.Config{})

	byID := map[string]*registry.ModelInfo{}
	for _, model := range models {
		byID[model.ID] = model
	}
	if _, ok := byID["brand-new-free"]; !ok {
		t.Fatalf("live catalog entry missing from merged models (%d entries)", len(models))
	}
	if _, ok := byID["claude-sonnet-4-6:global"]; ok {
		t.Fatal("regional :global alias must stay hidden")
	}
	if len(byID) < len(registry.GetOpenCodeModels()) {
		t.Fatalf("static fallback catalog must be preserved: got %d entries", len(byID))
	}
}
