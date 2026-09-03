package management

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Command Code quota endpoints, ported from the opencodex fork's
// src/providers/quota.ts. The credits endpoint is the same Bearer surface the
// Command Code CLI's usage view uses (windowLimits.fiveHour / weekly), plus a
// soft whoami (team orgId scoping) and subscription-scoped spend for creditsUsd.
const (
	commandCodeQuotaBaseURL      = "https://api.commandcode.ai"
	commandCodeQuotaWhoamiPath   = "/alpha/whoami"
	commandCodeQuotaCreditsPath  = "/alpha/billing/credits"
	commandCodeQuotaSubsPath     = "/alpha/billing/subscriptions"
	commandCodeQuotaUsagePath    = "/alpha/usage/summary"
	commandCodeQuotaHTTPTimeout  = 15 * time.Second
	commandCodeQuotaMaxBodyBytes = 1 << 20
)

// commandCodeQuotaWindow is a single normalized Command Code rolling window
// (cap/used off /alpha/billing/credits), expressed as a used percentage.
type commandCodeQuotaWindow struct {
	Name             string     `json:"name"`
	UsedPercent      float64    `json:"used_percent"`
	RemainingPercent float64    `json:"remaining_percent"`
	ResetAt          *time.Time `json:"reset_at,omitempty"`
}

// commandCodeCreditsUsd is the subscription-scoped spend against the remaining
// credit pools, normalized to a used percentage.
type commandCodeCreditsUsd struct {
	Used      float64    `json:"used"`
	Limit     float64    `json:"limit"`
	Remaining float64    `json:"remaining"`
	Percent   float64    `json:"percent"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// commandCodeQuotaResponse is the normalized quota payload returned to the caller.
type commandCodeQuotaResponse struct {
	AuthIndex string                 `json:"auth_index,omitempty"`
	Email     string                 `json:"email,omitempty"`
	FiveHour  commandCodeQuotaWindow `json:"five_hour"`
	Weekly    commandCodeQuotaWindow `json:"weekly"`
	Credits   *commandCodeCreditsUsd `json:"credits_usd,omitempty"`
}

// GetCommandCodeQuota reports the Command Code account quota for a commandcode
// credential.
//
// Endpoint:
//
//	GET /v0/management/commandcode-quota
//
// Query Parameters (optional):
//   - auth_index: The credential "auth_index" from GET /v0/management/auth-files.
//     If omitted, uses the first available commandcode credential.
//
// The quota is fetched from api.commandcode.ai with the configured API key,
// mirroring the opencodex reference implementation. The response is normalized
// into fixed quota windows: five-hour tokens, weekly tokens, and subscription
// credits spend (credits_usd).
func (h *Handler) GetCommandCodeQuota(c *gin.Context) {
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

	auth := h.resolveCommandCodeAuth(authIndex)
	if auth == nil {
		if authIndex != "" {
			c.JSON(http.StatusNotFound, gin.H{"error": "commandcode credential not found"})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no commandcode credential found"})
		}
		return
	}

	apiKey := commandCodeQuotaAPIKey(auth)
	if apiKey == "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "commandcode api key unavailable; re-login required"})
		return
	}

	quota, errFetch := h.fetchCommandCodeQuota(c.Request.Context(), auth, apiKey)
	if errFetch != nil {
		log.WithError(errFetch).Debug("commandcode quota request failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": errFetch.Error()})
		return
	}

	auth.EnsureIndex()
	quota.AuthIndex = strings.TrimSpace(auth.Index)
	if quota.AuthIndex == "" {
		quota.AuthIndex = strings.TrimSpace(auth.ID)
	}
	if email := strings.TrimSpace(authAttribute(auth, "email")); email != "" {
		quota.Email = email
	} else if raw, ok := auth.Metadata["email"].(string); ok {
		quota.Email = strings.TrimSpace(raw)
	}

	c.JSON(http.StatusOK, quota)
}

// resolveCommandCodeAuth locates a commandcode credential by auth index, or
// returns the first available commandcode credential when authIndex is empty.
func (h *Handler) resolveCommandCodeAuth(authIndex string) *coreauth.Auth {
	if h == nil || h.authManager == nil {
		return nil
	}
	if authIndex != "" {
		auth := h.authByIndex(authIndex)
		if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "commandcode") {
			return nil
		}
		return auth
	}
	var firstCommandCode *coreauth.Auth
	for _, auth := range h.authManager.List() {
		if auth == nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(auth.Provider), "commandcode") {
			continue
		}
		if firstCommandCode == nil {
			firstCommandCode = auth
		}
	}
	return firstCommandCode
}

// commandCodeQuotaAPIKey returns the Command Code API key from the auth record,
// mirroring the executor's commandCodeAPIKey resolution order.
func commandCodeQuotaAPIKey(auth *coreauth.Auth) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	for _, keyName := range []string{"api_key", "apiKey", "key", "commandcode", "access"} {
		if key := strings.TrimSpace(auth.Attributes[keyName]); key != "" {
			return key
		}
	}
	return ""
}

// commandCodeQuotaBaseURLForAuth resolves the quota base host from the auth
// record's base_url override, falling back to the canonical Command Code host.
func commandCodeQuotaBaseURLForAuth(auth *coreauth.Auth) string {
	if auth != nil && auth.Attributes != nil {
		if configured := strings.TrimSpace(auth.Attributes["base_url"]); configured != "" {
			return strings.TrimRight(configured, "/")
		}
	}
	return commandCodeQuotaBaseURL
}

// fetchCommandCodeQuota performs the authenticated Command Code quota probe:
// whoami (orgId scoping) + /alpha/billing/credits, then subscription-scoped
// spend for creditsUsd. Every upstream failure soft-fails to an error.
func (h *Handler) fetchCommandCodeQuota(ctx context.Context, auth *coreauth.Auth, apiKey string) (*commandCodeQuotaResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, commandCodeQuotaHTTPTimeout)
	defer cancel()

	base := commandCodeQuotaBaseURLForAuth(auth)
	client := &http.Client{
		Transport: h.apiCallTransport(auth, ""),
		// The upstream contract rejects redirects; surface the 3xx instead.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// whoami: resolve the team orgId so credits/usage are scoped to the same
	// account that routes requests.
	orgQuery := ""
	if whoamiBody, err := h.fetchCommandCodeJSON(ctx, client, base+commandCodeQuotaWhoamiPath, apiKey); err == nil {
		if orgID := commandCodeQuotaOrgID(whoamiBody); orgID != "" {
			orgQuery = "?orgId=" + orgID
		}
	}

	creditsBody, errCredits := h.fetchCommandCodeJSON(ctx, client, base+commandCodeQuotaCreditsPath+orgQuery, apiKey)
	if errCredits != nil {
		return nil, errCredits
	}
	credits := commandCodeQuotaCredits(creditsBody)
	limits := commandCodeQuotaWindowLimits(creditsBody)
	if credits == nil && limits == nil {
		return nil, fmt.Errorf("commandcode quota response missing credits and windowLimits")
	}

	result := &commandCodeQuotaResponse{
		FiveHour: commandCodeQuotaWindow{Name: "five_hour"},
		Weekly:   commandCodeQuotaWindow{Name: "weekly"},
	}
	if fiveHour := parseCommandCodeQuotaWindow(limits, "fiveHour"); fiveHour != nil {
		result.FiveHour = *fiveHour
	}
	if weekly := parseCommandCodeQuotaWindow(limits, "weekly"); weekly != nil {
		result.Weekly = *weekly
	}
	if credits != nil {
		if creditsUsd := h.fetchCommandCodeSpend(ctx, client, base, apiKey, credits, orgQuery); creditsUsd != nil {
			result.Credits = creditsUsd
		}
	}
	return result, nil
}

// fetchCommandCodeJSON performs a soft-fail GET returning the parsed JSON
// record, or an error when the upstream is unavailable.
func (h *Handler) fetchCommandCodeJSON(ctx context.Context, client *http.Client, url, apiKey string) (map[string]any, error) {
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if errReq != nil {
		return nil, fmt.Errorf("commandcode quota: build request: %w", errReq)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "CLIProxyAPI-Management")

	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("commandcode quota request failed: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		bodyText, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("commandcode quota upstream returned %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyText)))
	}
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, commandCodeQuotaMaxBodyBytes))
	if errRead != nil {
		return nil, fmt.Errorf("commandcode quota: read response: %w", errRead)
	}
	var parsed map[string]any
	if errJSON := json.Unmarshal(body, &parsed); errJSON != nil {
		return nil, fmt.Errorf("commandcode quota: decode response: %w", errJSON)
	}
	return parsed, nil
}

// commandCodeQuotaOrgID extracts the team orgId from the whoami payload,
// preferring the nested `data` shell when the outer object is only an envelope.
func commandCodeQuotaOrgID(body map[string]any) string {
	payload := commandCodeQuotaUnwrapData(body)
	org, _ := payload["org"].(map[string]any)
	if org == nil {
		return ""
	}
	id, _ := org["id"].(string)
	return strings.TrimSpace(id)
}

// commandCodeQuotaUnwrapData prefers the nested `data` shell when the outer
// object is only an envelope ({ data: {...} }).
func commandCodeQuotaUnwrapData(body map[string]any) map[string]any {
	if nested, ok := body["data"].(map[string]any); ok {
		return nested
	}
	return body
}

// commandCodeQuotaCredits extracts the credits pool from the credits payload.
func commandCodeQuotaCredits(body map[string]any) map[string]any {
	payload := commandCodeQuotaUnwrapData(body)
	credits, _ := payload["credits"].(map[string]any)
	return credits
}

// commandCodeQuotaWindowLimits extracts the windowLimits map from the credits
// payload.
func commandCodeQuotaWindowLimits(body map[string]any) map[string]any {
	payload := commandCodeQuotaUnwrapData(body)
	limits, _ := payload["windowLimits"].(map[string]any)
	return limits
}

// parseCommandCodeQuotaWindow normalizes one rolling window ({ cap, used,
// resetAt }) into a used-percentage window, or nil when the row is absent or
// malformed.
func parseCommandCodeQuotaWindow(limits map[string]any, key string) *commandCodeQuotaWindow {
	if limits == nil {
		return nil
	}
	row, _ := limits[key].(map[string]any)
	if row == nil {
		return nil
	}
	capValue, capOK := commandCodeQuotaNumber(row["cap"])
	used, usedOK := commandCodeQuotaNumber(row["used"])
	if !capOK || !usedOK || capValue <= 0 || used < 0 {
		return nil
	}
	percent := (used / capValue) * 100
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	window := &commandCodeQuotaWindow{
		Name:             key,
		UsedPercent:      percent,
		RemainingPercent: 100 - percent,
	}
	if resetAt := commandCodeQuotaResetAt(row); resetAt != nil {
		window.ResetAt = resetAt
	}
	return window
}

// commandCodeQuotaResetAt parses a reset timestamp from the common field names.
func commandCodeQuotaResetAt(row map[string]any) *time.Time {
	for _, key := range []string{"resetAt", "resetTime", "reset_at", "reset_time"} {
		if value, ok := row[key]; ok {
			if t, ok := commandCodeQuotaTime(value); ok {
				return &t
			}
		}
	}
	return nil
}

// commandCodeQuotaNumber coerces a JSON number (float64 or int) to float64.
func commandCodeQuotaNumber(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// commandCodeQuotaTime coerces a reset timestamp (epoch seconds or milliseconds,
// or an RFC3339 string) to time.Time.
func commandCodeQuotaTime(value any) (time.Time, bool) {
	switch v := value.(type) {
	case float64:
		return commandCodeQuotaTimeFromEpoch(v), true
	case int:
		return commandCodeQuotaTimeFromEpoch(float64(v)), true
	case int64:
		return commandCodeQuotaTimeFromEpoch(float64(v)), true
	case string:
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// commandCodeQuotaTimeFromEpoch interprets a numeric reset as epoch seconds
// when small, otherwise epoch milliseconds.
func commandCodeQuotaTimeFromEpoch(value float64) time.Time {
	if value > 1e12 {
		return time.UnixMilli(int64(value)).UTC()
	}
	return time.Unix(int64(value), 0).UTC()
}

// fetchCommandCodeSpend computes the subscription-scoped spend (used) against
// the remaining credit pools → creditsUsd. Period scoping: `since=<currentPeriodStart>`
// keeps spend aligned with the pools' billing cycle, and `currentPeriodEnd`
// becomes expiresAt.
func (h *Handler) fetchCommandCodeSpend(ctx context.Context, client *http.Client, base, apiKey string, credits map[string]any, orgQuery string) *commandCodeCreditsUsd {
	subscriptionBody, errSubs := h.fetchCommandCodeJSON(ctx, client, base+commandCodeQuotaSubsPath+orgQuery, apiKey)
	if errSubs != nil {
		return nil
	}
	subscription := commandCodeQuotaUnwrapData(subscriptionBody)
	periodStart, _ := subscription["currentPeriodStart"].(string)
	periodStart = strings.TrimSpace(periodStart)
	// Unscoped /usage/summary is lifetime spend; mixing it with current-cycle
	// remaining pools produces a wrong percent. Omit creditsUsd until a period exists.
	if periodStart == "" {
		return nil
	}
	separator := "?"
	if orgQuery != "" {
		separator = "&"
	}
	summaryBody, errSummary := h.fetchCommandCodeJSON(ctx, client, base+commandCodeQuotaUsagePath+orgQuery+separator+"since="+periodStart, apiKey)
	if errSummary != nil {
		return nil
	}
	summary := commandCodeQuotaUnwrapData(summaryBody)
	used, usedOK := commandCodeQuotaNumber(summary["totalCost"])
	if !usedOK {
		used, usedOK = commandCodeQuotaNumber(summary["totalMonthlyCredits"])
	}
	if !usedOK || used < 0 {
		return nil
	}

	var pools []float64
	for _, key := range []string{"monthlyCredits", "purchasedCredits", "freeCredits"} {
		if value, ok := commandCodeQuotaNumber(credits[key]); ok {
			pools = append(pools, value)
		}
	}
	// Field presence is what separates a real balance from absent data: an
	// exhausted all-zero account still reports remaining=0, while no
	// remaining-credit field at all means there is nothing to meter.
	if len(pools) == 0 {
		return nil
	}
	remaining := 0.0
	for _, value := range pools {
		if value > 0 {
			remaining += value
		}
	}
	limit := used + remaining
	percent := 0.0
	if limit > 0 {
		percent = (used / limit) * 100
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	// Purchased credits roll over past the subscription period end, so an expiry
	// is only truthful when the aggregate contains no non-expiring purchased pool.
	purchased, _ := commandCodeQuotaNumber(credits["purchasedCredits"])
	result := &commandCodeCreditsUsd{
		Used:      used,
		Limit:     limit,
		Remaining: remaining,
		Percent:   percent,
	}
	if expiresAt := commandCodeQuotaResetAt(subscription); expiresAt != nil && purchased <= 0 {
		result.ExpiresAt = expiresAt
	}
	return result
}
