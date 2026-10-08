package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestConfigV8JSONWritesSaveBlockStyleYAML(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := "config-version: 8\nrouting:\n  strategy: round-robin\noauth:\n  excluded-models:\n    codex: [a, b]\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	router := gin.New()
	router.PUT("/v8/management/config/*path", h.ConfigV8)
	router.PATCH("/v8/management/config", h.ConfigV8)

	for _, tc := range []struct{ method, url, body string }{
		{http.MethodPut, "/v8/management/config/api-keys/openai-compatibility", `[{"name":"new","base-url":"https://example.invalid","models":[{"name":"m1","alias":"a1"}],"headers":{"X-A":"1"},"keys":[{"api-key":"k1"}]}]`},
		{http.MethodPatch, "/v8/management/config", `{"routing":{"retry":{"request-retry":2}}}`},
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.url, strings.NewReader(tc.body)))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s %s: status=%d body=%s", tc.method, tc.url, recorder.Code, recorder.Body.String())
		}
	}

	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(saved)
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, `{"`) || strings.Contains(line, `[{`) || strings.Contains(line, `"name"`) {
			t.Fatalf("JSON write was saved in flow style: %q\n%s", line, text)
		}
	}
	for _, want := range []string{"- name: new", "base-url: https://example.invalid", "request-retry: 2"} {
		if !strings.Contains(text, want) {
			t.Fatalf("saved config missing %q:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "codex: [a, b]") {
		t.Fatalf("hand-written flow sequence was rewritten:\n%s", text)
	}
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.OpenAICompatibility) != 1 || loaded.OpenAICompatibility[0].Name != "new" {
		t.Fatalf("openai-compatibility not persisted: %+v", loaded.OpenAICompatibility)
	}
}
