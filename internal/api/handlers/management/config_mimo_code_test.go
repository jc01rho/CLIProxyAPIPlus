package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestPutMiMoCodeKeysNormalizesAndFiltersEmptyClientID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{cfg: &config.Config{}, configFilePath: writeTestConfigFile(t)}
	r := setupTestRouter(h)
	r.PUT("/mimo-code-api-key", h.PutMiMoCodeKeys)

	body := []byte(`[
		{"client-id":"   ","base-url":"https://discard.example.com"},
		{"client-id":" client-1 ","base-url":" https://mimo.example.com ","proxy-url":" http://proxy.example.com ","prefix":" mimo ","headers":{" X-Test ":" value ","Empty":"   "},"models":[{"name":" model-a ","alias":" alias-a "},{"name":"   ","alias":"   "}]}
	]`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/mimo-code-api-key", bytes.NewReader(body))
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.MiMoCodeKey); got != 1 {
		t.Fatalf("mimo-code keys len = %d, want 1", got)
	}
	entry := h.cfg.MiMoCodeKey[0]
	if entry.ClientID != "client-1" || entry.BaseURL != "https://mimo.example.com" || entry.ProxyURL != "http://proxy.example.com" || entry.Prefix != "mimo" {
		t.Fatalf("entry was not normalized: %#v", entry)
	}
	if got := entry.Headers["X-Test"]; got != "value" {
		t.Fatalf("header X-Test = %q, want %q", got, "value")
	}
	if _, ok := entry.Headers["Empty"]; ok {
		t.Fatalf("empty header was not removed: %#v", entry.Headers)
	}
	if len(entry.Models) != 1 || entry.Models[0].Name != "model-a" || entry.Models[0].Alias != "alias-a" {
		t.Fatalf("models were not normalized: %#v", entry.Models)
	}
}

func TestPatchMiMoCodeKeyMatchesClientID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{
		cfg:            &config.Config{MiMoCodeKey: []config.MiMoCodeKey{{ClientID: "client-1", BaseURL: "https://old.example.com"}}},
		configFilePath: writeTestConfigFile(t),
	}
	r := setupTestRouter(h)
	r.PATCH("/mimo-code-api-key", h.PatchMiMoCodeKey)

	body := []byte(`{"match":"client-1","value":{"client-id":" client-2 ","base-url":" https://new.example.com ","headers":{" X-Test ":" value "}}}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/mimo-code-api-key", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	entry := h.cfg.MiMoCodeKey[0]
	if entry.ClientID != "client-2" || entry.BaseURL != "https://new.example.com" {
		t.Fatalf("entry was not patched and normalized: %#v", entry)
	}
	if got := entry.Headers["X-Test"]; got != "value" {
		t.Fatalf("header X-Test = %q, want %q", got, "value")
	}
}

func TestDeleteMiMoCodeKeyUsesClientIDAndBaseURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{
		cfg: &config.Config{MiMoCodeKey: []config.MiMoCodeKey{
			{ClientID: "shared-client", BaseURL: "https://a.example.com"},
			{ClientID: "shared-client", BaseURL: "https://b.example.com"},
		}},
		configFilePath: writeTestConfigFile(t),
	}
	r := setupTestRouter(h)
	r.DELETE("/mimo-code-api-key", h.DeleteMiMoCodeKey)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/mimo-code-api-key?client-id=shared-client&base-url=https://a.example.com", nil)
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.MiMoCodeKey); got != 1 {
		t.Fatalf("mimo-code keys len = %d, want 1", got)
	}
	if got := h.cfg.MiMoCodeKey[0].BaseURL; got != "https://b.example.com" {
		t.Fatalf("remaining base-url = %q, want %q", got, "https://b.example.com")
	}
}

func TestGetMiMoCodeKeysIncludesAuthIndex(t *testing.T) {
	gin.SetMode(gin.TestMode)
	idGen := synthesizer.NewStableIDGenerator()
	id, _ := idGen.Next("mimo-code:apikey", "client-1", "https://mimo.example.com")
	manager := coreauth.NewManager(nil, nil, nil)
	registered, err := manager.Register(context.Background(), &coreauth.Auth{ID: id, Provider: "mimo-code"})
	if err != nil {
		t.Fatalf("register auth: %v", err)
	}
	expectedAuthIndex := registered.Index
	h := NewHandler(
		&config.Config{MiMoCodeKey: []config.MiMoCodeKey{{ClientID: "client-1", BaseURL: "https://mimo.example.com"}}},
		writeTestConfigFile(t),
		manager,
	)
	r := setupTestRouter(h)
	r.GET("/mimo-code-api-key", h.GetMiMoCodeKeys)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/mimo-code-api-key", nil)
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		Keys []struct {
			ClientID  string `json:"client-id"`
			AuthIndex string `json:"auth-index"`
		} `json:"mimo-code-api-key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v body=%s", err, rec.Body.String())
	}
	if len(payload.Keys) != 1 {
		t.Fatalf("keys len = %d, want 1; body=%s", len(payload.Keys), rec.Body.String())
	}
	if payload.Keys[0].ClientID != "client-1" || payload.Keys[0].AuthIndex != expectedAuthIndex {
		t.Fatalf("unexpected key payload: %#v, want auth-index %q", payload.Keys[0], expectedAuthIndex)
	}
}
