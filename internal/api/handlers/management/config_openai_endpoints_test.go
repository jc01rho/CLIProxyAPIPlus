package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestOpenAICompatEndpointsSaveReload(t *testing.T) {
	for _, v8 := range []bool{false, true} {
		for _, method := range []string{http.MethodPut, http.MethodPatch} {
			for _, endpoints := range [][]string{nil, {"/responses"}, {"/chat/completions"}, {"/responses", "/chat/completions"}} {
				t.Run(fmt.Sprintf("v8=%t/%s/%v", v8, method, endpoints), func(t *testing.T) {
					path := filepath.Join(t.TempDir(), "config.yaml")
					initial := "openai-compatibility:\n  - name: compat\n    base-url: https://example.invalid/v1\n    models:\n      - name: upstream\n        alias: route\n        supported-endpoints: [/previous]\n"
					if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
						t.Fatal(err)
					}
					cfg, err := config.LoadConfig(path)
					if err != nil {
						t.Fatal(err)
					}
					h := &Handler{cfg: cfg, configFilePath: path}
					reloads := make(chan *config.Config, 1)
					h.SetConfigReloadHook(func(_ context.Context, snapshot *config.Config) { reloads <- snapshot })
					router := gin.New()
					router.Use(func(c *gin.Context) { c.Set(ConfigV8ContextKey, v8) })
					router.PUT("/providers", h.PutOpenAICompat)
					router.PATCH("/providers", h.PatchOpenAICompat)
					router.GET("/providers", h.GetOpenAICompat)
					model := config.OpenAICompatibilityModel{Name: "upstream", Alias: "route", SupportedEndpoints: endpoints}
					modelsJSON, err := json.Marshal([]config.OpenAICompatibilityModel{model})
					if err != nil {
						t.Fatal(err)
					}
					body := fmt.Sprintf(`[{"name":"compat","base-url":"https://example.invalid/v1","models":%s}]`, modelsJSON)
					if method == http.MethodPatch {
						body = fmt.Sprintf(`{"name":"compat","value":{"models":%s}}`, modelsJSON)
					}
					response := httptest.NewRecorder()
					router.ServeHTTP(response, httptest.NewRequest(method, "/providers", strings.NewReader(body)))
					if response.Code != http.StatusOK {
						t.Fatalf("save: %d %s", response.Code, response.Body.String())
					}
					select {
					case snapshot := <-reloads:
						if !slices.Equal(snapshot.OpenAICompatibility[0].Models[0].SupportedEndpoints, endpoints) {
							t.Fatal("reload snapshot lost endpoint configuration")
						}
					case <-time.After(5 * time.Second):
						t.Fatal("reload callback missing")
					}
					loaded, err := config.LoadConfig(path)
					if err != nil {
						t.Fatal(err)
					}
					if !slices.Equal(loaded.OpenAICompatibility[0].Models[0].SupportedEndpoints, endpoints) {
						t.Fatal("saved YAML lost endpoint configuration")
					}
					h.SetConfig(loaded)
					response = httptest.NewRecorder()
					router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/providers", nil))
					var result struct {
						Providers []config.OpenAICompatibility `json:"openai-compatibility"`
					}
					if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					if response.Code != http.StatusOK || len(result.Providers) != 1 || len(result.Providers[0].Models) != 1 {
						t.Fatalf("unexpected GET response: %s", response.Body.String())
					}
					if got := result.Providers[0].Models[0]; got.Name != "upstream" || got.Alias != "route" || !slices.Equal(got.SupportedEndpoints, endpoints) {
						t.Fatalf("model roundtrip = %+v, want %+v", got, model)
					}
				})
			}
		}
	}
}
