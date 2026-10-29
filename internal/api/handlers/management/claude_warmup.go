package management

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/claudewarmup"
)

// PostClaudeWarmupRun triggers a Claude OAuth warmup run immediately and returns
// per-account results. It works whether or not the schedule is enabled.
// Query "jitter=false" skips the random delay between accounts.
func (h *Handler) PostClaudeWarmupRun(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	var warmupCfg config.ClaudeWarmupConfig
	if cfg := h.configSnapshot(); cfg != nil {
		warmupCfg = cfg.ClaudeWarmup
	}
	warmupCfg = warmupCfg.WithDefaults()
	opts := claudewarmup.Options{}
	if raw := strings.TrimSpace(c.Query("jitter")); raw != "" {
		if enabled, errParse := strconv.ParseBool(raw); errParse == nil {
			opts.NoJitter = !enabled
		}
	}
	results, errRun := claudewarmup.Run(c.Request.Context(), h.authManager, warmupCfg, opts)
	if errors.Is(errRun, claudewarmup.ErrAlreadyRunning) {
		c.JSON(http.StatusConflict, gin.H{"error": errRun.Error()})
		return
	}
	body := gin.H{"model": warmupCfg.Model, "max_tokens": warmupCfg.MaxTokens, "results": results}
	if errRun != nil {
		body["error"] = errRun.Error()
	}
	c.JSON(http.StatusOK, body)
}
