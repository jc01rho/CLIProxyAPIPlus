package cliproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/api"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	internalregistry "github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"golang.org/x/crypto/bcrypt"
)

func TestAuthFileModels_ClaudeAliasForkPreservesSourceAndAlias(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-opus-5-5","display_name":"Claude Opus 5.5"}]}`))
	}))
	defer upstream.Close()

	secret := "test-secret"
	hashedSecret, errHash := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.MinCost)
	if errHash != nil {
		t.Fatalf("hash management secret: %v", errHash)
	}
	authDir := t.TempDir()
	cfg := &internalconfig.Config{
		AuthDir: authDir,
		RemoteManagement: internalconfig.RemoteManagement{
			SecretKey:   string(hashedSecret),
			AllowRemote: true,
		},
	}
	service, errBuild := NewBuilder().
		WithConfig(cfg).
		WithConfigPath(filepath.Join(authDir, "config.yaml")).
		Build()
	if errBuild != nil {
		t.Fatalf("build service: %v", errBuild)
	}
	server := api.NewServer(service.cfg, service.coreManager, service.accessManager, service.configPath, service.serverOptions...)
	if server == nil {
		t.Fatal("new server returned nil")
	}

	reg := internalregistry.GetGlobalRegistry()
	authID := "claude-sajjon-alias-fork-test"
	reg.UnregisterClient(authID)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	auth := &coreauth.Auth{
		ID:       authID,
		FileName: "claude-sajjon.json",
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"base_url":  upstream.URL,
			"auth_kind": coreauth.AuthKindOAuth,
		},
		Metadata: map[string]any{
			"access_token": "test-token",
			"base_url":     upstream.URL,
			"type":         "claude",
		},
	}
	coreauth.SetOAuthModelAliasesAttribute(auth, []internalconfig.OAuthModelAlias{
		{Name: "claude-opus-5-5", Alias: "opus", Fork: true},
	})
	if _, errRegister := service.coreManager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/auth-files/models?name="+auth.FileName, nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("model list status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		Models []*internalregistry.ModelInfo `json:"models"`
	}
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &payload); errDecode != nil {
		t.Fatalf("decode model list: %v", errDecode)
	}
	for _, modelID := range []string{"claude-opus-5-5", "opus"} {
		if !hasRegisteredModel(payload.Models, modelID) {
			t.Fatalf("expected %s after model refresh, got %v", modelID, registeredModelIDs(payload.Models))
		}
	}
}

func hasRegisteredModel(models []*internalregistry.ModelInfo, modelID string) bool {
	for _, model := range models {
		if model != nil && model.ID == modelID {
			return true
		}
	}
	return false
}

func registeredModelIDs(models []*internalregistry.ModelInfo) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		if model != nil {
			ids = append(ids, model.ID)
		}
	}
	return ids
}
