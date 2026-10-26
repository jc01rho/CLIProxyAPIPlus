package management

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/teamshare"
)

// GetTeamShare returns per-pool totals, per-account utilization and per-member allocation.
func (h *Handler) GetTeamShare(c *gin.Context) {
	c.JSON(http.StatusOK, teamshare.Default().Snapshot())
}

// PostTeamShareAckReauth clears a re-authentication flag after the operator re-registered
// the credential. Re-login itself is never automatic.
func (h *Handler) PostTeamShareAckReauth(c *gin.Context) {
	authID := strings.TrimSpace(c.Query("auth_id"))
	if authID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_id is required"})
		return
	}
	if !teamshare.Default().AckReauth(authID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "no team-share state for that credential"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
