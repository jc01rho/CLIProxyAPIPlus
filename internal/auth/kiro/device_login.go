package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

// Device-login contracts match kiro-lb (minpeter/kiro-lb device_login.py):
//
//   - Social (Google / GitHub) on prod.us-east-1.auth.desktop.kiro.dev.
//     Pending is HTTP 200 with status=authorization_pending; timings are milliseconds.
//     The token carries profileArn and routes to runtime.kiro.dev.
//   - Builder ID on oidc.{region}.amazonaws.com. Pending is HTTP 400 with
//     error=authorization_pending; timings are seconds. The client registration
//     is stored with the refresh token. Builder ID has no profile.
const (
	kiroDeviceAuthHost     = "https://prod.us-east-1.auth.desktop.kiro.dev"
	kiroDeviceClientID     = "kiro-cli"
	kiroDeviceSocialRegion = "us-east-1"

	kiroDeviceBuilderIDStartURL    = "https://view.awsapps.com/start"
	kiroDeviceBuilderIDRegion      = "us-east-1"
	kiroDeviceBuilderIDClientName  = "kiro-cli"
	kiroDeviceDefaultSocialTimeout = 5 * time.Minute
)

var (
	// ErrDeviceAuthorizationPending means the operator has not approved yet.
	ErrDeviceAuthorizationPending = errors.New("authorization_pending")
	// ErrDeviceSlowDown means the poller should increase its interval.
	ErrDeviceSlowDown = errors.New("slow_down")
	// ErrDeviceExpired means the approval window closed.
	ErrDeviceExpired = errors.New("expired_token")
	// ErrDeviceDenied means the operator rejected the login.
	ErrDeviceDenied = errors.New("access_denied")
)

// DeviceLoginKind is a kiro-lb login provider.
type DeviceLoginKind string

const (
	DeviceLoginGoogle    DeviceLoginKind = "Google"
	DeviceLoginGitHub    DeviceLoginKind = "Github"
	DeviceLoginBuilderID DeviceLoginKind = "BuilderId"
)

// DeviceLoginFlow is the operator-facing device authorization session.
type DeviceLoginFlow struct {
	Kind                    DeviceLoginKind
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	ExpiresIn               time.Duration
	Interval                time.Duration
	ClientID                string
	ClientSecret            string
	Region                  string
}

// DeviceLoginToken is the approved credential document.
type DeviceLoginToken struct {
	AccessToken      string
	RefreshToken     string
	ProfileArn       string
	IdentityProvider string
	ExpiresIn        int
	ClientID         string
	ClientSecret     string
	Region           string
	StartURL         string
	AuthMethod       string
	Provider         string
}

// DeviceLoginClient talks to Kiro social device endpoints and AWS SSO OIDC.
type DeviceLoginClient struct {
	httpClient *http.Client
	cfg        *config.Config
	authHost   string
}

// NewDeviceLoginClient constructs a device-login client.
func NewDeviceLoginClient(cfg *config.Config) *DeviceLoginClient {
	client := &http.Client{Timeout: 30 * time.Second}
	if cfg != nil {
		client = util.SetProxy(&cfg.SDKConfig, client)
	}
	return &DeviceLoginClient{
		httpClient: client,
		cfg:        cfg,
		authHost:   kiroDeviceAuthHost,
	}
}

// ParseDeviceLoginKind maps UI / query values onto kiro-lb providers.
func ParseDeviceLoginKind(raw string) (DeviceLoginKind, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "builder-id", "builderid", "builder_id", "aws", "aws-builder-id":
		return DeviceLoginBuilderID, nil
	case "google":
		return DeviceLoginGoogle, nil
	case "github", "github.com":
		return DeviceLoginGitHub, nil
	default:
		return "", fmt.Errorf("kiro device login provider must be builder-id, google, or github")
	}
}

// Start begins a device-authorization flow.
func (c *DeviceLoginClient) Start(ctx context.Context, kind DeviceLoginKind) (*DeviceLoginFlow, error) {
	if c == nil {
		return nil, fmt.Errorf("kiro device login client is nil")
	}
	if kind == DeviceLoginBuilderID {
		return c.startBuilderID(ctx)
	}
	return c.startSocial(ctx, kind)
}

// Poll asks upstream once whether the operator has approved.
func (c *DeviceLoginClient) Poll(ctx context.Context, flow *DeviceLoginFlow) (*DeviceLoginToken, error) {
	if c == nil {
		return nil, fmt.Errorf("kiro device login client is nil")
	}
	if flow == nil {
		return nil, fmt.Errorf("kiro device login flow is nil")
	}
	if flow.Kind == DeviceLoginBuilderID {
		return c.pollBuilderID(ctx, flow)
	}
	return c.pollSocial(ctx, flow)
}

// Wait polls at the advertised interval until approved, denied, expired, or ctx ends.
func (c *DeviceLoginClient) Wait(ctx context.Context, flow *DeviceLoginFlow) (*DeviceLoginToken, error) {
	if flow == nil {
		return nil, fmt.Errorf("kiro device login flow is nil")
	}
	interval := flow.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(flow.ExpiresIn)
	if flow.ExpiresIn <= 0 {
		deadline = time.Now().Add(kiroDeviceDefaultSocialTimeout)
	}
	for {
		if time.Now().After(deadline) {
			return nil, ErrDeviceExpired
		}
		token, err := c.Poll(ctx, flow)
		if err == nil {
			return token, nil
		}
		if errors.Is(err, ErrDeviceAuthorizationPending) {
			if errWait := sleepContext(ctx, interval); errWait != nil {
				return nil, errWait
			}
			continue
		}
		if errors.Is(err, ErrDeviceSlowDown) {
			interval += 5 * time.Second
			if errWait := sleepContext(ctx, interval); errWait != nil {
				return nil, errWait
			}
			continue
		}
		return nil, err
	}
}

// ToTokenData converts an approved device login into the persistent Kiro token shape.
func (t *DeviceLoginToken) ToTokenData() *KiroTokenData {
	if t == nil {
		return nil
	}
	expiresIn := t.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	email := ExtractEmailFromJWT(t.AccessToken)
	return &KiroTokenData{
		AccessToken:  t.AccessToken,
		RefreshToken: t.RefreshToken,
		ProfileArn:   t.ProfileArn,
		ExpiresAt:    time.Now().Add(time.Duration(expiresIn) * time.Second).Format(time.RFC3339),
		AuthMethod:   t.AuthMethod,
		Provider:     t.Provider,
		ClientID:     t.ClientID,
		ClientSecret: t.ClientSecret,
		Region:       t.Region,
		StartURL:     t.StartURL,
		Email:        email,
	}
}

func (c *DeviceLoginClient) startSocial(ctx context.Context, kind DeviceLoginKind) (*DeviceLoginFlow, error) {
	payload, err := c.postJSON(ctx, c.socialAuthHost()+"/oauth/device/authorization", map[string]any{
		"clientId":      kiroDeviceClientID,
		"loginProvider": string(kind),
	})
	if err != nil {
		return nil, err
	}
	expiresMS := jsonNumber(payload["expiresInMilliseconds"], 300000)
	intervalMS := jsonNumber(payload["intervalInMilliseconds"], 5000)
	deviceCode := strings.TrimSpace(jsonString(payload["deviceCode"]))
	if deviceCode == "" {
		return nil, fmt.Errorf("kiro device authorization returned no device code")
	}
	verificationComplete := strings.TrimSpace(jsonString(payload["verificationUriComplete"]))
	verificationURI := strings.TrimSpace(jsonString(payload["verificationUri"]))
	if verificationComplete == "" {
		verificationComplete = verificationURI
	}
	if verificationComplete == "" {
		return nil, fmt.Errorf("kiro device authorization returned no verification URL")
	}
	return &DeviceLoginFlow{
		Kind:                    kind,
		DeviceCode:              deviceCode,
		UserCode:                strings.TrimSpace(jsonString(payload["userCode"])),
		VerificationURI:         verificationURI,
		VerificationURIComplete: verificationComplete,
		ExpiresIn:               time.Duration(expiresMS) * time.Millisecond,
		Interval:                time.Duration(intervalMS) * time.Millisecond,
		Region:                  kiroDeviceSocialRegion,
	}, nil
}

func (c *DeviceLoginClient) pollSocial(ctx context.Context, flow *DeviceLoginFlow) (*DeviceLoginToken, error) {
	payload, err := c.postJSON(ctx, c.socialAuthHost()+"/oauth/device/poll", map[string]any{
		"clientId":   kiroDeviceClientID,
		"deviceCode": flow.DeviceCode,
	})
	if err != nil {
		return nil, err
	}
	accessToken := strings.TrimSpace(jsonString(payload["accessToken"]))
	if accessToken != "" {
		provider := string(flow.Kind)
		if idp := strings.TrimSpace(jsonString(payload["identityProvider"])); idp != "" {
			provider = idp
		}
		return &DeviceLoginToken{
			AccessToken:      accessToken,
			RefreshToken:     strings.TrimSpace(jsonString(payload["refreshToken"])),
			ProfileArn:       strings.TrimSpace(jsonString(payload["profileArn"])),
			IdentityProvider: strings.TrimSpace(jsonString(payload["identityProvider"])),
			ExpiresIn:        jsonNumber(payload["expiresIn"], 3600),
			Region:           kiroDeviceSocialRegion,
			AuthMethod:       "social",
			Provider:         provider,
		}, nil
	}
	status := strings.TrimSpace(jsonString(payload["status"]))
	if status == "" || status == "authorization_pending" {
		return nil, ErrDeviceAuthorizationPending
	}
	if strings.Contains(status, "expired") {
		return nil, fmt.Errorf("%w: %s", ErrDeviceExpired, status)
	}
	if strings.Contains(status, "denied") || strings.Contains(status, "access_denied") {
		return nil, fmt.Errorf("%w: %s", ErrDeviceDenied, status)
	}
	return nil, fmt.Errorf("device authorization %s", status)
}

func (c *DeviceLoginClient) startBuilderID(ctx context.Context) (*DeviceLoginFlow, error) {
	registration, err := c.oidcCall(ctx, kiroDeviceBuilderIDRegion, "/client/register", map[string]any{
		"clientName": kiroDeviceBuilderIDClientName,
		"clientType": "public",
		"scopes": []string{
			"codewhisperer:completions",
			"codewhisperer:analysis",
			"codewhisperer:conversations",
		},
	})
	if err != nil {
		return nil, err
	}
	clientID := strings.TrimSpace(jsonString(registration["clientId"]))
	clientSecret := strings.TrimSpace(jsonString(registration["clientSecret"]))
	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("builder id client registration returned no credentials")
	}
	authorization, err := c.oidcCall(ctx, kiroDeviceBuilderIDRegion, "/device_authorization", map[string]any{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"startUrl":     kiroDeviceBuilderIDStartURL,
	})
	if err != nil {
		return nil, err
	}
	deviceCode := strings.TrimSpace(jsonString(authorization["deviceCode"]))
	if deviceCode == "" {
		return nil, fmt.Errorf("builder id device authorization returned no device code")
	}
	verificationComplete := strings.TrimSpace(jsonString(authorization["verificationUriComplete"]))
	verificationURI := strings.TrimSpace(jsonString(authorization["verificationUri"]))
	if verificationComplete == "" {
		verificationComplete = verificationURI
	}
	if verificationComplete == "" {
		return nil, fmt.Errorf("builder id device authorization returned no verification URL")
	}
	expiresIn := jsonNumber(authorization["expiresIn"], 600)
	interval := jsonNumber(authorization["interval"], 5)
	return &DeviceLoginFlow{
		Kind:                    DeviceLoginBuilderID,
		DeviceCode:              deviceCode,
		UserCode:                strings.TrimSpace(jsonString(authorization["userCode"])),
		VerificationURI:         verificationURI,
		VerificationURIComplete: verificationComplete,
		ExpiresIn:               time.Duration(expiresIn) * time.Second,
		Interval:                time.Duration(interval) * time.Second,
		ClientID:                clientID,
		ClientSecret:            clientSecret,
		Region:                  kiroDeviceBuilderIDRegion,
	}, nil
}

func (c *DeviceLoginClient) pollBuilderID(ctx context.Context, flow *DeviceLoginFlow) (*DeviceLoginToken, error) {
	payload, err := c.oidcCall(ctx, flow.Region, "/token", map[string]any{
		"clientId":     flow.ClientID,
		"clientSecret": flow.ClientSecret,
		"deviceCode":   flow.DeviceCode,
		"grantType":    "urn:ietf:params:oauth:grant-type:device_code",
	})
	if err != nil {
		return nil, err
	}
	accessToken := strings.TrimSpace(jsonString(payload["accessToken"]))
	if accessToken == "" {
		return nil, fmt.Errorf("builder id returned no access token")
	}
	region := strings.TrimSpace(flow.Region)
	if region == "" {
		region = kiroDeviceBuilderIDRegion
	}
	return &DeviceLoginToken{
		AccessToken:  accessToken,
		RefreshToken: strings.TrimSpace(jsonString(payload["refreshToken"])),
		ExpiresIn:    jsonNumber(payload["expiresIn"], 3600),
		ClientID:     flow.ClientID,
		ClientSecret: flow.ClientSecret,
		Region:       region,
		StartURL:     kiroDeviceBuilderIDStartURL,
		AuthMethod:   "builder-id",
		Provider:     "AWS",
	}, nil
}

func (c *DeviceLoginClient) socialAuthHost() string {
	if c != nil && strings.TrimSpace(c.authHost) != "" {
		return strings.TrimRight(c.authHost, "/")
	}
	return kiroDeviceAuthHost
}

func (c *DeviceLoginClient) oidcBase(region string) string {
	if c != nil && c.cfg != nil {
		if override := c.cfg.GetOAuthEndpointOverride("kiro"); strings.TrimSpace(override.ApiBaseURL) != "" {
			return strings.TrimRight(override.ApiBaseURL, "/")
		}
	}
	if strings.TrimSpace(region) == "" {
		region = kiroDeviceBuilderIDRegion
	}
	return fmt.Sprintf("https://oidc.%s.amazonaws.com", region)
}

func (c *DeviceLoginClient) oidcCall(ctx context.Context, region, path string, body map[string]any) (map[string]any, error) {
	payload, status, err := c.doJSON(ctx, c.oidcBase(region)+path, body)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		code := strings.TrimSpace(jsonString(payload["error"]))
		if code == "" {
			code = fmt.Sprintf("HTTP_%d", status)
		}
		switch {
		case strings.Contains(code, "authorization_pending") || strings.EqualFold(code, "AuthorizationPending"):
			return nil, ErrDeviceAuthorizationPending
		case strings.Contains(code, "slow_down") || strings.EqualFold(code, "SlowDown"):
			return nil, ErrDeviceSlowDown
		case strings.Contains(code, "expired_token") || strings.EqualFold(code, "ExpiredToken"):
			return nil, fmt.Errorf("%w: %s", ErrDeviceExpired, code)
		case strings.Contains(code, "access_denied") || strings.EqualFold(code, "AccessDenied"):
			return nil, fmt.Errorf("%w: %s", ErrDeviceDenied, code)
		}
		message := strings.TrimSpace(jsonString(payload["error_description"]))
		if message == "" {
			message = strings.TrimSpace(jsonString(payload["message"]))
		}
		if message == "" {
			message = code
		}
		return nil, fmt.Errorf("%s: %s", code, message)
	}
	return payload, nil
}

func (c *DeviceLoginClient) postJSON(ctx context.Context, url string, body map[string]any) (map[string]any, error) {
	payload, status, err := c.doJSON(ctx, url, body)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		message := strings.TrimSpace(jsonString(payload["message"]))
		if message == "" {
			message = fmt.Sprintf("HTTP %d", status)
		}
		return nil, fmt.Errorf("HTTP %d: %s", status, message)
	}
	return payload, nil
}

func (c *DeviceLoginClient) doJSON(ctx context.Context, url string, body map[string]any) (map[string]any, int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	payload := map[string]any{}
	if len(bytes.TrimSpace(respBody)) > 0 {
		if err := json.Unmarshal(respBody, &payload); err != nil {
			payload = map[string]any{"message": strings.TrimSpace(string(respBody))}
		}
	}
	return payload, resp.StatusCode, nil
}

func jsonString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	case json.Number:
		return typed.String()
	case float64:
		return fmt.Sprintf("%.0f", typed)
	default:
		return ""
	}
}

func jsonNumber(value any, fallback int) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		n, err := typed.Int64()
		if err == nil {
			return int(n)
		}
	case string:
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(typed), "%d", &n); err == nil {
			return n
		}
	}
	return fallback
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
