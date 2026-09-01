package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/zcode"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// zcodeQuotaPath is the Z.AI coding-plan quota endpoint path, probed live
// against the provisioned-key API host (api.z.ai). The endpoint authenticates
// with the provisioned Z.AI API key ("{id}.{secret}") sent raw in the
// Authorization header — no "Bearer " prefix — and answers with the
// {"code","msg","success","data":{...}} envelope. zcode auth files carry no
// broker JWT, so the zcode.z.ai start-plan gateway is deliberately not used
// here; that origin requires desktop-app attestation this proxy cannot
// produce.
const zcodeQuotaPath = "/api/monitor/usage/quota/limit"

// zcodeQuotaHTTPTimeout bounds the quota probe.
const zcodeQuotaHTTPTimeout = 15 * time.Second

// zcodeQuotaLimit mirrors one entry of the upstream data.limits array.
type zcodeQuotaLimit struct {
	Type          string  `json:"type"`          // "TOKENS_LIMIT" | "TIME_LIMIT" | other (ignored)
	Unit          int     `json:"unit"`          // window selector: 3 = five-hour, 6 = weekly
	Percentage    float64 `json:"percentage"`    // used percentage
	NextResetTime float64 `json:"nextResetTime"` // epoch milliseconds; <= 0 means no reset
}

// zcodeQuotaWindow is a single normalized quota window.
type zcodeQuotaWindow struct {
	Name             string     `json:"name"`
	UsedPercent      float64    `json:"used_percent"`
	RemainingPercent float64    `json:"remaining_percent"`
	ResetAt          *time.Time `json:"reset_at,omitempty"`
}

// zcodeQuotaResponse is the normalized quota payload returned to the caller.
type zcodeQuotaResponse struct {
	AuthIndex string           `json:"auth_index,omitempty"`
	Email     string           `json:"email,omitempty"`
	Level     string           `json:"level"`
	FiveHour  zcodeQuotaWindow `json:"five_hour"`
	Weekly    zcodeQuotaWindow `json:"weekly"`
	MCP       zcodeQuotaWindow `json:"mcp"`
	Monthly   zcodeQuotaWindow `json:"monthly"`
}

// GetZcodeQuota reports the Z.AI coding-plan quota for a zcode credential.
//
// Endpoint:
//
//	GET /v0/management/zcode-quota
//
// Query Parameters (optional):
//   - auth_index: The credential "auth_index" from GET /v0/management/auth-files.
//     If omitted, uses the first available zcode credential.
//
// The quota is fetched from api.z.ai with the provisioned Z.AI API key,
// matching the gajae-code reference implementation
// (packages/ai/src/utils/oauth/glm-zcode.ts). The response is normalized into
// fixed quota windows: five-hour tokens, weekly tokens, MCP/time limit, and
// monthly (never populated for Z.AI).
func (h *Handler) GetZcodeQuota(c *gin.Context) {
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

	auth := h.resolveZcodeAuth(authIndex)
	if auth == nil {
		if authIndex != "" {
			c.JSON(http.StatusNotFound, gin.H{"error": "zcode credential not found"})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no zcode credential found"})
		}
		return
	}

	apiKey := zcodeQuotaAPIKey(auth)
	if apiKey == "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "zcode provisioned api key unavailable; re-login required"})
		return
	}

	quota, errFetch := h.fetchZcodeQuota(c.Request.Context(), auth, apiKey)
	if errFetch != nil {
		log.WithError(errFetch).Debug("zcode quota request failed")
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

// resolveZcodeAuth locates a zcode credential by auth index, or returns the
// first available zcode credential when authIndex is empty.
func (h *Handler) resolveZcodeAuth(authIndex string) *coreauth.Auth {
	if h == nil || h.authManager == nil {
		return nil
	}
	if authIndex != "" {
		auth := h.authByIndex(authIndex)
		if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "zcode") {
			return nil
		}
		return auth
	}
	var firstZcode *coreauth.Auth
	for _, auth := range h.authManager.List() {
		if auth == nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(auth.Provider), "zcode") {
			continue
		}
		if firstZcode == nil {
			firstZcode = auth
		}
	}
	return firstZcode
}

// zcodeQuotaAPIKey returns the provisioned Z.AI API key from the auth record,
// mirroring the executor's zcodeCreds resolution order: the in-memory
// Attributes["api_key"] first, then the persisted Metadata["access_token"]
// (both hold the same "{id}.{secret}" key; the latter is what survives an
// auth-file reload from disk).
func zcodeQuotaAPIKey(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if key := strings.TrimSpace(auth.Attributes["api_key"]); key != "" {
			return key
		}
	}
	if auth.Metadata != nil {
		if token, ok := auth.Metadata["access_token"].(string); ok {
			return strings.TrimSpace(token)
		}
	}
	return ""
}

// fetchZcodeQuota performs the authenticated GET against the Z.AI quota
// endpoint and returns the parsed, normalized quota.
func (h *Handler) fetchZcodeQuota(ctx context.Context, auth *coreauth.Auth, apiKey string) (*zcodeQuotaResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, zcodeQuotaHTTPTimeout)
	defer cancel()

	quotaURL := strings.TrimSuffix(zcode.DefaultZaiAPIBase, "/") + zcodeQuotaPath
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, quotaURL, nil)
	if errReq != nil {
		return nil, fmt.Errorf("zcode quota: build request: %w", errReq)
	}
	// The monitor API contract takes the key verbatim, without a Bearer prefix.
	req.Header.Set("Authorization", apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "CLIProxyAPI-Management")

	client := &http.Client{
		Transport: h.apiCallTransport(auth, ""),
		// The upstream contract rejects redirects; surface the 3xx instead.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("zcode quota request failed: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		bodyText, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("zcode quota upstream returned %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyText)))
	}

	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errRead != nil {
		return nil, fmt.Errorf("zcode quota: read response: %w", errRead)
	}
	// Note: Z.AI answers HTTP 200 with an error body (code/msg/success:false)
	// for auth failures, so the status code alone is not authoritative — the
	// body parse below treats a missing data payload as an error.
	return parseZcodeQuotaResponse(body)
}

// parseZcodeQuotaResponse decodes the upstream quota response and normalizes
// it into fixed quota windows. Window mapping follows the coding-plan schema:
//   - TOKENS_LIMIT unit 3 -> five-hour token window
//   - TOKENS_LIMIT unit 6 -> weekly token window
//   - TIME_LIMIT (any unit) -> MCP/time limit window
//
// Unknown type/unit entries are ignored; duplicate windows are last-wins.
// Percentages are clamped to [0, 100]; remaining = 100 - used. nextResetTime
// is epoch milliseconds; values <= 0 mean no reset (reset_at omitted).
func parseZcodeQuotaResponse(body []byte) (*zcodeQuotaResponse, error) {
	var envelope struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("zcode quota: decode response envelope: %w", err)
	}
	data := bytes.TrimSpace(envelope.Data)
	if len(data) == 0 || string(data) == "null" {
		if envelope.Msg != "" {
			return nil, fmt.Errorf("zcode quota request rejected: code %d: %s", envelope.Code, envelope.Msg)
		}
		return nil, fmt.Errorf("zcode quota response missing data")
	}

	var dataPayload struct {
		Level  string          `json:"level"`
		Limits json.RawMessage `json:"limits"`
	}
	if err := json.Unmarshal(data, &dataPayload); err != nil {
		return nil, fmt.Errorf("zcode quota: decode response data: %w", err)
	}
	limits := bytes.TrimSpace(dataPayload.Limits)
	if len(limits) == 0 || string(limits) == "null" {
		return nil, fmt.Errorf("zcode quota response missing data.limits")
	}
	var rawLimits []zcodeQuotaLimit
	if err := json.Unmarshal(limits, &rawLimits); err != nil {
		return nil, fmt.Errorf("zcode quota: decode data.limits: %w", err)
	}

	result := &zcodeQuotaResponse{
		Level:    dataPayload.Level,
		FiveHour: zcodeQuotaWindow{Name: "five_hour"},
		Weekly:   zcodeQuotaWindow{Name: "weekly"},
		MCP:      zcodeQuotaWindow{Name: "mcp"},
		Monthly:  zcodeQuotaWindow{Name: "monthly"},
	}
	for _, limit := range rawLimits {
		used := clampZcodePercent(limit.Percentage)
		window := zcodeQuotaWindow{
			UsedPercent:      used,
			RemainingPercent: 100 - used,
		}
		if limit.NextResetTime > 0 {
			resetAt := time.UnixMilli(int64(limit.NextResetTime)).UTC()
			window.ResetAt = &resetAt
		}
		switch {
		case limit.Type == "TOKENS_LIMIT" && limit.Unit == 3:
			window.Name = "five_hour"
			result.FiveHour = window
		case limit.Type == "TOKENS_LIMIT" && limit.Unit == 6:
			window.Name = "weekly"
			result.Weekly = window
		case limit.Type == "TIME_LIMIT":
			window.Name = "mcp"
			result.MCP = window
		default:
			// Unknown type/unit entries are silently ignored.
		}
	}
	return result, nil
}

// clampZcodePercent clamps a used percentage to [0, 100].
func clampZcodePercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}
