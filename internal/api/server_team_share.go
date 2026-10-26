package api

import (
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/managementasset"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/teamshare"
)

// teamShareGuard rejects a generation request with 429 once the authenticated client key has
// used up its percentage of a team pool window. Read-only lookups such as model listings pass.
func teamShareGuard(c *gin.Context, apiKey string) {
	switch c.Request.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return
	}
	engine := teamshare.Default()
	if apiKey == "" || !engine.Enabled() {
		return
	}
	decision := engine.Check(apiKey)
	if decision.Allowed {
		return
	}
	c.Header("Retry-After", strconv.Itoa(decision.RetryAfterSeconds(time.Now())))
	c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": gin.H{
		"type":              "rate_limit_error",
		"code":              "team_share_exceeded",
		"message":           fmt.Sprintf("team share exhausted for the %s window of pool %q; it resets at %s", decision.Window, decision.Pool, decision.ResetAt.UTC().Format(time.RFC3339)),
		"pool":              decision.Pool,
		"window":            decision.Window,
		"allocated_usd":     decision.AllocatedUSD,
		"used_usd":          decision.UsedUSD,
		"remaining_usd":     decision.RemainingUSD,
		"remaining_percent": decision.RemainingPercent,
		"reset_at":          decision.ResetAt.UTC().Format(time.RFC3339),
	}})
}

func (s *Server) serveTeamSharePanel(c *gin.Context) {
	cfg := s.getConfig()
	if cfg == nil || cfg.Home.Enabled || cfg.RemoteManagement.DisableControlPanel {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "text/html; charset=utf-8", managementasset.TeamShareHTML())
}
