package meta

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
)

// MetaAuth performs the Meta Model API subscription device-code login and key mint.
type MetaAuth struct {
	httpClient      *http.Client
	minPollInterval time.Duration
}

// metaProviderName is the config identifier for the Meta Model API provider.
const metaProviderName = "meta"

// MetaModelName is the default Muse Spark model registered with the meta provider.
const MetaModelName = "muse-spark-1.3"

// metaMaxContextLength is Muse Spark's context window in tokens.
const metaMaxContextLength = 1048576

// StoreAPIKey adds the minted key to the meta openai-compatibility provider in cfg,
// creating the provider (with the Muse Spark model) when absent. It reports
// whether cfg was modified.
func StoreAPIKey(cfg *config.Config, apiKey string) bool {
	if cfg == nil {
		return false
	}
	for i := range cfg.OpenAICompatibility {
		entry := &cfg.OpenAICompatibility[i]
		if !strings.EqualFold(strings.TrimSpace(entry.Name), metaProviderName) {
			continue
		}
		for _, keyEntry := range entry.APIKeyEntries {
			if strings.TrimSpace(keyEntry.APIKey) == apiKey {
				return false
			}
		}
		entry.APIKeyEntries = append(entry.APIKeyEntries, config.OpenAICompatibilityAPIKey{APIKey: apiKey})
		return true
	}

	cfg.OpenAICompatibility = append(cfg.OpenAICompatibility, config.OpenAICompatibility{
		Name:    metaProviderName,
		BaseURL: APIBaseURL,
		APIKeyEntries: []config.OpenAICompatibilityAPIKey{
			{APIKey: apiKey},
		},
		Models: []config.OpenAICompatibilityModel{
			{
				Name:             MetaModelName,
				Alias:            MetaModelName,
				DisplayName:      "Muse Spark 1.3",
				MaxContextLength: metaMaxContextLength,
				InputModalities:  []string{"text", "image", "pdf", "video"},
				OutputModalities: []string{"text"},
			},
		},
	})
	return true
}

// NewMetaAuth creates a Meta OAuth helper using config proxy settings.
func NewMetaAuth(cfg *config.Config) *MetaAuth {
	return NewMetaAuthWithProxyURL(cfg, "")
}

// NewMetaAuthWithProxyURL creates a Meta OAuth helper with an explicit proxy URL.
func NewMetaAuthWithProxyURL(cfg *config.Config, proxyURL string) *MetaAuth {
	effectiveProxyURL := strings.TrimSpace(proxyURL)
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		if effectiveProxyURL == "" {
			effectiveProxyURL = strings.TrimSpace(cfg.ProxyURL)
		}
	}
	sdkCfg.ProxyURL = effectiveProxyURL
	return &MetaAuth{httpClient: util.SetProxy(&sdkCfg, &http.Client{Timeout: httpClientTimeout})}
}

// StartDeviceFlow requests a device code from Meta.
func (a *MetaAuth) StartDeviceFlow(ctx context.Context) (*DeviceCodeResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	form := url.Values{
		"client_id": {ClientID},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, AuthURL+AuthorizationEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("meta device code: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("meta device code request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("meta device code: close response body error: %v", errClose)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("meta device code: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("meta device code request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var deviceCode DeviceCodeResponse
	if err = json.Unmarshal(body, &deviceCode); err != nil {
		return nil, fmt.Errorf("meta device code: parse response: %w", err)
	}
	if strings.TrimSpace(deviceCode.DeviceCode) == "" {
		return nil, fmt.Errorf("meta device code: response missing device_code")
	}
	if strings.TrimSpace(deviceCode.UserCode) == "" {
		return nil, fmt.Errorf("meta device code: response missing user_code")
	}
	if strings.TrimSpace(deviceCode.VerificationURI) == "" && strings.TrimSpace(deviceCode.VerificationURIComplete) == "" {
		return nil, fmt.Errorf("meta device code: response missing verification URI")
	}
	return &deviceCode, nil
}

// WaitForAuthorization polls until the user authorizes the device code and returns the access token.
func (a *MetaAuth) WaitForAuthorization(ctx context.Context, deviceCode *DeviceCodeResponse) (string, error) {
	if deviceCode == nil {
		return "", fmt.Errorf("meta device code: response is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	minInterval := defaultPollInterval
	if a != nil && a.minPollInterval > 0 {
		minInterval = a.minPollInterval
	}

	interval := time.Duration(deviceCode.Interval) * time.Second
	if a != nil && a.minPollInterval > 0 && deviceCode.Interval <= 0 {
		interval = a.minPollInterval
	} else if interval < minInterval {
		interval = minInterval
	}

	deadline := time.Now().Add(MaxPollDuration)
	if deviceCode.ExpiresIn > 0 {
		codeDeadline := time.Now().Add(time.Duration(deviceCode.ExpiresIn) * time.Second)
		if codeDeadline.Before(deadline) {
			deadline = codeDeadline
		}
	}

	firstAttempt := true
	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("meta device code: context cancelled: %w", ctx.Err())
		case <-timer.C:
			if !firstAttempt && time.Now().After(deadline) {
				return "", fmt.Errorf("meta device code expired")
			}
			firstAttempt = false

			token, pollErr, nextInterval, shouldContinue := a.exchangeDeviceCode(ctx, deviceCode.DeviceCode, interval)
			if token != "" {
				return token, nil
			}
			if !shouldContinue {
				return "", pollErr
			}
			interval = nextInterval
			timer.Reset(interval)
		}
	}
}

// exchangeDeviceCode attempts to exchange a device code for an access token.
// Returns (token, error, nextInterval, shouldContinue).
func (a *MetaAuth) exchangeDeviceCode(ctx context.Context, deviceCode string, interval time.Duration) (string, error, time.Duration, bool) {
	form := url.Values{
		"grant_type":  {DeviceCodeGrantType},
		"device_code": {strings.TrimSpace(deviceCode)},
		"client_id":   {ClientID},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, AuthURL+TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("meta device token: create request: %w", err), interval, false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("meta device token request failed: %w", err), interval, false
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("meta device token: close response body error: %v", errClose)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("meta device token: read response: %w", err), interval, false
	}

	var payload struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		AccessToken      string `json:"access_token"`
		ExpiresIn        int    `json:"expires_in"`
		Interval         int    `json:"interval"`
	}
	if err = json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("meta device token: parse response: %w", err), interval, false
	}

	if payload.Error != "" {
		switch payload.Error {
		case "authorization_pending":
			return "", nil, interval, true
		case "slow_down":
			nextInterval := interval + defaultPollInterval
			if payload.Interval > 0 {
				nextInterval = time.Duration(payload.Interval) * time.Second
			}
			if nextInterval < minPollInterval {
				nextInterval = minPollInterval
			}
			return "", nil, nextInterval, true
		case "expired_token":
			return "", fmt.Errorf("meta device code expired"), interval, false
		case "access_denied":
			return "", fmt.Errorf("meta device authorization denied"), interval, false
		default:
			desc := strings.TrimSpace(payload.ErrorDescription)
			if desc != "" {
				return "", fmt.Errorf("meta device token error: %s: %s", payload.Error, desc), interval, false
			}
			return "", fmt.Errorf("meta device token error: %s", payload.Error), interval, false
		}
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("meta device token request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body))), interval, false
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return "", fmt.Errorf("meta device token response missing access_token"), interval, false
	}
	return strings.TrimSpace(payload.AccessToken), nil, interval, false
}

// MintAPIKey exchanges a subscription access token for a Model API key.
func (a *MetaAuth) MintAPIKey(ctx context.Context, accessToken string) (string, error) {
	if strings.TrimSpace(accessToken) == "" {
		return "", fmt.Errorf("meta key mint: access token is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, MintURL, nil)
	if err != nil {
		return "", fmt.Errorf("meta key mint: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("meta key mint request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("meta key mint: close response body error: %v", errClose)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("meta key mint: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("meta key mint request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var minted MintedKey
	if err = json.Unmarshal(body, &minted); err != nil {
		return "", fmt.Errorf("meta key mint: parse response: %w", err)
	}
	if strings.TrimSpace(minted.APIKey) == "" {
		return "", fmt.Errorf("meta key mint response missing api_key")
	}
	return strings.TrimSpace(minted.APIKey), nil
}
