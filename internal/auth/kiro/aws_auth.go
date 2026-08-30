// Package kiro provides OAuth2 authentication functionality for AWS CodeWhisperer (Kiro) API.
// This package implements token loading, refresh, and API communication with CodeWhisperer.
package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	pathListAvailableModels = "ListAvailableModels"

	BuilderIDRequestProfileARN = "arn:aws:codewhisperer:us-east-1:638616132270:profile/AAAACCCCXXXX"
)

// KiroAuth handles AWS CodeWhisperer authentication and API communication.
// It provides methods for loading tokens, refreshing expired tokens,
// and communicating with the CodeWhisperer API.
type KiroAuth struct {
	httpClient *http.Client
}

// NewKiroAuth creates a new Kiro authentication service.
// It initializes the HTTP client with proxy settings from the configuration.
//
// Parameters:
//   - cfg: The application configuration containing proxy settings
//
// Returns:
//   - *KiroAuth: A new Kiro authentication service instance
func NewKiroAuth(cfg *config.Config) *KiroAuth {
	return &KiroAuth{
		httpClient: util.SetProxy(&cfg.SDKConfig, &http.Client{Timeout: 120 * time.Second}),
	}
}

func newKiroUsageLimitsRequest(ctx context.Context, tokenData *KiroTokenData, requireEmail bool) (*http.Request, error) {
	if tokenData == nil {
		return nil, fmt.Errorf("kiro token data is required")
	}
	profileArn := strings.TrimSpace(tokenData.ProfileArn)
	if profileArn == "" && strings.EqualFold(strings.TrimSpace(tokenData.AuthMethod), "builder-id") {
		profileArn = BuilderIDRequestProfileARN
	}
	region := NormalizeKiroRegion(tokenData.APIRegion)
	if region == "" {
		region = ExtractRegionFromProfileArn(profileArn)
	}
	if region == "" {
		region = NormalizeKiroRegion(tokenData.Region)
	}
	if region == "" {
		region = "us-east-1"
	}
	params := map[string]string{
		"origin":       "AI_EDITOR",
		"profileArn":   profileArn,
		"resourceType": "AGENTIC_REQUEST",
	}
	if requireEmail && profileArn == "" {
		params["isEmailRequired"] = "true"
	}
	values := url.Values{}
	for key, value := range params {
		values.Set(key, value)
	}
	requestBody, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("failed to encode usage request: %w", err)
	}
	requestURL := fmt.Sprintf("https://management.%s.kiro.dev/?%s", region, values.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(requestBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create usage request: %w", err)
	}
	setRuntimeHeaders(req, tokenData.AccessToken, GetAccountKey(tokenData.ClientID, tokenData.RefreshToken))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Amz-Target", "AmazonCodeWhispererService.GetUsageLimits")
	return req, nil
}

// LoadTokenFromFile loads token data from a file path.
// This method reads and parses the token file, expanding ~ to the home directory.
//
// Parameters:
//   - tokenFile: Path to the token file (supports ~ expansion)
//
// Returns:
//   - *KiroTokenData: The parsed token data
//   - error: An error if file reading or parsing fails
func (k *KiroAuth) LoadTokenFromFile(tokenFile string) (*KiroTokenData, error) {
	// Expand ~ to home directory
	if strings.HasPrefix(tokenFile, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		tokenFile = filepath.Join(home, tokenFile[1:])
	}

	data, err := os.ReadFile(tokenFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read token file: %w", err)
	}

	var tokenData KiroTokenData
	if err := json.Unmarshal(data, &tokenData); err != nil {
		return nil, fmt.Errorf("failed to parse token file: %w", err)
	}

	return &tokenData, nil
}

// IsTokenExpired checks if the token has expired.
// This method parses the expiration timestamp and compares it with the current time.
//
// Parameters:
//   - tokenData: The token data to check
//
// Returns:
//   - bool: True if the token has expired, false otherwise
func (k *KiroAuth) IsTokenExpired(tokenData *KiroTokenData) bool {
	if tokenData.ExpiresAt == "" {
		return true
	}

	expiresAt, err := time.Parse(time.RFC3339, tokenData.ExpiresAt)
	if err != nil {
		// Try alternate format
		expiresAt, err = time.Parse("2006-01-02T15:04:05.000Z", tokenData.ExpiresAt)
		if err != nil {
			return true
		}
	}

	return time.Now().After(expiresAt)
}

// makeRequest sends a REST-style GET request to the CodeWhisperer API.
//
// Parameters:
//   - ctx: The context for the request
//   - path: The API path (e.g., "getUsageLimits")
//   - tokenData: The token data containing access token, refresh token, and profile ARN
//   - queryParams: Query parameters to add to the URL
//
// Returns:
//   - []byte: The response body
//   - error: An error if the request fails
func (k *KiroAuth) makeRequest(ctx context.Context, path string, tokenData *KiroTokenData, queryParams map[string]string) ([]byte, error) {
	// Get endpoint from profileArn (defaults to us-east-1 if empty)
	profileArn := queryParams["profileArn"]
	endpoint := GetKiroAPIEndpointFromProfileArn(profileArn)
	url := buildURL(endpoint, path, queryParams)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	accountKey := GetAccountKey(tokenData.ClientID, tokenData.RefreshToken)
	setRuntimeHeaders(req, tokenData.AccessToken, accountKey)

	resp, err := k.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("failed to close response body: %v", errClose)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	return body, nil
}

// GetUsageLimits retrieves usage information from the CodeWhisperer API.
// This method fetches the current usage statistics and subscription information.
//
// Parameters:
//   - ctx: The context for the request
//   - tokenData: The token data containing access token and profile ARN
//
// Returns:
//   - *KiroUsageInfo: The usage information
//   - error: An error if the request fails
func (k *KiroAuth) GetUsageLimits(ctx context.Context, tokenData *KiroTokenData) (*KiroUsageInfo, error) {
	req, err := newKiroUsageLimitsRequest(ctx, tokenData, false)
	if err != nil {
		return nil, err
	}
	resp, err := k.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("usage request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read usage response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("usage API error (status %d): %s", resp.StatusCode, string(body))
	}

	var result struct {
		SubscriptionInfo struct {
			SubscriptionTitle string `json:"subscriptionTitle"`
		} `json:"subscriptionInfo"`
		UsageBreakdownList []struct {
			CurrentUsageWithPrecision float64 `json:"currentUsageWithPrecision"`
			UsageLimitWithPrecision   float64 `json:"usageLimitWithPrecision"`
			ResourceType              string  `json:"resourceType"`
		} `json:"usageBreakdownList"`
		NextDateReset float64 `json:"nextDateReset"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse usage response: %w", err)
	}

	usage := &KiroUsageInfo{
		SubscriptionTitle: result.SubscriptionInfo.SubscriptionTitle,
		NextReset:         fmt.Sprintf("%v", result.NextDateReset),
	}

	for _, breakdown := range result.UsageBreakdownList {
		if breakdown.ResourceType != "" && breakdown.ResourceType != "AGENTIC_REQUEST" {
			continue
		}
		usage.CurrentUsage = breakdown.CurrentUsageWithPrecision
		usage.UsageLimit = breakdown.UsageLimitWithPrecision
		break
	}

	return usage, nil
}

// ListAvailableModels retrieves available models from the CodeWhisperer API.
// This method fetches the list of AI models available for the authenticated user.
//
// OmniRoute-aligned behavior:
//   - origin=AI_EDITOR is sent first (universal call for Builder ID and IdC).
//   - profileArn is added only on retry for desktop-style accounts that have one.
//     Sending profileArn for Builder ID can yield 403.
//   - The response body may use either "models" or "availableModels"; both are accepted.
//
// Parameters:
//   - ctx: The context for the request
//   - tokenData: The token data containing access token and profile ARN
//
// Returns:
//   - []*KiroModel: The list of available models
//   - error: An error if the request fails
func (k *KiroAuth) ListAvailableModels(ctx context.Context, tokenData *KiroTokenData) ([]*KiroModel, error) {
	baseParams := map[string]string{
		"origin": "AI_EDITOR",
	}

	body, err := k.makeRequest(ctx, pathListAvailableModels, tokenData, baseParams)
	if err != nil && tokenData != nil && tokenData.ProfileArn != "" {
		retryParams := map[string]string{
			"origin":     "AI_EDITOR",
			"profileArn": tokenData.ProfileArn,
		}
		body, err = k.makeRequest(ctx, pathListAvailableModels, tokenData, retryParams)
	}
	if err != nil {
		return nil, err
	}

	models := parseKiroAvailableModels(body)
	if len(models) == 0 {
		return nil, fmt.Errorf("kiro: ListAvailableModels returned no models")
	}
	return models, nil
}

type kiroAvailableModelRaw struct {
	ModelID        string  `json:"modelId"`
	ModelName      string  `json:"modelName"`
	Description    string  `json:"description"`
	RateMultiplier float64 `json:"rateMultiplier"`
	RateUnit       string  `json:"rateUnit"`
	TokenLimits    *struct {
		MaxInputTokens int `json:"maxInputTokens"`
	} `json:"tokenLimits"`
}

// parseKiroAvailableModels parses the CodeWhisperer ListAvailableModels response.
// Both "models" and "availableModels" payload keys are accepted; the first
// non-empty wins. Duplicate IDs are dropped.
func parseKiroAvailableModels(body []byte) []*KiroModel {
	var raw struct {
		Models          []kiroAvailableModelRaw `json:"models"`
		AvailableModels []kiroAvailableModelRaw `json:"availableModels"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil
	}

	items := raw.Models
	if len(items) == 0 {
		items = raw.AvailableModels
	}

	seen := make(map[string]struct{}, len(items))
	out := make([]*KiroModel, 0, len(items))
	for _, m := range items {
		if m.ModelID == "" {
			continue
		}
		if _, dup := seen[m.ModelID]; dup {
			continue
		}
		seen[m.ModelID] = struct{}{}
		maxInputTokens := 0
		if m.TokenLimits != nil {
			maxInputTokens = m.TokenLimits.MaxInputTokens
		}
		out = append(out, &KiroModel{
			ModelID:        m.ModelID,
			ModelName:      m.ModelName,
			Description:    m.Description,
			RateMultiplier: m.RateMultiplier,
			RateUnit:       m.RateUnit,
			MaxInputTokens: maxInputTokens,
		})
	}
	return out
}

// CreateTokenStorage creates a new KiroTokenStorage from token data.
// This method converts the token data into a storage structure suitable for persistence.
//
// Parameters:
//   - tokenData: The token data to convert
//
// Returns:
//   - *KiroTokenStorage: A new token storage instance
func (k *KiroAuth) CreateTokenStorage(tokenData *KiroTokenData) *KiroTokenStorage {
	return &KiroTokenStorage{
		AccessToken:  tokenData.AccessToken,
		RefreshToken: tokenData.RefreshToken,
		ProfileArn:   tokenData.ProfileArn,
		ExpiresAt:    tokenData.ExpiresAt,
		AuthMethod:   tokenData.AuthMethod,
		Provider:     tokenData.Provider,
		LastRefresh:  time.Now().Format(time.RFC3339),
		ClientID:     tokenData.ClientID,
		ClientSecret: tokenData.ClientSecret,
		Region:       tokenData.Region,
		StartURL:     tokenData.StartURL,
		Email:        tokenData.Email,
	}
}

// ValidateToken checks if the token is valid by making a test API call.
// This method verifies the token by attempting to fetch usage limits.
//
// Parameters:
//   - ctx: The context for the request
//   - tokenData: The token data to validate
//
// Returns:
//   - error: An error if the token is invalid
func (k *KiroAuth) ValidateToken(ctx context.Context, tokenData *KiroTokenData) error {
	_, err := k.GetUsageLimits(ctx, tokenData)
	return err
}

// UpdateTokenStorage updates an existing token storage with new token data.
// This method refreshes the token storage with newly obtained access and refresh tokens.
//
// Parameters:
//   - storage: The existing token storage to update
//   - tokenData: The new token data to apply
func (k *KiroAuth) UpdateTokenStorage(storage *KiroTokenStorage, tokenData *KiroTokenData) {
	storage.AccessToken = tokenData.AccessToken
	storage.RefreshToken = tokenData.RefreshToken
	storage.ProfileArn = tokenData.ProfileArn
	storage.ExpiresAt = tokenData.ExpiresAt
	storage.AuthMethod = tokenData.AuthMethod
	storage.Provider = tokenData.Provider
	storage.LastRefresh = time.Now().Format(time.RFC3339)
	if tokenData.ClientID != "" {
		storage.ClientID = tokenData.ClientID
	}
	if tokenData.ClientSecret != "" {
		storage.ClientSecret = tokenData.ClientSecret
	}
	if tokenData.Region != "" {
		storage.Region = tokenData.Region
	}
	if tokenData.StartURL != "" {
		storage.StartURL = tokenData.StartURL
	}
	if tokenData.Email != "" {
		storage.Email = tokenData.Email
	}
}
