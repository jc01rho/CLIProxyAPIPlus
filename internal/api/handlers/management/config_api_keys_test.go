package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestAPIKeysModelWhitelistsRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("api-keys:\n  - unrestricted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(cfg, configPath, nil)
	router := setupTestRouter(handler)
	router.GET("/api-keys", handler.GetAPIKeys)
	router.PUT("/api-keys", handler.PutAPIKeys)

	body := []byte(`{"api-keys":["unrestricted","gpt-only"],"model-whitelists":{"gpt-only":["gpt-*","gpt-5.2"]}}`)
	put := httptest.NewRequest(http.MethodPut, "/api-keys", bytes.NewReader(body))
	put.Header.Set("Content-Type", "application/json")
	putRecorder := httptest.NewRecorder()
	router.ServeHTTP(putRecorder, put)
	if putRecorder.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", putRecorder.Code, putRecorder.Body.String())
	}

	get := httptest.NewRequest(http.MethodGet, "/api-keys", nil)
	getRecorder := httptest.NewRecorder()
	router.ServeHTTP(getRecorder, get)
	if getRecorder.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", getRecorder.Code, getRecorder.Body.String())
	}
	var response struct {
		APIKeys         []string            `json:"api-keys"`
		ModelWhitelists map[string][]string `json:"model-whitelists"`
	}
	if err := json.Unmarshal(getRecorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.APIKeys) != 2 || len(response.ModelWhitelists["gpt-only"]) != 2 {
		t.Fatalf("unexpected API key response: %#v", response)
	}

	reloaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.APIKeyModelWhitelists["gpt-only"]; len(got) != 2 || got[0] != "gpt-*" {
		t.Fatalf("persisted whitelist = %#v", got)
	}
}
