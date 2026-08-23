package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func writeTestConfigFile(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if errWrite := os.WriteFile(path, []byte("{}\n"), 0o600); errWrite != nil {
		t.Fatalf("failed to write test config: %v", errWrite)
	}
	return path
}

func TestDeleteGeminiKey_RequiresBaseURLWhenAPIKeyDuplicated(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			GeminiKey: []config.GeminiKey{
				{APIKey: "shared-key", BaseURL: "https://a.example.com"},
				{APIKey: "shared-key", BaseURL: "https://b.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/gemini-api-key?api-key=shared-key", nil)

	h.DeleteGeminiKey(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if got := len(h.cfg.GeminiKey); got != 2 {
		t.Fatalf("gemini keys len = %d, want 2", got)
	}
}

func TestPutCommandCodeKeys_PreservesEmptyBaseURLForDefaultEndpoint(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg:            &config.Config{},
		configFilePath: writeTestConfigFile(t),
	}

	body := []byte(`[{
		"api-key": " user_default ",
		"base-url": " ",
		"models": [{"name": " upstream-model ", "alias": " alias-model "}]
	}]`)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/commandcode-api-key", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PutCommandCodeKeys(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.CommandCodeKey); got != 1 {
		t.Fatalf("commandcode keys len = %d, want 1", got)
	}
	entry := h.cfg.CommandCodeKey[0]
	if entry.APIKey != "user_default" {
		t.Fatalf("APIKey = %q, want %q", entry.APIKey, "user_default")
	}
	if entry.BaseURL != "" {
		t.Fatalf("BaseURL = %q, want empty default", entry.BaseURL)
	}
	if got := entry.Models[0].Name; got != "upstream-model" {
		t.Fatalf("Models[0].Name = %q, want %q", got, "upstream-model")
	}
	if got := entry.Models[0].Alias; got != "alias-model" {
		t.Fatalf("Models[0].Alias = %q, want %q", got, "alias-model")
	}
}

func TestPutCommandCodeKeysPreservesNestedOnlyProvider(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg:            &config.Config{},
		configFilePath: writeTestConfigFile(t),
	}
	body := []byte(`[{
		"base-url": " https://commandcode.example/v1 ",
		"api-key-entries": [
			{
				"api-key": " key-a ",
				"proxy-url": " socks5://proxy-a.example:1080 ",
				"weight": 3,
				"comment": " primary "
			},
			{"api-key": " key-b ", "weight": 1},
			{"api-key": " "}
		]
	}]`)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(
		http.MethodPut,
		"/v0/management/commandcode-api-key",
		bytes.NewReader(body),
	)
	c.Request.Header.Set("Content-Type", "application/json")

	h.PutCommandCodeKeys(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.CommandCodeKey); got != 1 {
		t.Fatalf("commandcode keys len = %d, want 1", got)
	}
	entry := h.cfg.CommandCodeKey[0]
	if entry.APIKey != "" {
		t.Fatalf("legacy APIKey = %q, want empty", entry.APIKey)
	}
	if got := len(entry.APIKeyEntries); got != 2 {
		t.Fatalf("nested API keys len = %d, want 2", got)
	}
	if got := entry.APIKeyEntries[0].APIKey; got != "key-a" {
		t.Fatalf("nested API key = %q, want key-a", got)
	}
	if got := entry.APIKeyEntries[0].ProxyURL; got != "socks5://proxy-a.example:1080" {
		t.Fatalf("nested proxy URL = %q", got)
	}
	if got := entry.APIKeyEntries[0].Comment; got != "primary" {
		t.Fatalf("nested comment = %q, want primary", got)
	}

	persisted, err := os.ReadFile(h.configFilePath)
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	persistedText := string(persisted)
	if !strings.Contains(persistedText, "api-key-entries:") ||
		!strings.Contains(persistedText, "api-key: key-a") ||
		!strings.Contains(persistedText, "api-key: key-b") {
		t.Fatalf("persisted config missing nested entries:\n%s", persistedText)
	}
}

func TestPutCommandCodeKeysFoldsDuplicateLegacyKeyWhenEntriesPresent(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg:            &config.Config{},
		configFilePath: writeTestConfigFile(t),
	}
	body := []byte(`[{"api-key":"user_A","comment":"rkjnice@gmail.com,github","priority":4,"base-url":"https://api.commandcode.ai","api-key-entries":[{"api-key":"user_A"},{"api-key":"user_B","comment":"a6420@yonsei"}]}]`)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/commandcode-api-key", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PutCommandCodeKeys(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	entry := h.cfg.CommandCodeKey[0]
	if entry.APIKey != "" {
		t.Fatalf("legacy APIKey = %q, want empty after fold", entry.APIKey)
	}
	if entry.Comment != "rkjnice@gmail.com,github" {
		t.Fatalf("provider Comment = %q, want preserved", entry.Comment)
	}
	if got := len(entry.APIKeyEntries); got != 2 {
		t.Fatalf("nested API keys len = %d, want 2", got)
	}
	if got := entry.APIKeyEntries[0].APIKey; got != "user_A" {
		t.Fatalf("nested[0] API key = %q, want user_A", got)
	}
	if got := entry.APIKeyEntries[1].APIKey; got != "user_B" {
		t.Fatalf("nested[1] API key = %q, want user_B", got)
	}
}

func TestCommandCodeKeysWithAuthIndexIncludesNestedEntries(t *testing.T) {
	t.Parallel()

	idGen := synthesizer.NewStableIDGenerator()
	firstID, _ := idGen.Next(
		"commandcode:apikey",
		"key-a",
		"https://commandcode.example/v1",
		"socks5://proxy-a.example:1080",
	)
	secondID, _ := idGen.Next(
		"commandcode:apikey",
		"key-b",
		"https://commandcode.example/v1",
		"",
	)
	manager := coreauth.NewManager(nil, nil, nil)
	firstAuth, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       firstID,
		Provider: "commandcode",
		Status:   coreauth.StatusActive,
		ProxyURL: "socks5://proxy-a.example:1080",
		Attributes: map[string]string{
			coreauth.AttributeAPIKey: "key-a",
			"base_url":               "https://commandcode.example/v1",
		},
	})
	if err != nil {
		t.Fatalf("register first auth: %v", err)
	}
	secondAuth, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       secondID,
		Provider: "commandcode",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			coreauth.AttributeAPIKey: "key-b",
			"base_url":               "https://commandcode.example/v1",
		},
	})
	if err != nil {
		t.Fatalf("register second auth: %v", err)
	}
	firstIndex := firstAuth.EnsureIndex()
	secondIndex := secondAuth.EnsureIndex()

	h := &Handler{
		cfg: &config.Config{
			CommandCodeKey: []config.CommandCodeKey{
				{
					BaseURL: "https://commandcode.example/v1",
					APIKeyEntries: []config.OpenAICompatibilityAPIKey{
						{
							APIKey:   "key-a",
							ProxyURL: "socks5://proxy-a.example:1080",
						},
						{APIKey: "key-b"},
					},
				},
			},
		},
		authManager: manager,
	}

	got := h.commandCodeKeysWithAuthIndex()
	if len(got) != 1 || len(got[0].APIKeyEntries) != 2 {
		t.Fatalf("nested response = %#v", got)
	}
	if got[0].APIKeyEntries[0].AuthIndex != firstIndex {
		t.Fatalf(
			"first auth index = %q, want %q",
			got[0].APIKeyEntries[0].AuthIndex,
			firstIndex,
		)
	}
	if got[0].APIKeyEntries[1].AuthIndex != secondIndex {
		t.Fatalf(
			"second auth index = %q, want %q",
			got[0].APIKeyEntries[1].AuthIndex,
			secondIndex,
		)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if !strings.Contains(string(encoded), firstIndex) ||
		!strings.Contains(string(encoded), secondIndex) {
		t.Fatalf("encoded response missing nested auth indexes: %s", encoded)
	}
}

func TestPatchFreebuffKeyPreservesUnspecifiedFields(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{FreebuffKey: []config.FreebuffKey{{
			APIKey:  "token",
			BaseURL: "https://www.codebuff.com",
			Models: []config.FreebuffModel{{
				Name: "deepseek/deepseek-v4-flash", Alias: "flash", AgentID: "base2-free-deepseek-flash",
			}},
		}}},
		configFilePath: writeTestConfigFile(t),
	}
	body := []byte(`{"index":0,"value":{"priority":7}}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/freebuff-api-key", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchFreebuffKey(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(h.cfg.FreebuffKey) != 1 {
		t.Fatalf("freebuff keys = %#v", h.cfg.FreebuffKey)
	}
	entry := h.cfg.FreebuffKey[0]
	if entry.APIKey != "token" || entry.Priority != 7 || len(entry.Models) != 1 {
		t.Fatalf("patched entry = %#v", entry)
	}
}

func TestFreebuffKeysWithAuthIndexIncludesNestedEntries(t *testing.T) {
	t.Parallel()

	idGen := synthesizer.NewStableIDGenerator()
	firstID, _ := idGen.Next("freebuff:apikey", "key-a", "https://www.codebuff.com", "socks5://proxy-a.example:1080")
	secondID, _ := idGen.Next("freebuff:apikey", "key-b", "https://www.codebuff.com", "")
	manager := coreauth.NewManager(nil, nil, nil)
	firstAuth, err := manager.Register(context.Background(), &coreauth.Auth{
		ID: firstID, Provider: "freebuff", Status: coreauth.StatusActive,
		ProxyURL:   "socks5://proxy-a.example:1080",
		Attributes: map[string]string{coreauth.AttributeAPIKey: "key-a", "base_url": "https://www.codebuff.com"},
	})
	if err != nil {
		t.Fatalf("register first auth: %v", err)
	}
	secondAuth, err := manager.Register(context.Background(), &coreauth.Auth{
		ID: secondID, Provider: "freebuff", Status: coreauth.StatusActive,
		Attributes: map[string]string{coreauth.AttributeAPIKey: "key-b", "base_url": "https://www.codebuff.com"},
	})
	if err != nil {
		t.Fatalf("register second auth: %v", err)
	}
	h := &Handler{
		cfg: &config.Config{FreebuffKey: []config.FreebuffKey{{
			BaseURL: "https://www.codebuff.com",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{
				{APIKey: "key-a", ProxyURL: "socks5://proxy-a.example:1080"},
				{APIKey: "key-b"},
			},
		}}},
		authManager: manager,
	}

	got := h.freebuffKeysWithAuthIndex()
	if len(got) != 1 || len(got[0].APIKeyEntries) != 2 {
		t.Fatalf("nested response = %#v", got)
	}
	if got[0].APIKeyEntries[0].AuthIndex != firstAuth.EnsureIndex() ||
		got[0].APIKeyEntries[1].AuthIndex != secondAuth.EnsureIndex() {
		t.Fatalf("nested auth indexes = %#v", got[0].APIKeyEntries)
	}
}

func TestDeleteFreebuffKeyRejectsMalformedIndex(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg:            &config.Config{FreebuffKey: []config.FreebuffKey{{APIKey: "must-remain"}}},
		configFilePath: writeTestConfigFile(t),
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/freebuff-api-key?index=invalid", nil)

	h.DeleteFreebuffKey(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if len(h.cfg.FreebuffKey) != 1 || h.cfg.FreebuffKey[0].APIKey != "must-remain" {
		t.Fatalf("malformed index changed config: %#v", h.cfg.FreebuffKey)
	}
}

func TestPutFreebuffKeysRejectsInvalidNestedWeight(t *testing.T) {
	t.Parallel()

	h := &Handler{cfg: &config.Config{}, configFilePath: writeTestConfigFile(t)}
	body := []byte(fmt.Sprintf(
		`[{"api-key-entries":[{"api-key":"token","weight":%d}]}]`,
		config.MaxCredentialWeight+1,
	))
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/freebuff-api-key", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PutFreebuffKeys(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if len(h.cfg.FreebuffKey) != 0 {
		t.Fatalf("invalid weight changed config: %#v", h.cfg.FreebuffKey)
	}
}

func TestPatchFreebuffKeyRejectsInvalidNestedWeight(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg:            &config.Config{FreebuffKey: []config.FreebuffKey{{APIKey: "must-remain"}}},
		configFilePath: writeTestConfigFile(t),
	}
	body := []byte(fmt.Sprintf(
		`{"index":0,"value":{"api-key-entries":[{"api-key":"replacement","weight":%d}]}}`,
		config.MaxCredentialWeight+1,
	))
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/freebuff-api-key", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchFreebuffKey(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	entry := h.cfg.FreebuffKey[0]
	if entry.APIKey != "must-remain" || len(entry.APIKeyEntries) != 0 {
		t.Fatalf("invalid weight changed entry: %#v", entry)
	}
}

func TestPatchFreebuffKeyRejectsAmbiguousCredentialMatch(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{FreebuffKey: []config.FreebuffKey{
			{APIKey: "duplicate", Priority: 1},
			{APIKey: "duplicate", Priority: 2},
		}},
		configFilePath: writeTestConfigFile(t),
	}
	body := []byte(`{"match":"duplicate","value":{"priority":9}}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/freebuff-api-key", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchFreebuffKey(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if h.cfg.FreebuffKey[0].Priority != 1 || h.cfg.FreebuffKey[1].Priority != 2 {
		t.Fatalf("ambiguous match changed config: %#v", h.cfg.FreebuffKey)
	}
}

func TestDeleteFreebuffKeyRejectsAmbiguousCredentialMatch(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{FreebuffKey: []config.FreebuffKey{
			{APIKey: "duplicate", BaseURL: "https://a.example"},
			{APIKey: "duplicate", BaseURL: "https://b.example"},
		}},
		configFilePath: writeTestConfigFile(t),
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/freebuff-api-key?api-key=duplicate", nil)

	h.DeleteFreebuffKey(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if len(h.cfg.FreebuffKey) != 2 {
		t.Fatalf("ambiguous match deleted config: %#v", h.cfg.FreebuffKey)
	}
}

func TestDeleteGeminiKey_DeletesOnlyMatchingBaseURL(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			GeminiKey: []config.GeminiKey{
				{APIKey: "shared-key", BaseURL: "https://a.example.com"},
				{APIKey: "shared-key", BaseURL: "https://b.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/gemini-api-key?api-key=shared-key&base-url=https://a.example.com", nil)

	h.DeleteGeminiKey(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.GeminiKey); got != 1 {
		t.Fatalf("gemini keys len = %d, want 1", got)
	}
	if got := h.cfg.GeminiKey[0].BaseURL; got != "https://b.example.com" {
		t.Fatalf("remaining base-url = %q, want %q", got, "https://b.example.com")
	}
}

func TestDeleteGeminiStyleKeyRejectsAmbiguousRoutingIdentity(t *testing.T) {
	tests := []struct {
		name         string
		interactions bool
	}{
		{name: "Gemini"},
		{name: "Interactions", interactions: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entries := []config.GeminiKey{
				{APIKey: "shared-key", BaseURL: "https://shared.example.com", Prefix: "team-a"},
				{APIKey: "shared-key", BaseURL: "https://shared.example.com", Prefix: "team-b"},
			}
			cfg := &config.Config{}
			path := "/v0/management/gemini-api-key?api-key=shared-key&base-url=https://shared.example.com"
			if tc.interactions {
				cfg.InteractionsKey = entries
				path = "/v0/management/interactions-api-key?api-key=shared-key&base-url=https://shared.example.com"
			} else {
				cfg.GeminiKey = entries
			}
			handler := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodDelete, path, nil)

			if tc.interactions {
				handler.DeleteInteractionsKey(ctx)
			} else {
				handler.DeleteGeminiKey(ctx)
			}

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
			remaining := cfg.GeminiKey
			if tc.interactions {
				remaining = cfg.InteractionsKey
			}
			if len(remaining) != 2 {
				t.Fatalf("remaining credential count = %d, want 2", len(remaining))
			}
		})
	}
}

func TestPatchGeminiStyleKeyRoutingIdentity(t *testing.T) {
	tests := []struct {
		name         string
		interactions bool
		firstBase    string
		wantStatus   int
	}{
		{name: "Gemini unique base URL", firstBase: "https://first.example.com", wantStatus: http.StatusOK},
		{name: "Gemini ambiguous base URL", firstBase: "https://shared.example.com", wantStatus: http.StatusBadRequest},
		{name: "Interactions unique base URL", interactions: true, firstBase: "https://first.example.com", wantStatus: http.StatusOK},
		{name: "Interactions ambiguous base URL", interactions: true, firstBase: "https://shared.example.com", wantStatus: http.StatusBadRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entries := []config.GeminiKey{
				{APIKey: "shared-key", BaseURL: tc.firstBase, Prefix: "team-a"},
				{APIKey: "shared-key", BaseURL: "https://shared.example.com", Prefix: "team-b"},
			}
			cfg := &config.Config{}
			path := "/v0/management/gemini-api-key?base-url=https://shared.example.com"
			if tc.interactions {
				cfg.InteractionsKey = entries
				path = "/v0/management/interactions-api-key?base-url=https://shared.example.com"
			} else {
				cfg.GeminiKey = entries
			}
			handler := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPatch, path, strings.NewReader(`{"match":"shared-key","value":{"prefix":"updated"}}`))

			if tc.interactions {
				handler.PatchInteractionsKey(ctx)
			} else {
				handler.PatchGeminiKey(ctx)
			}

			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, tc.wantStatus, recorder.Body.String())
			}
			remaining := cfg.GeminiKey
			if tc.interactions {
				remaining = cfg.InteractionsKey
			}
			if tc.wantStatus == http.StatusOK {
				if remaining[0].Prefix != "team-a" || remaining[1].Prefix != "updated" {
					t.Fatalf("prefixes = %q, %q; want team-a, updated", remaining[0].Prefix, remaining[1].Prefix)
				}
			} else if remaining[0].Prefix != "team-a" || remaining[1].Prefix != "team-b" {
				t.Fatalf("ambiguous patch changed prefixes to %q, %q", remaining[0].Prefix, remaining[1].Prefix)
			}
		})
	}
}

func TestDeleteClaudeKey_DeletesEmptyBaseURLWhenExplicitlyProvided(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			ClaudeKey: []config.ClaudeKey{
				{APIKey: "shared-key", BaseURL: ""},
				{APIKey: "shared-key", BaseURL: "https://claude.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/claude-api-key?api-key=shared-key&base-url=", nil)

	h.DeleteClaudeKey(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.ClaudeKey); got != 1 {
		t.Fatalf("claude keys len = %d, want 1", got)
	}
	if got := h.cfg.ClaudeKey[0].BaseURL; got != "https://claude.example.com" {
		t.Fatalf("remaining base-url = %q, want %q", got, "https://claude.example.com")
	}
}

func TestDeleteVertexCompatKey_DeletesOnlyMatchingBaseURL(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			VertexCompatAPIKey: []config.VertexCompatKey{
				{APIKey: "shared-key", BaseURL: "https://a.example.com"},
				{APIKey: "shared-key", BaseURL: "https://b.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/vertex-api-key?api-key=shared-key&base-url=https://b.example.com", nil)

	h.DeleteVertexCompatKey(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.VertexCompatAPIKey); got != 1 {
		t.Fatalf("vertex keys len = %d, want 1", got)
	}
	if got := h.cfg.VertexCompatAPIKey[0].BaseURL; got != "https://a.example.com" {
		t.Fatalf("remaining base-url = %q, want %q", got, "https://a.example.com")
	}
}

func TestDeleteXAIKey_RequiresBaseURLWhenAPIKeyDuplicated(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			XAIKey: []config.XAIKey{
				{APIKey: "shared-key", BaseURL: "https://a.example.com"},
				{APIKey: "shared-key", BaseURL: "https://b.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/xai-api-key?api-key=shared-key", nil)

	h.DeleteXAIKey(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if got := len(h.cfg.XAIKey); got != 2 {
		t.Fatalf("xAI keys len = %d, want 2", got)
	}
}

func TestDeleteCodexKey_RequiresBaseURLWhenAPIKeyDuplicated(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			CodexKey: []config.CodexKey{
				{APIKey: "shared-key", BaseURL: "https://a.example.com"},
				{APIKey: "shared-key", BaseURL: "https://b.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/codex-api-key?api-key=shared-key", nil)

	h.DeleteCodexKey(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if got := len(h.cfg.CodexKey); got != 2 {
		t.Fatalf("codex keys len = %d, want 2", got)
	}
}

type oauthAliasDeleteExecutor struct{}

func (e *oauthAliasDeleteExecutor) Identifier() string { return "claude" }

func (e *oauthAliasDeleteExecutor) Execute(_ context.Context, _ *coreauth.Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte(req.Model), Headers: make(http.Header)}, nil
}

func (e *oauthAliasDeleteExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &coreauth.Error{HTTPStatus: http.StatusNotImplemented, Message: "ExecuteStream not implemented"}
}

func (e *oauthAliasDeleteExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *oauthAliasDeleteExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &coreauth.Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *oauthAliasDeleteExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, &coreauth.Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func TestDeleteOAuthModelAlias_SyncsAuthManager(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&oauthAliasDeleteExecutor{})
	manager.SetOAuthModelAlias(map[string][]config.OAuthModelAlias{
		"claude": {{Name: "claude-haiku-4-5-20251001", Alias: "haiku-cc", Fork: true}},
	})

	authEntry := &coreauth.Auth{
		ID:       "claude-oauth-delete-test",
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{
			"email": "claude@example.com",
		},
	}
	registered, err := manager.Register(context.Background(), authEntry)
	if err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(registered.ID, registered.Provider, []*registry.ModelInfo{{ID: "claude-haiku-4-5-20251001"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(registered.ID)
	})
	manager.RefreshSchedulerEntry(registered.ID)

	respBefore, err := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "haiku-cc"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute before delete: %v", err)
	}
	if got := string(respBefore.Payload); got != "claude-haiku-4-5-20251001" {
		t.Fatalf("payload before delete = %q, want %q", got, "claude-haiku-4-5-20251001")
	}

	h := &Handler{
		cfg: &config.Config{
			OAuthModelAlias: map[string][]config.OAuthModelAlias{
				"claude": {{Name: "claude-haiku-4-5-20251001", Alias: "haiku-cc", Fork: true}},
			},
		},
		configFilePath: writeTestConfigFile(t),
		authManager:    manager,
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/oauth-model-alias?channel=claude", nil)

	h.DeleteOAuthModelAlias(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if aliases, ok := h.cfg.OAuthModelAlias["claude"]; !ok || aliases != nil {
		t.Fatalf("cfg oauth alias after delete = %#v, want explicit nil marker", h.cfg.OAuthModelAlias["claude"])
	}

	_, err = manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "haiku-cc"}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatalf("execute after delete unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "auth_not_found") {
		t.Fatalf("execute after delete error = %v, want auth_not_found", err)
	}
}
