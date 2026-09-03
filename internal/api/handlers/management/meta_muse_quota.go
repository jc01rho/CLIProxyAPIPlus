package management

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// metaMuseQuotaResponse is the normalized, cache-only Meta Muse quota payload
// returned to the caller: the five-hour and weekly rolling-window used
// percentages last observed on a real chat completion response, plus the
// time of that observation.
type metaMuseQuotaResponse struct {
	AuthIndex           string     `json:"auth_index,omitempty"`
	FiveHourUsedPercent *float64   `json:"five_hour_used_percent,omitempty"`
	FiveHourResetAt     *time.Time `json:"five_hour_reset_at,omitempty"`
	WeeklyUsedPercent   *float64   `json:"weekly_used_percent,omitempty"`
	WeeklyResetAt       *time.Time `json:"weekly_reset_at,omitempty"`
	ObservedAt          time.Time  `json:"observed_at"`
}

// GetMetaMuseQuota reports the Meta Muse Model API quota for a meta
// credential.
//
// Endpoint:
//
//	GET /v0/management/meta-muse-quota
//
// Query Parameters (optional):
//   - auth_index: The credential "auth_index" from GET /v0/management/auth-files.
//     If omitted, uses the first available meta credential.
//
// Unlike the Command Code and Z.AI quota endpoints, Meta Muse exposes no
// dedicated quota API. Its response stream contains the
// response.subscription_usage event; the executor converts that event into
// internal result headers for QuotaState persistence. This handler only
// serves that cached snapshot and never dials upstream.
func (h *Handler) GetMetaMuseQuota(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	authIndex := strings.TrimSpace(c.Query("auth_index"))
	if authIndex == "" {
		authIndex = strings.TrimSpace(c.Query("authIndex"))
	}
	if authIndex == "" {
		authIndex = strings.TrimSpace(c.Query("AuthIndex"))
	}

	auth := h.resolveMetaMuseAuth(authIndex)
	if auth == nil {
		if authIndex != "" {
			c.JSON(http.StatusNotFound, gin.H{"error": "meta credential not found"})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no meta credential found"})
		}
		return
	}

	usage, ok := auth.Quota.MuseUsage()
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "meta muse quota not observed yet"})
		return
	}

	auth.EnsureIndex()
	response := metaMuseQuotaResponse{
		AuthIndex:           strings.TrimSpace(auth.Index),
		FiveHourUsedPercent: usage.FiveHourUsedPercent,
		FiveHourResetAt:     usage.FiveHourResetAt,
		WeeklyUsedPercent:   usage.WeeklyUsedPercent,
		WeeklyResetAt:       usage.WeeklyResetAt,
		ObservedAt:          usage.ObservedAt,
	}
	if response.AuthIndex == "" {
		response.AuthIndex = strings.TrimSpace(auth.ID)
	}
	c.JSON(http.StatusOK, response)
}

// resolveMetaMuseAuth locates a meta credential by auth index, or returns the
// first available meta credential when authIndex is empty.
func (h *Handler) resolveMetaMuseAuth(authIndex string) *coreauth.Auth {
	if h == nil || h.authManager == nil {
		return nil
	}
	if authIndex != "" {
		auth := h.authByIndex(authIndex)
		if auth == nil || !coreauth.IsMetaMuseProvider(auth.Provider) {
			return nil
		}
		return auth
	}
	var firstMeta *coreauth.Auth
	for _, auth := range h.authManager.List() {
		if auth == nil {
			continue
		}
		if !coreauth.IsMetaMuseProvider(auth.Provider) {
			continue
		}
		if firstMeta == nil {
			firstMeta = auth
		}
	}
	return firstMeta
}
