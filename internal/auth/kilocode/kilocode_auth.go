// Package kilocode provides authentication and token management for Kilocode API.
// It handles the device flow for secure authentication with the Kilocode API.
package kilocode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	// kilocodeAPIBaseURL is the base URL for Kilocode API.
	kilocodeAPIBaseURL = "https://api.kilo.ai"
	// kilocodeDeviceCodeURL is the endpoint for requesting device codes.
	kilocodeDeviceCodeURL = "https://api.kilo.ai/api/device-auth/codes"
	// kilocodeVerifyURL is the URL where users verify their device codes.
	kilocodeVerifyURL = "https://kilo.ai/device/verify"
	// defaultPollInterval is the default interval for polling token endpoint.
	defaultPollInterval = 3 * time.Second
	// maxPollDuration is the maximum time to wait for user authorization.
	maxPollDuration = 15 * time.Minute
)

// DeviceCodeResponse represents Kilocode's device code response.
type DeviceCodeResponse struct {
	// Code is the device verification code.
	Code string `json:"code"`
	// VerificationURL is the URL where the user should enter the code.
	VerificationURL string `json:"verificationUrl"`
	// ExpiresIn is the number of seconds until the device code expires.
	ExpiresIn int `json:"expiresIn"`
}

// PollResponse represents the polling response from Kilocode.
type PollResponse struct {
	// Status indicates the current status: pending, approved, denied, expired.
	Status string `json:"status"`
	// Token is the access token (only present when status is "approved").
	Token string `json:"token,omitempty"`
	// UserID is the user ID (only present when status is "approved").
	UserID string `json:"userId,omitempty"`
	// UserEmail is the user email (only present when status is "approved").
	UserEmail string `json:"userEmail,omitempty"`
}

// DeviceFlowClient handles the device flow for Kilocode.
type DeviceFlowClient struct {
	httpClient *http.Client
	cfg        *config.Config
}

// NewDeviceFlowClient creates a new device flow client.
func NewDeviceFlowClient(cfg *config.Config) *DeviceFlowClient {
	client := &http.Client{Timeout: 30 * time.Second}
	if cfg != nil {
		client = util.SetProxy(&cfg.SDKConfig, client)
	}
	return &DeviceFlowClient{
		httpClient: client,
		cfg:        cfg,
	}
}

// RequestDeviceCode initiates the device flow by requesting a device code from Kilocode.
func (c *DeviceFlowClient) RequestDeviceCode(ctx context.Context) (*DeviceCodeResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, kilocodeDeviceCodeURL, nil)
	if err != nil {
		return nil, NewAuthenticationError(ErrDeviceCodeFailed, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, NewAuthenticationError(ErrDeviceCodeFailed, err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("kilocode device code: close body error: %v", errClose)
		}
	}()

	if !isHTTPSuccess(resp.StatusCode) {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, NewAuthenticationError(ErrDeviceCodeFailed, fmt.Errorf("status %d: %s", resp.StatusCode, string(bodyBytes)))
	}

	var deviceCode DeviceCodeResponse
	if err = json.NewDecoder(resp.Body).Decode(&deviceCode); err != nil {
		return nil, NewAuthenticationError(ErrDeviceCodeFailed, err)
	}

	return &deviceCode, nil
}

// PollForToken polls the token endpoint until the user authorizes or the device code expires.
func (c *DeviceFlowClient) PollForToken(ctx context.Context, code string) (*PollResponse, error) {
	if code == "" {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, fmt.Errorf("device code is empty"))
	}

	pollURL := fmt.Sprintf("%s/%s", kilocodeDeviceCodeURL, url.PathEscape(code))
	deadline := time.Now().Add(maxPollDuration)

	ticker := time.NewTicker(defaultPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, NewAuthenticationError(ErrPollingTimeout, ctx.Err())
		case <-ticker.C:
			if time.Now().After(deadline) {
				return nil, ErrPollingTimeout
			}

			pollResp, err := c.pollDeviceCode(ctx, pollURL)
			if err != nil {
				return nil, err
			}

			switch pollResp.Status {
			case "pending":
				// Continue polling
				continue
			case "approved":
				// Success - return the response
				return pollResp, nil
			case "denied":
				return nil, ErrAccessDenied
			case "expired":
				return nil, ErrDeviceCodeExpired
			default:
				return nil, NewAuthenticationError(ErrTokenExchangeFailed,
					fmt.Errorf("unknown status: %s", pollResp.Status))
			}
		}
	}
}

// pollDeviceCode makes a single polling request to check the device code status.
func (c *DeviceFlowClient) pollDeviceCode(ctx context.Context, pollURL string) (*PollResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
	if err != nil {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("kilocode token poll: close body error: %v", errClose)
		}
	}()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, err)
	}

	// Handle different HTTP status codes
	switch resp.StatusCode {
	case http.StatusOK:
		// Success - parse the response
		var pollResp PollResponse
		if err = json.Unmarshal(bodyBytes, &pollResp); err != nil {
			return nil, NewAuthenticationError(ErrTokenExchangeFailed, err)
		}
		return &pollResp, nil
	case http.StatusAccepted:
		// Still pending
		return &PollResponse{Status: "pending"}, nil
	case http.StatusForbidden:
		// Access denied
		return &PollResponse{Status: "denied"}, nil
	case http.StatusGone:
		// Code expired
		return &PollResponse{Status: "expired"}, nil
	default:
		return nil, NewAuthenticationError(ErrTokenExchangeFailed,
			fmt.Errorf("status %d: %s", resp.StatusCode, string(bodyBytes)))
	}
}

// KilocodeAuth handles Kilocode authentication flow.
// It provides methods for device flow authentication and token management.
type KilocodeAuth struct {
	httpClient   *http.Client
	deviceClient *DeviceFlowClient
	cfg          *config.Config
}

// NewKilocodeAuth creates a new KilocodeAuth service instance.
// It initializes an HTTP client with proxy settings from the provided configuration.
func NewKilocodeAuth(cfg *config.Config) *KilocodeAuth {
	return &KilocodeAuth{
		httpClient:   util.SetProxy(&cfg.SDKConfig, &http.Client{Timeout: 30 * time.Second}),
		deviceClient: NewDeviceFlowClient(cfg),
		cfg:          cfg,
	}
}

// StartDeviceFlow initiates the device flow authentication.
// Returns the device code response containing the user code and verification URI.
func (k *KilocodeAuth) StartDeviceFlow(ctx context.Context) (*DeviceCodeResponse, error) {
	return k.deviceClient.RequestDeviceCode(ctx)
}

// WaitForAuthorization polls for user authorization and returns the auth bundle.
func (k *KilocodeAuth) WaitForAuthorization(ctx context.Context, deviceCode *DeviceCodeResponse) (*KilocodeAuthBundle, error) {
	if deviceCode == nil {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, fmt.Errorf("device code is nil"))
	}

	pollResp, err := k.deviceClient.PollForToken(ctx, deviceCode.Code)
	if err != nil {
		return nil, err
	}

	if pollResp.Status != "approved" {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed,
			fmt.Errorf("unexpected status: %s", pollResp.Status))
	}

	if pollResp.Token == "" {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, fmt.Errorf("empty token in response"))
	}

	return &KilocodeAuthBundle{
		Token:     pollResp.Token,
		UserID:    pollResp.UserID,
		UserEmail: pollResp.UserEmail,
	}, nil
}

// GetAPIEndpoint returns the Kilocode API endpoint URL for OpenRouter compatibility.
func (k *KilocodeAuth) GetAPIEndpoint() string {
	return "https://kilo.ai/api/openrouter"
}

// ValidateToken checks if a Kilocode access token is valid.
// Since Kilocode API only supports /chat/completions, we skip validation here.
// Token validity will be verified during actual API requests.
func (k *KilocodeAuth) ValidateToken(ctx context.Context, token string) (bool, error) {
	if token == "" {
		return false, nil
	}

	// Kilocode API only supports /chat/completions endpoint
	// We assume token is valid if it's not empty; actual validation happens during requests
	return true, nil
}

// CreateTokenStorage creates a new KilocodeTokenStorage from auth bundle.
func (k *KilocodeAuth) CreateTokenStorage(bundle *KilocodeAuthBundle) *KilocodeTokenStorage {
	return &KilocodeTokenStorage{
		Token:     bundle.Token,
		UserID:    bundle.UserID,
		UserEmail: bundle.UserEmail,
		Type:      "kilocode",
	}
}

// LoadAndValidateToken loads a token from storage and validates it.
// Returns true if valid, false if invalid or expired.
func (k *KilocodeAuth) LoadAndValidateToken(ctx context.Context, storage *KilocodeTokenStorage) (bool, error) {
	if storage == nil || storage.Token == "" {
		return false, fmt.Errorf("no token available")
	}

	// Mask token for logging
	maskedToken := maskToken(storage.Token)
	log.Debugf("kilocode: validating token %s for user %s", maskedToken, storage.UserID)

	valid, err := k.ValidateToken(ctx, storage.Token)
	if err != nil {
		log.Debugf("kilocode: token validation failed for %s: %v", maskedToken, err)
		return false, err
	}

	if !valid {
		log.Debugf("kilocode: token %s is invalid", maskedToken)
		return false, fmt.Errorf("token is invalid")
	}

	log.Debugf("kilocode: token %s is valid", maskedToken)
	return true, nil
}

// isHTTPSuccess checks if the status code indicates success (2xx).
func isHTTPSuccess(statusCode int) bool {
	return statusCode >= 200 && statusCode < 300
}

// FetchModels retrieves available models from the Kilocode API and filters for free models.
// This method fetches the list of AI models available from Kilocode and returns only
// those that are free (pricing.prompt == "0" && pricing.completion == "0").
//
// Parameters:
//   - ctx: The context for the request
//   - token: The access token for authentication
//
// Returns:
//   - []*registry.ModelInfo: The list of available free models converted to internal format
//   - error: An error if the request fails
func (k *KilocodeAuth) FetchModels(ctx context.Context, token string) ([]*registry.ModelInfo, error) {
	if token == "" {
		return nil, fmt.Errorf("kilocode: access token is required")
	}

	// Make request to Kilocode models endpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.GetAPIEndpoint()+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("kilocode: failed to create models request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := k.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kilocode: failed to fetch models: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("kilocode fetch models: close body error: %v", errClose)
		}
	}()

	if !isHTTPSuccess(resp.StatusCode) {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("kilocode: models API returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	// Parse the API response
	var apiResponse registry.KilocodeAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResponse); err != nil {
		return nil, fmt.Errorf("kilocode: failed to parse models response: %w", err)
	}

	// Convert API models to internal format (filters for free models automatically)
	models := registry.ConvertKilocodeAPIModels(apiResponse.Data)

	maskedToken := maskToken(token)
	log.Debugf("kilocode: fetched %d free models with token %s", len(models), maskedToken)

	return models, nil
}

// maskToken masks a token for safe logging by showing only first and last few characters.
func maskToken(token string) string {
	if len(token) <= 8 {
		return "***"
	}
	return token[:4] + "***" + token[len(token)-4:]
}
