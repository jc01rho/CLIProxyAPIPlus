package management

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatSystemContentSaveReload(t *testing.T) {
	for _, v8 := range []bool{false, true} {
		for _, method := range []string{http.MethodPut, http.MethodPatch} {
			for _, enabled := range []bool{false, true} {
				t.Run(fmt.Sprintf("v8=%t/%s/enabled=%t", v8, method, enabled), func(t *testing.T) {
					path := filepath.Join(t.TempDir(), "config.yaml")
					initial := fmt.Sprintf("openai-compatibility:\n  - name: compat\n    base-url: https://example.invalid/v1\n    system-content-as-string: %t\n", !enabled)
					if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
						t.Fatal(err)
					}
					cfg, err := config.LoadConfig(path)
					if err != nil {
						t.Fatal(err)
					}
					h := &Handler{cfg: cfg, configFilePath: path}
					reloads := make(chan *config.Config, 1)
					h.SetConfigReloadHook(func(_ context.Context, next *config.Config) { reloads <- next })
					router := gin.New()
					router.Use(func(c *gin.Context) { c.Set(ConfigV8ContextKey, v8) })
					router.PUT("/providers", h.PutOpenAICompat)
					router.PATCH("/providers", h.PatchOpenAICompat)
					router.GET("/providers", h.GetOpenAICompat)
					body := fmt.Sprintf(`[{"name":"compat","base-url":"https://example.invalid/v1","system-content-as-string":%t}]`, enabled)
					if method == http.MethodPatch {
						body = fmt.Sprintf(`{"name":"compat","value":{"system-content-as-string":%t}}`, enabled)
					}
					response := httptest.NewRecorder()
					router.ServeHTTP(response, httptest.NewRequest(method, "/providers", strings.NewReader(body)))
					if response.Code != http.StatusOK {
						t.Fatalf("save: %d %s", response.Code, response.Body.String())
					}
					select {
					case snapshot := <-reloads:
						if got := snapshot.OpenAICompatibility[0].SystemContentAsString; got != enabled {
							t.Fatalf("hot reload value = %t, want %t", got, enabled)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("config reload callback missing")
					}
					loaded, err := config.LoadConfig(path)
					if err != nil {
						t.Fatal(err)
					}
					if got := loaded.OpenAICompatibility[0].SystemContentAsString; got != enabled {
						t.Fatalf("persisted value = %t, want %t", got, enabled)
					}
					h.SetConfig(loaded)
					response = httptest.NewRecorder()
					router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/providers", nil))
					if response.Code != http.StatusOK {
						t.Fatalf("get: %d %s", response.Code, response.Body.String())
					}
					if got := gjson.GetBytes(response.Body.Bytes(), "openai-compatibility.0.system-content-as-string").Bool(); got != enabled {
						t.Fatalf("GET value = %t, want %t", got, enabled)
					}
				})
			}
		}
	}
}
