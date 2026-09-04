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
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	claudeQuotaHTTPTimeout  = 25 * time.Second
	claudeQuotaMaxBodyBytes = int64(1 << 20)
	// claudeQuotaDefaultBaseURL is the Anthropic default; per-auth base_url
	// overrides it and per-auth quota_url bypasses it entirely.
	claudeQuotaDefaultBaseURL = "https://api.anthropic.com"
	claudeQuotaSelfPath       = "/v1/usage/self"
)

type claudeQuotaResponse struct {
	AuthIndex    string                 `json:"auth_index"`
	Email        string                 `json:"email,omitempty"`
	ProviderKey  string                 `json:"provider_key,omitempty"`
	ObservedAt   string                 `json:"observed_at"`
	RequestCount int64                  `json:"request_count,omitempty"`
	TotalTokens  int64                  `json:"total_tokens,omitempty"`
	TotalCostUSD float64                `json:"total_cost_usd,omitempty"`
	Limits       []claudeQuotaLimitView `json:"limits"`
}

type claudeQuotaLimitView struct {
	LimitType      string  `json:"limit_type"`
	LimitWindow    string  `json:"limit_window"`
	MaxValue       float64 `json:"max_value"`
	CurrentValue   float64 `json:"current_value"`
	RemainingValue float64 `json:"remaining_value"`
	UsedPercent    float64 `json:"used_percent"`
	ModelFilter    *string `json:"model_filter"`
	ResetAt        string  `json:"reset_at"`
}

// thirdPartyClaudeUsage mirrors 3rd-party Claude-compatible quota documents
// such as https://claude.nekos.me/v1/usage/self: a flat request/token/cost
// summary plus a limits array keyed by limit_type/limit_window.
type thirdPartyClaudeUsage struct {
	RequestCount int64   `json:"request_count"`
	TotalTokens  int64   `json:"total_tokens"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	Limits       []struct {
		LimitType      string  `json:"limit_type"`
		LimitWindow    string  `json:"limit_window"`
		MaxValue       float64 `json:"max_value"`
		CurrentValue   float64 `json:"current_value"`
		RemainingValue float64 `json:"remaining_value"`
		UsedPercent    float64 `json:"used_percent"`
		ModelFilter    *string `json:"model_filter"`
		ResetAt        string  `json:"reset_at"`
	} `json:"limits"`
}

// GetClaudeQuota proxies an authenticated GET to the per-auth quota URL and
// returns the parsed quota. URL resolution order: auth Attributes["quota_url"]
// first (fully modifiable, independent of base_url), then
// Attributes["base_url"] + /v1/usage/self, then the Anthropic default.
func (h *Handler) GetClaudeQuota(c *gin.Context) {
	authIndex := strings.TrimSpace(c.Query("auth_index"))
	if authIndex == "" {
		authIndex = strings.TrimSpace(c.Query("authIndex"))
	}
	if authIndex == "" {
		authIndex = strings.TrimSpace(c.Query("AuthIndex"))
	}

	auth := h.resolveClaudeQuotaAuth(authIndex)
	if auth == nil {
		if authIndex != "" {
			c.JSON(http.StatusNotFound, gin.H{"error": "claude credential not found"})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no claude credential found"})
		}
		return
	}

	apiKey := claudeQuotaAPIKey(auth)
	if apiKey == "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "claude api key unavailable; set attributes.api_key or re-login required"})
		return
	}

	quota, errFetch := h.fetchClaudeQuota(c.Request.Context(), auth, apiKey)
	if errFetch != nil {
		log.WithError(errFetch).Debug("claude quota request failed")
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
	if pk := strings.TrimSpace(auth.Provider); pk != "" {
		quota.ProviderKey = pk
	}

	c.JSON(http.StatusOK, quota)
}

func (h *Handler) resolveClaudeQuotaAuth(authIndex string) *auth.Auth {
	if h == nil || h.authManager == nil {
		return nil
	}
	if authIndex != "" {
		found := h.authByIndex(authIndex)
		if found == nil || !strings.EqualFold(strings.TrimSpace(found.Provider), "claude") {
			return nil
		}
		return found
	}
	var first *auth.Auth
	for _, candidate := range h.authManager.List() {
		if candidate == nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(candidate.Provider), "claude") {
			continue
		}
		if first == nil {
			first = candidate
		}
	}
	return first
}

func claudeQuotaAPIKey(target *auth.Auth) string {
	if target == nil {
		return ""
	}
	if target.Attributes != nil {
		if key := strings.TrimSpace(target.Attributes["api_key"]); key != "" {
			return key
		}
	}
	if target.Metadata != nil {
		if token, ok := target.Metadata["access_token"].(string); ok {
			return strings.TrimSpace(token)
		}
	}
	return ""
}

// claudeQuotaURL resolves the quota endpoint: explicit Attributes["quota_url"]
// wins, then Attributes["base_url"] + /v1/usage/self, then the default.
func claudeQuotaURL(target *auth.Auth) string {
	if target != nil && target.Attributes != nil {
		if raw := strings.TrimSpace(target.Attributes["quota_url"]); raw != "" {
			return raw
		}
		if raw := strings.TrimSpace(target.Attributes["base_url"]); raw != "" {
			return strings.TrimRight(raw, "/") + claudeQuotaSelfPath
		}
	}
	return claudeQuotaDefaultBaseURL + claudeQuotaSelfPath
}

func (h *Handler) fetchClaudeQuota(ctx context.Context, target *auth.Auth, apiKey string) (*claudeQuotaResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, claudeQuotaHTTPTimeout)
	defer cancel()

	endpoint := claudeQuotaURL(target)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build claude quota request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Transport: h.apiCallTransport(target, "")}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request claude quota: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("claude quota request failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(preview)))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, claudeQuotaMaxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("read claude quota response: %w", err)
	}
	var doc thirdPartyClaudeUsage
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse claude quota response: %w", err)
	}
	out := &claudeQuotaResponse{
		ObservedAt:   time.Now().UTC().Format(time.RFC3339),
		RequestCount: doc.RequestCount,
		TotalTokens:  doc.TotalTokens,
		TotalCostUSD: doc.TotalCostUSD,
		Limits:       make([]claudeQuotaLimitView, 0, len(doc.Limits)),
	}
	for _, limit := range doc.Limits {
		out.Limits = append(out.Limits, claudeQuotaLimitView{
			LimitType:      strings.TrimSpace(limit.LimitType),
			LimitWindow:    strings.TrimSpace(limit.LimitWindow),
			MaxValue:       limit.MaxValue,
			CurrentValue:   limit.CurrentValue,
			RemainingValue: limit.RemainingValue,
			UsedPercent:    limit.UsedPercent,
			ModelFilter:    limit.ModelFilter,
			ResetAt:        strings.TrimSpace(limit.ResetAt),
		})
	}
	return out, nil
}
