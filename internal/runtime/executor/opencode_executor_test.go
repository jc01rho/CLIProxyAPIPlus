package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

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

			applyOpenCodeHeaders(req, tc.apiKey, true)

			if got := req.Header.Get("Authorization"); got != tc.wantBearer {
				t.Fatalf("Authorization = %q, want %q", got, tc.wantBearer)
			}
			if got := req.Header.Get("User-Agent"); !strings.HasPrefix(got, "opencode/") {
				t.Fatalf("User-Agent = %q, want an opencode/<version> prefix (the free-tier gate matches it)", got)
			}
			if got := req.Header.Get("x-opencode-client"); got == "" {
				t.Fatal("x-opencode-client must be set")
			}
			if got := req.Header.Get("x-opencode-project"); got == "" {
				t.Fatal("x-opencode-project must be set")
			}
			for _, header := range []string{"x-opencode-session", "x-opencode-request"} {
				value := req.Header.Get(header)
				if !strings.HasPrefix(value, "ses_") && !strings.HasPrefix(value, "msg_") {
					t.Fatalf("%s = %q, want an ses_/msg_ prefixed identifier", header, value)
				}
				if len(value) != 30 {
					t.Fatalf("%s = %q, want prefix plus 26 ULID characters", header, value)
				}
			}
			if got := req.Header.Get("Accept"); got != "text/event-stream" {
				t.Fatalf("Accept = %q, want text/event-stream for streaming calls", got)
			}
		})
	}
}

func TestNewOpenCodeIdentifier_MatchesSchemaShape(t *testing.T) {
	now := time.Now().UnixMilli()
	first := newOpenCodeIdentifier("ses")
	second := newOpenCodeIdentifier("ses")
	if first == second {
		t.Fatalf("identifiers must not repeat: %q", first)
	}

	for _, id := range []string{first, second} {
		body := strings.TrimPrefix(id, "ses_")
		if len(body) != 26 {
			t.Fatalf("identifier body = %q, want 26 characters", body)
		}
		for _, char := range body[:12] {
			if !strings.ContainsRune("0123456789abcdef", char) {
				t.Fatalf("timestamp segment %q must be lowercase hex (got %q)", body[:12], char)
			}
		}
		for _, char := range body[12:] {
			if !strings.ContainsRune(openCodeIDTailAlphabet, char) {
				t.Fatalf("tail %q contains %q outside the base62 alphabet", body[12:], char)
			}
		}
		stamp, err := strconv.ParseUint(body[:12], 16, 64)
		if err != nil {
			t.Fatalf("timestamp segment %q must parse as hex: %v", body[:12], err)
		}
		// The 48 bit segment packs (milliseconds << 12 | counter), so the high
		// 36 bits must track the current time modulo 2^36.
		masked := (uint64(now) << 12) & 0xffffffffffff
		if delta := int64(stamp>>12) - int64(masked>>12); delta < -5_000 || delta > 5_000 {
			t.Fatalf("timestamp segment encodes %d, want a value near %d", stamp>>12, masked>>12)
		}
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
