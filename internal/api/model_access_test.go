package api

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

func TestAPIKeyModelAccessMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.APIKeyModelWhitelists = map[string][]string{
		"gpt-only": {"gpt-*", "o3"},
	}

	newEngine := func(apiKey string) *gin.Engine {
		engine := gin.New()
		engine.Use(func(c *gin.Context) {
			c.Set("userApiKey", apiKey)
			c.Next()
		})
		engine.Use(apiKeyModelAccessMiddleware(func() *config.Config { return cfg }))
		engine.POST("/v1/chat/completions", func(c *gin.Context) {
			body, _ := io.ReadAll(c.Request.Body)
			c.Data(http.StatusOK, "application/json", body)
		})
		engine.GET("/v1/models", func(c *gin.Context) {
			patterns := sdkaccess.ModelAccessPatterns(c)
			c.JSON(http.StatusOK, gin.H{
				"gpt":    sdkaccess.ModelAllowed("gpt-5.2", patterns),
				"claude": sdkaccess.ModelAllowed("claude-sonnet-4", patterns),
			})
		})
		return engine
	}

	t.Run("allows matching wildcard and preserves request body", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(
			http.MethodPost,
			"/v1/chat/completions",
			bytes.NewBufferString(`{"model":"gpt-5.2","messages":[]}`),
		)
		request.Header.Set("Content-Type", "application/json")
		newEngine("gpt-only").ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		if recorder.Body.String() != `{"model":"gpt-5.2","messages":[]}` {
			t.Fatalf("body was not preserved: %s", recorder.Body.String())
		}
	})

	t.Run("rejects model outside whitelist", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(
			http.MethodPost,
			"/v1/chat/completions",
			bytes.NewBufferString(`{"model":"claude-sonnet-4","messages":[]}`),
		)
		request.Header.Set("Content-Type", "application/json")
		newEngine("gpt-only").ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("filters model-list context for restricted key", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		newEngine("gpt-only").ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		if recorder.Body.String() != `{"claude":false,"gpt":true}` {
			t.Fatalf("unexpected model access response: %s", recorder.Body.String())
		}
	})

	t.Run("keeps unconfigured keys unrestricted", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(
			http.MethodPost,
			"/v1/chat/completions",
			bytes.NewBufferString(`{"model":"claude-sonnet-4","messages":[]}`),
		)
		request.Header.Set("Content-Type", "application/json")
		newEngine("unrestricted").ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
	})
}

func TestAPIKeyModelAccessMiddlewareUsesReloadedConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	initial := &config.Config{}
	initial.APIKeyModelWhitelists = map[string][]string{"key": {"gpt-*"}}
	current := initial

	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("userApiKey", "key")
		c.Next()
	})
	engine.Use(apiKeyModelAccessMiddleware(func() *config.Config { return current }))
	engine.GET("/v1/models", func(c *gin.Context) {
		patterns := sdkaccess.ModelAccessPatterns(c)
		c.JSON(http.StatusOK, gin.H{
			"gpt":    sdkaccess.ModelAllowed("gpt-5.2", patterns),
			"claude": sdkaccess.ModelAllowed("claude-sonnet-4", patterns),
		})
	})

	requestModels := func() string {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		return recorder.Body.String()
	}

	if got := requestModels(); got != `{"claude":false,"gpt":true}` {
		t.Fatalf("initial policy response = %s", got)
	}
	reloaded := &config.Config{}
	reloaded.APIKeyModelWhitelists = map[string][]string{"key": {"claude-*"}}
	current = reloaded
	if got := requestModels(); got != `{"claude":true,"gpt":false}` {
		t.Fatalf("reloaded policy response = %s", got)
	}
}
