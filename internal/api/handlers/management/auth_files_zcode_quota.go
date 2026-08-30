package management

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// zcodePlanBalanceURL is the ZCode gateway billing/balance endpoint, verified
// from the ZCode desktop app (app.asar zcodePlanBillingBalanceUrl). The call
// authenticates with the broker zcode JWT, not with the provisioned Z.AI API
// key, so it cannot ride the generic APICall $TOKEN$ path (that resolves the
// OAuth access token).
const zcodePlanBalanceURL = "https://zcode.z.ai/api/v1/zcode-plan/billing/balance"

// zcodeBalanceHTTPTimeout bounds the balance probe.
const zcodeBalanceHTTPTimeout = 15 * time.Second

// GetZcodeQuota reports the zcode gateway plan balance for one auth index.
// The response mirrors what the frontend ZcodeQuotaBody renders: the raw
// plans array plus a normalized active-plan summary. The broker JWT itself is
// never returned, only used server-side as the bearer credential.
func (h *Handler) GetZcodeQuota(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	authIndex := strings.TrimSpace(c.Query("auth_index"))
	if authIndex == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required"})
		return
	}

	auth := h.authByIndex(authIndex)
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth not found"})
		return
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "zcode") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth is not a zcode credential"})
		return
	}

	jwt := strings.TrimSpace(authAttribute(auth, "zcode_token"))
	if jwt == "" && len(auth.Metadata) > 0 {
		if raw, ok := auth.Metadata["zcode_token"].(string); ok {
			jwt = strings.TrimSpace(raw)
		}
	}
	if jwt == "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "zcode token unavailable; re-login required"})
		return
	}

	body, status, errFetch := fetchZcodeBalance(c, jwt)
	if errFetch != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": errFetch.Error()})
		return
	}
	if status < 200 || status >= 300 {
		c.JSON(status, gin.H{"error": "zcode gateway rejected the balance request", "upstream_status": status})
		return
	}

	c.Data(status, "application/json", body)
}

// fetchZcodeBalance performs the authenticated GET against the zcode gateway.
func fetchZcodeBalance(c *gin.Context, jwt string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), zcodeBalanceHTTPTimeout)
	defer cancel()

	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, zcodePlanBalanceURL, nil)
	if errReq != nil {
		return nil, 0, errReq
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "CLIProxyAPI-Management")

	client := &http.Client{Timeout: zcodeBalanceHTTPTimeout}
	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, 0, errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			_ = errClose
		}
	}()

	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errRead != nil {
		return nil, resp.StatusCode, errRead
	}
	return body, resp.StatusCode, nil
}
