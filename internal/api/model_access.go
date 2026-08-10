package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
)

func apiKeyModelAccessMiddleware(loadConfig func() *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		var cfg *config.Config
		if loadConfig != nil {
			cfg = loadConfig()
		}
		if cfg == nil {
			c.Next()
			return
		}
		rawPrincipal, ok := c.Get("userApiKey")
		if !ok {
			c.Next()
			return
		}
		principal, ok := rawPrincipal.(string)
		if !ok || principal == "" {
			c.Next()
			return
		}
		patterns := cfg.APIKeyModelWhitelists[principal]
		if len(patterns) == 0 {
			c.Next()
			return
		}
		sdkaccess.SetModelAccessPatterns(c, patterns)
		model := requestedModelFromRequest(c.Request)
		if model == "" || sdkaccess.ModelAllowed(model, patterns) {
			c.Next()
			return
		}
		c.AbortWithStatusJSON(http.StatusForbidden, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "model is not allowed for this API key",
				Type:    "permission_error",
				Code:    "model_not_allowed",
			},
		})
	}
}

func requestedModelFromRequest(request *http.Request) string {
	if request == nil || request.URL == nil {
		return ""
	}
	if model := strings.TrimSpace(request.URL.Query().Get("model")); model != "" {
		return model
	}
	if model := requestedGeminiPathModel(request.URL.Path); model != "" {
		return model
	}
	if request.Body == nil || !strings.Contains(strings.ToLower(request.Header.Get("Content-Type")), "json") {
		return ""
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return ""
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	var payload struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	return strings.TrimSpace(payload.Model)
}

func requestedGeminiPathModel(path string) string {
	const marker = "/models/"
	index := strings.Index(path, marker)
	if index < 0 {
		return ""
	}
	model := path[index+len(marker):]
	if separator := strings.IndexAny(model, ":/"); separator >= 0 {
		model = model[:separator]
	}
	return strings.TrimSpace(model)
}
