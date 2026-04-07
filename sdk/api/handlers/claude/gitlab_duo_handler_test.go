package claude

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	"github.com/tidwall/gjson"
	"github.com/tiktoken-go/tokenizer"
)

func addClaudeTestSegment(segments *[]string, value string) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return
	}
	*segments = append(*segments, trimmed)
}

func collectClaudeTestContentSegments(content gjson.Result, segments *[]string) {
	if !content.Exists() {
		return
	}
	if content.Type == gjson.String {
		addClaudeTestSegment(segments, content.String())
		return
	}
	if content.IsArray() {
		content.ForEach(func(_, part gjson.Result) bool {
			partType := part.Get("type").String()
			switch partType {
			case "text":
				addClaudeTestSegment(segments, part.Get("text").String())
			default:
				if part.Type == gjson.JSON {
					addClaudeTestSegment(segments, part.Raw)
				} else {
					addClaudeTestSegment(segments, part.String())
				}
			}
			return true
		})
		return
	}
	if content.Type == gjson.JSON {
		addClaudeTestSegment(segments, content.Raw)
	}
}

func estimateClaudeInputTokensForTest(enc tokenizer.Codec, payload []byte) (int, error) {
	if enc == nil || len(payload) == 0 {
		return 0, nil
	}
	root := gjson.ParseBytes(payload)
	segments := make([]string, 0, 32)
	collectClaudeTestContentSegments(root.Get("system"), &segments)
	if messages := root.Get("messages"); messages.Exists() && messages.IsArray() {
		messages.ForEach(func(_, msg gjson.Result) bool {
			addClaudeTestSegment(&segments, msg.Get("role").String())
			collectClaudeTestContentSegments(msg.Get("content"), &segments)
			return true
		})
	}
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		tools.ForEach(func(_, tool gjson.Result) bool {
			addClaudeTestSegment(&segments, tool.Get("name").String())
			addClaudeTestSegment(&segments, tool.Get("description").String())
			if schema := tool.Get("input_schema"); schema.Exists() {
				addClaudeTestSegment(&segments, schema.Raw)
			}
			return true
		})
	}
	joined := strings.TrimSpace(strings.Join(segments, "\n"))
	if joined == "" {
		return 0, nil
	}
	return enc.Count(joined)
}

func TestClaudeMessagesWithGitLabDuoAnthropicGateway(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotPath, gotAuthHeader, gotRealmHeader string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuthHeader = r.Header.Get("Authorization")
		gotRealmHeader = r.Header.Get("X-Gitlab-Realm")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"cmd":"ls"}}],"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":11,"output_tokens":4}}`))
	}))
	defer upstream.Close()

	manager, _ := registerGitLabDuoAnthropicAuth(t, upstream.URL)
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewClaudeCodeAPIHandler(base)
	router := gin.New()
	router.POST("/v1/messages", h.ClaudeMessages)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"model":"claude-sonnet-4-5",
		"max_tokens":128,
		"messages":[{"role":"user","content":"list files"}],
		"tools":[{"name":"Bash","description":"run bash","input_schema":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if gotPath != "/v1/proxy/anthropic/v1/messages" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/proxy/anthropic/v1/messages")
	}
	if gotAuthHeader != "Bearer gateway-token" {
		t.Fatalf("authorization = %q, want Bearer gateway-token", gotAuthHeader)
	}
	if gotRealmHeader != "saas" {
		t.Fatalf("x-gitlab-realm = %q, want saas", gotRealmHeader)
	}
	if !strings.Contains(resp.Body.String(), `"tool_use"`) {
		t.Fatalf("expected tool_use response, got %s", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"Bash"`) {
		t.Fatalf("expected Bash tool in response, got %s", resp.Body.String())
	}
}

func TestClaudeMessagesStreamWithGitLabDuoAnthropicGateway(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-5\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_start\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello from duo\"}}\n\n"))
		_, _ = w.Write([]byte("event: message_delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"input_tokens\":10,\"output_tokens\":3}}\n\n"))
		_, _ = w.Write([]byte("event: message_stop\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer upstream.Close()

	manager, _ := registerGitLabDuoAnthropicAuth(t, upstream.URL)
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewClaudeCodeAPIHandler(base)
	router := gin.New()
	router.POST("/v1/messages", h.ClaudeMessages)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"model":"claude-sonnet-4-5",
		"stream":true,
		"max_tokens":64,
		"messages":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if gotPath != "/v1/proxy/anthropic/v1/messages" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/proxy/anthropic/v1/messages")
	}
	if got := resp.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", got)
	}
	if !strings.Contains(resp.Body.String(), "event: content_block_delta") {
		t.Fatalf("expected streamed claude event, got %s", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "hello from duo") {
		t.Fatalf("expected streamed text, got %s", resp.Body.String())
	}
}

func TestClaudeMessages_ThresholdRoutingSelectsCredentialByEstimatedTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)

	type upstreamRecord struct {
		path string
		body []byte
	}
	type billingDecisionSnapshot struct {
		billingClass string
		reason       string
	}
	type credentialUnderTest struct {
		idSuffix      string
		provider      string
		attributes    map[string]string
		metadata      map[string]any
		registerModel string
	}

	newClaudeUpstream := func(t *testing.T, responseModel string) (*httptest.Server, <-chan upstreamRecord) {
		t.Helper()
		records := make(chan upstreamRecord, 2)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			records <- upstreamRecord{path: r.URL.Path, body: body}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"id":"msg_1","type":"message","role":"assistant","model":%q,"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":11,"output_tokens":4}}`, responseModel)))
		}))
		return server, records
	}

	buildClaudePayloadWithEstimatedTokens := func(t *testing.T, model string, target int) string {
		t.Helper()
		enc, err := tokenizer.Get(tokenizer.Cl100kBase)
		if err != nil {
			t.Fatalf("tokenizer.Get() error: %v", err)
		}

		makePayload := func(text string) []byte {
			return []byte(fmt.Sprintf(`{"model":%q,"max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":%q}]}]}`, model, text))
		}

		prefix := "user\n"
		prefixIDs, _, err := enc.Encode(prefix)
		if err != nil {
			t.Fatalf("encode prefix error: %v", err)
		}
		if len(prefixIDs) >= target {
			t.Fatalf("prefix token count %d already exceeds target %d", len(prefixIDs), target)
		}

		candidatePieces := []string{" a", " b", " c", " x", " y", " z", ".", ",", "!", "?", " hello", " test"}
		oneTokenIDs := make([]uint, 0, len(candidatePieces))
		for _, piece := range candidatePieces {
			ids, _, errEncode := enc.Encode(piece)
			if errEncode != nil {
				t.Fatalf("encode candidate %q error: %v", piece, errEncode)
			}
			if len(ids) == 1 {
				oneTokenIDs = append(oneTokenIDs, ids[0])
			}
		}
		if len(oneTokenIDs) == 0 {
			t.Fatal("failed to find single-token candidate pieces")
		}

		suffixTokenCount := target - len(prefixIDs)
		for _, tokenID := range oneTokenIDs {
			fullIDs := make([]uint, 0, target)
			fullIDs = append(fullIDs, prefixIDs...)
			for i := 0; i < suffixTokenCount; i++ {
				fullIDs = append(fullIDs, tokenID)
			}
			joined, errDecode := enc.Decode(fullIDs)
			if errDecode != nil {
				continue
			}
			if !strings.HasPrefix(joined, prefix) {
				continue
			}
			payload := makePayload(strings.TrimPrefix(joined, prefix))
			count, errCount := estimateClaudeInputTokensForTest(enc, payload)
			if errCount != nil {
				t.Fatalf("estimateClaudeInputTokensForTest(decoded payload) error: %v", errCount)
			}
			if count == target {
				return string(payload)
			}
		}

		t.Fatalf("failed to build Claude payload with exact estimated token count %d", target)
		return ""
	}

	tests := []struct {
		name              string
		requestModel      string
		targetTokens      int
		wantBillingClass  string
		wantSelectedModel string
		wantAuthKind      string
	}{
		{
			name:              "1499 토큰이면 ai provider config credential 선택",
			requestModel:      "claude-opus-4-6",
			targetTokens:      1499,
			wantBillingClass:  "metered",
			wantSelectedModel: "claude-opus-4-6",
			wantAuthKind:      "config",
		},
		{
			name:              "1499 토큰 alias 모델이면 ai provider config credential 선택",
			requestModel:      "opus-4.6",
			targetTokens:      1499,
			wantBillingClass:  "metered",
			wantSelectedModel: "claude-opus-4-6",
			wantAuthKind:      "config",
		},
		{
			name:              "1502 토큰이면 oauth credential 선택",
			requestModel:      "opus-4.6",
			targetTokens:      1502,
			wantBillingClass:  "per-request",
			wantSelectedModel: "opus-4.6",
			wantAuthKind:      "oauth",
		},
		{
			name:              "1502 토큰 실제 모델이면 oauth credential 선택",
			requestModel:      "claude-opus-4-6",
			targetTokens:      1502,
			wantBillingClass:  "per-request",
			wantSelectedModel: "claude-opus-4-6",
			wantAuthKind:      "oauth",
		},
	}

	estimatedTokensPattern := regexp.MustCompile(`estimated_tokens=(\d+)`)
	routeModel := "claude-opus-4-6"

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			configUpstream, configRecords := newClaudeUpstream(t, routeModel)
			defer configUpstream.Close()
			oauthUpstream, oauthRecords := newClaudeUpstream(t, routeModel)
			defer oauthUpstream.Close()

			manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
			manager.SetRetryConfig(0, 0, 0)
			manager.SetConfig(&internalconfig.Config{
				ClaudeKey: []internalconfig.ClaudeKey{{
					APIKey:       "config-key",
					BaseURL:      configUpstream.URL,
					BillingClass: internalconfig.BillingClassMetered,
					Models: []internalconfig.ClaudeModel{{
						Name:  routeModel,
						Alias: "opus-4.6",
					}},
				}},
				Routing: internalconfig.RoutingConfig{
					TokenThresholdRules: []internalconfig.TokenThresholdRule{
						{ModelPattern: "*opus*", MaxTokens: 1500, BillingClass: internalconfig.BillingClassMetered, Enabled: true},
						{ModelPattern: "*opus*", MinTokens: 1501, BillingClass: internalconfig.BillingClassPerRequest, Enabled: true},
					},
				},
				OAuthModelAlias: map[string][]internalconfig.OAuthModelAlias{
					"claude": {{Name: "opus-4.6", Alias: "claude-opus-4-6", Fork: true}},
				},
			})
			manager.RegisterExecutor(runtimeexecutor.NewClaudeExecutor(&internalconfig.Config{}))

			baseID := uuid.NewString()
			configID := baseID + "-config-auth"
			oauthID := baseID + "-oauth-auth"
			credentials := []credentialUnderTest{
				{
					idSuffix:      configID,
					provider:      "claude",
					registerModel: routeModel,
					attributes: map[string]string{
						"api_key":       "config-key",
						"base_url":      configUpstream.URL,
						"billing_class": "metered",
						"priority":      "100",
						"auth_kind":     "apikey",
					},
				},
				{
					idSuffix:      oauthID,
					provider:      "claude",
					registerModel: routeModel,
					attributes: map[string]string{
						"api_key":       "oauth-key",
						"base_url":      oauthUpstream.URL,
						"billing_class": "per-request",
						"priority":      "1",
						"auth_kind":     "oauth",
					},
					metadata: map[string]any{
						"auth_method": "oauth",
						"email":       "oauth-test@example.com",
					},
				},
			}

			for _, cred := range credentials {
				auth := &coreauth.Auth{
					ID:         cred.idSuffix,
					Provider:   cred.provider,
					Status:     coreauth.StatusActive,
					Attributes: cred.attributes,
					Metadata:   cred.metadata,
				}
				registered, err := manager.Register(context.Background(), auth)
				if err != nil {
					t.Fatalf("register auth %s: %v", auth.ID, err)
				}
				models := []*registry.ModelInfo{{ID: cred.registerModel}}
				if cred.attributes["auth_kind"] == "apikey" {
					models = append(models, &registry.ModelInfo{ID: "opus-4.6"})
				}
				if cred.attributes["auth_kind"] == "oauth" {
					models = append(models, &registry.ModelInfo{ID: "opus-4.6"})
				}
				registry.GetGlobalRegistry().RegisterClient(registered.ID, registered.Provider, models)
				authID := registered.ID
				t.Cleanup(func() {
					registry.GetGlobalRegistry().UnregisterClient(authID)
				})
			}

			base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
			h := NewClaudeCodeAPIHandler(base)
			router := gin.New()

			var decision billingDecisionSnapshot
			router.Use(func(c *gin.Context) {
				c.Next()
				rawDecision, exists := c.Get("billingClassDecision")
				if !exists {
					return
				}
				if mapped, ok := rawDecision.(map[string]string); ok {
					decision = billingDecisionSnapshot{billingClass: mapped["billing_class"], reason: mapped["reason"]}
				}
			})
			router.POST("/v1/messages", h.ClaudeMessages)

			body := buildClaudePayloadWithEstimatedTokens(t, tc.requestModel, tc.targetTokens)
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Anthropic-Version", "2023-06-01")
			resp := httptest.NewRecorder()

			router.ServeHTTP(resp, req)

			if resp.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d body=%s", resp.Code, http.StatusOK, resp.Body.String())
			}

			select {
			case got := <-func() <-chan upstreamRecord {
				if tc.wantBillingClass == "metered" {
					return configRecords
				}
				return oauthRecords
			}():
				if got.path != "/v1/messages" {
					t.Fatalf("upstream path = %q, want %q", got.path, "/v1/messages")
				}
				if gotModel := gjson.GetBytes(got.body, "model").String(); gotModel != tc.wantSelectedModel {
					t.Fatalf("upstream model = %q, want %q", gotModel, tc.wantSelectedModel)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("expected upstream call for billing class %s", tc.wantBillingClass)
			}

			select {
			case unexpected := <-func() <-chan upstreamRecord {
				if tc.wantBillingClass == "metered" {
					return oauthRecords
				}
				return configRecords
			}():
				t.Fatalf("unexpected upstream call to other credential: path=%s body=%s", unexpected.path, string(unexpected.body))
			default:
			}

			if decision.billingClass == "" {
				t.Fatalf("expected billingClassDecision to be captured")
			}
			if got := decision.billingClass; got != tc.wantBillingClass {
				t.Fatalf("billing_class = %q, want %q", got, tc.wantBillingClass)
			}
			for _, want := range []string{
				"threshold_rule",
				"target=" + tc.wantBillingClass,
				"selected_billing_class=" + tc.wantBillingClass,
				"auth=" + map[bool]string{true: oauthID, false: configID}[tc.wantBillingClass == "per-request"],
			} {
				if !strings.Contains(decision.reason, want) {
					t.Fatalf("reason = %q, want substring %q", decision.reason, want)
				}
			}

			match := estimatedTokensPattern.FindStringSubmatch(decision.reason)
			if len(match) != 2 {
				t.Fatalf("reason = %q, expected estimated_tokens entry", decision.reason)
			}
			count := 0
			_, err := fmt.Sscanf(match[1], "%d", &count)
			if err != nil {
				t.Fatalf("parse estimated_tokens from %q: %v", match[1], err)
			}
			if count != tc.targetTokens {
				t.Fatalf("estimated_tokens = %d, want %d", count, tc.targetTokens)
			}

			selectedAuthID := map[bool]string{true: oauthID, false: configID}[tc.wantBillingClass == "per-request"]
			if tc.wantAuthKind == "oauth" && !strings.Contains(decision.reason, "auth="+selectedAuthID) {
				t.Fatalf("reason = %q, expected oauth auth id %q", decision.reason, selectedAuthID)
			}
		})
	}
}

func registerGitLabDuoAnthropicAuth(t *testing.T, upstreamURL string) (*coreauth.Manager, string) {
	t.Helper()

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(runtimeexecutor.NewGitLabExecutor(&internalconfig.Config{}))

	auth := &coreauth.Auth{
		ID:       "gitlab-duo-claude-handler-test",
		Provider: "gitlab",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"duo_gateway_base_url": upstreamURL,
			"duo_gateway_token":    "gateway-token",
			"duo_gateway_headers":  map[string]string{"X-Gitlab-Realm": "saas"},
			"model_provider":       "anthropic",
			"model_name":           "claude-sonnet-4-5",
		},
	}
	registered, err := manager.Register(context.Background(), auth)
	if err != nil {
		t.Fatalf("register auth: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(registered.ID, registered.Provider, runtimeexecutor.GitLabModelsFromAuth(registered))
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(registered.ID)
	})
	return manager, registered.ID
}
