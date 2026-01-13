package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// TierInfo holds cached subscription tier information for an auth.
type TierInfo struct {
	IsPro     bool      `json:"is_pro"`
	TierID    string    `json:"tier_id"`
	TierName  string    `json:"tier_name"`
	FetchedAt time.Time `json:"fetched_at"`
}

// TierCache manages in-memory tier information with TTL-based expiration.
// This cache is NOT persisted to disk - tier info is fetched on demand.
type TierCache struct {
	mu    sync.RWMutex
	tiers map[string]*TierInfo
	ttl   time.Duration
}

// NewTierCache creates a new tier cache with the specified TTL.
func NewTierCache(ttl time.Duration) *TierCache {
	if ttl <= 0 {
		ttl = time.Hour // Default 1 hour TTL
	}
	return &TierCache{
		tiers: make(map[string]*TierInfo),
		ttl:   ttl,
	}
}

// Get retrieves tier info for an auth ID. Returns nil if not found or expired.
func (c *TierCache) Get(authID string) *TierInfo {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()

	info, ok := c.tiers[authID]
	if !ok {
		return nil
	}

	// Check TTL expiration
	if c.IsExpired(info) {
		return nil
	}

	// Return a copy
	return &TierInfo{
		IsPro:     info.IsPro,
		TierID:    info.TierID,
		TierName:  info.TierName,
		FetchedAt: info.FetchedAt,
	}
}

// Set stores tier info for an auth ID.
func (c *TierCache) Set(authID string, info *TierInfo) {
	if c == nil || authID == "" || info == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	// Store a copy
	c.tiers[authID] = &TierInfo{
		IsPro:     info.IsPro,
		TierID:    info.TierID,
		TierName:  info.TierName,
		FetchedAt: info.FetchedAt,
	}
}

// Delete removes tier info for an auth ID.
func (c *TierCache) Delete(authID string) {
	if c == nil || authID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.tiers, authID)
}

// IsExpired checks if the tier info has exceeded the TTL.
func (c *TierCache) IsExpired(info *TierInfo) bool {
	if c == nil || info == nil {
		return true
	}
	return time.Since(info.FetchedAt) > c.ttl
}

// Len returns the number of cached tier entries.
func (c *TierCache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.tiers)
}

// SubscriptionTier represents a subscription tier from the loadCodeAssist API.
type SubscriptionTier struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// SubscriptionInfo contains subscription information from the loadCodeAssist API.
type SubscriptionInfo struct {
	CurrentTier             *SubscriptionTier  `json:"currentTier,omitempty"`
	PaidTier                *SubscriptionTier  `json:"paidTier,omitempty"`
	AllowedTiers            []SubscriptionTier `json:"allowedTiers,omitempty"`
	CloudaicompanionProject string             `json:"cloudaicompanionProject,omitempty"`
	GcpManaged              bool               `json:"gcpManaged,omitempty"`
}

// IsPaidTier checks if the subscription info indicates a paid tier (pro or ultra).
func IsPaidTier(info *SubscriptionInfo) bool {
	if info == nil {
		return false
	}
	var effectiveTier *SubscriptionTier
	if info.PaidTier != nil {
		effectiveTier = info.PaidTier
	} else {
		effectiveTier = info.CurrentTier
	}
	if effectiveTier == nil || effectiveTier.ID == "" {
		return false
	}
	id := strings.ToLower(effectiveTier.ID)
	return strings.Contains(id, "pro") || strings.Contains(id, "ultra")
}

// Antigravity API constants for tier detection
const (
	antigravityTierAPIEndpoint    = "https://cloudcode-pa.googleapis.com"
	antigravityTierAPIVersion     = "v1internal"
	antigravityTierAPIUserAgent   = "google-api-nodejs-client/9.15.1"
	antigravityTierAPIClient      = "gl-node/22.17.0"
	antigravityTierClientMetadata = "ideType=IDE_UNSPECIFIED,platform=PLATFORM_UNSPECIFIED,pluginType=GEMINI"
)

// FetchAntigravitySubscriptionInfo retrieves subscription tier info from the loadCodeAssist API.
func FetchAntigravitySubscriptionInfo(ctx context.Context, accessToken string, httpClient *http.Client) (*SubscriptionInfo, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, fmt.Errorf("access token is empty")
	}

	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	loadReqBody := map[string]any{
		"metadata": map[string]string{
			"ideType":    "IDE_UNSPECIFIED",
			"platform":   "PLATFORM_UNSPECIFIED",
			"pluginType": "GEMINI",
		},
	}

	rawBody, errMarshal := json.Marshal(loadReqBody)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal request body: %w", errMarshal)
	}

	endpointURL := fmt.Sprintf("%s/%s:loadCodeAssist", antigravityTierAPIEndpoint, antigravityTierAPIVersion)
	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, strings.NewReader(string(rawBody)))
	if errReq != nil {
		return nil, fmt.Errorf("create request: %w", errReq)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", antigravityTierAPIUserAgent)
	req.Header.Set("X-Goog-Api-Client", antigravityTierAPIClient)
	req.Header.Set("Client-Metadata", antigravityTierClientMetadata)

	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("execute request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("antigravity subscription info: close body error: %v", errClose)
		}
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	var loadResp struct {
		SubscriptionInfo *SubscriptionInfo `json:"subscriptionInfo"`
	}
	if errDecode := json.NewDecoder(resp.Body).Decode(&loadResp); errDecode != nil {
		return nil, fmt.Errorf("decode response: %w", errDecode)
	}

	return loadResp.SubscriptionInfo, nil
}

// BuildTierInfoFromSubscription creates a TierInfo from SubscriptionInfo.
func BuildTierInfoFromSubscription(info *SubscriptionInfo) *TierInfo {
	if info == nil {
		return nil
	}

	tierInfo := &TierInfo{
		IsPro:     IsPaidTier(info),
		FetchedAt: time.Now(),
	}

	// Get tier ID and name from effective tier
	var effectiveTier *SubscriptionTier
	if info.PaidTier != nil {
		effectiveTier = info.PaidTier
	} else {
		effectiveTier = info.CurrentTier
	}

	if effectiveTier != nil {
		tierInfo.TierID = effectiveTier.ID
		tierInfo.TierName = effectiveTier.Name
	}

	return tierInfo
}
