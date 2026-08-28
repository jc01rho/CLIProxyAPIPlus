// Package zcode provides the GLM ZCode OAuth flow (UNOFFICIAL, opt-in).
//
// It replicates how the ZCode desktop app turns a Z.AI login into usable GLM
// model access: authorize -> broker -> z/login -> provision a real Z.AI API
// key. The provisioned key is a plain Z.AI API key ("{id}.{secret}") used
// against https://api.z.ai/api/anthropic exactly like a dashboard key.
//
// Credential mapping:
//   - access  = the provisioned Z.AI API key ("{id}.{secret}")
//   - refresh = the upstream Z.AI OAuth access token (used to re-provision)
//
// The API key is long-lived, so expiry is pinned far in the future.
package zcode

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	jwtPattern       = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
	longTokenPattern = regexp.MustCompile(`[A-Za-z0-9_-]{40,}`)
)

// Endpoints / client id. Override via the matching ZCODE_OAUTH_* env vars.
const (
	DefaultAuthorizeURL   = "https://chat.z.ai/api/oauth/authorize"
	DefaultClientID       = "client_P8X5CMWmlaRO9gyO-KSqtg"
	DefaultRedirectURI    = "zcode://oauth/callback"
	DefaultBrokerTokenURL = "https://zcode.z.ai/api/v1/oauth/token"
	DefaultZaiLoginURL    = "https://api.z.ai/api/auth/z/login"
	DefaultUserinfoURL    = "https://chat.z.ai/api/oauth/userinfo"
	DefaultZaiAPIBase     = "https://api.z.ai"
	DefaultAnthropicBase  = "https://api.z.ai/api/anthropic"

	// Broker CLI OAuth flow endpoints. The broker issues a web-based
	// authorize_url (redirect handled by the broker), so no custom protocol
	// (zcode://) is required — the caller polls PollCLIFlow for completion.
	DefaultBrokerCLIInitURL = "https://zcode.z.ai/api/v1/oauth/cli/init"
	DefaultBrokerCLIPollURL = "https://zcode.z.ai/api/v1/oauth/cli/poll"

	// Provisioned API keys are long-lived; pin expiry far out so the auth
	// store never force-refreshes.
	apiKeyTTL = 10 * 365 * 24 * time.Hour

	// Name ZCode gives the API key it auto-provisions.
	apiKeyName = "zcode-api-key"

	tokenRequestTimeout = 30 * time.Second
)

// Credentials holds the result of the zcode OAuth flow.
type Credentials struct {
	AccessToken  string // provisioned Z.AI API key "{id}.{secret}"
	RefreshToken string // upstream Z.AI OAuth access token
	ExpiresAt    time.Time
	Email        string
	AccountID    string
}

// OAuth is the zcode OAuth handler.
type OAuth struct {
	httpClient *http.Client
	// Overridable endpoints (defaults used when empty).
	authorizeURL   string
	clientID       string
	redirectURI    string
	brokerTokenURL string
	zaiLoginURL    string
	userinfoURL    string
	zaiAPIBase     string
	brokerCLIInitURL string
	brokerCLIPollURL string
}

// NewOAuth creates a zcode OAuth handler with default endpoints.
func NewOAuth() *OAuth {
	return &OAuth{
		httpClient:     &http.Client{Timeout: tokenRequestTimeout},
		authorizeURL:   DefaultAuthorizeURL,
		clientID:       DefaultClientID,
		redirectURI:    DefaultRedirectURI,
		brokerTokenURL: DefaultBrokerTokenURL,
		zaiLoginURL:    DefaultZaiLoginURL,
		userinfoURL:    DefaultUserinfoURL,
		zaiAPIBase:     DefaultZaiAPIBase,
		brokerCLIInitURL: DefaultBrokerCLIInitURL,
		brokerCLIPollURL: DefaultBrokerCLIPollURL,
	}
}

// Config overrides the default endpoints and client. Empty fields fall back
// to defaults. Used for tests and for environments that proxy the endpoints.
type Config struct {
	AuthorizeURL   string
	ClientID       string
	RedirectURI    string
	BrokerTokenURL string
	ZaiLoginURL    string
	UserinfoURL    string
	ZaiAPIBase     string
	BrokerCLIInitURL string
	BrokerCLIPollURL string
	HTTPClient     *http.Client
}

// NewOAuthWithConfig creates a zcode OAuth handler with endpoint overrides.
func NewOAuthWithConfig(cfg Config) *OAuth {
	o := NewOAuth()
	if cfg.AuthorizeURL != "" {
		o.authorizeURL = cfg.AuthorizeURL
	}
	if cfg.ClientID != "" {
		o.clientID = cfg.ClientID
	}
	if cfg.RedirectURI != "" {
		o.redirectURI = cfg.RedirectURI
	}
	if cfg.BrokerTokenURL != "" {
		o.brokerTokenURL = cfg.BrokerTokenURL
	}
	if cfg.ZaiLoginURL != "" {
		o.zaiLoginURL = cfg.ZaiLoginURL
	}
	if cfg.UserinfoURL != "" {
		o.userinfoURL = cfg.UserinfoURL
	}
	if cfg.ZaiAPIBase != "" {
		o.zaiAPIBase = cfg.ZaiAPIBase
	}
	if cfg.BrokerCLIInitURL != "" {
		o.brokerCLIInitURL = cfg.BrokerCLIInitURL
	}
	if cfg.BrokerCLIPollURL != "" {
		o.brokerCLIPollURL = cfg.BrokerCLIPollURL
	}
	if cfg.HTTPClient != nil {
		o.httpClient = cfg.HTTPClient
	}
	return o
}

// GenerateAuthURL builds the authorize URL for the given state and redirect URI.
func (o *OAuth) GenerateAuthURL(state, redirectURI string) string {
	if redirectURI == "" {
		redirectURI = o.redirectURI
	}
	params := url.Values{}
	params.Set("redirect_uri", redirectURI)
	params.Set("response_type", "code")
	params.Set("client_id", o.clientID)
	params.Set("state", state)
	return o.authorizeURL + "?" + params.Encode()
}

// CLIFlow holds a broker CLI OAuth flow started via StartCLIFlow.
type CLIFlow struct {
	FlowID          string
	AuthorizeURL    string
	PollToken       string
	ExpiresAt       time.Time
	PollIntervalSec int
}

// StartCLIFlow starts a broker CLI OAuth flow. The returned AuthorizeURL uses a
// web redirect handled by the broker, so no custom protocol (zcode://) is
// required. The caller polls PollCLIFlow until the user completes authorization.
func (o *OAuth) StartCLIFlow(ctx context.Context) (*CLIFlow, error) {
	pollToken, err := randomHex(32)
	if err != nil {
		return nil, fmt.Errorf("zcode broker cli init: generate poll token: %w", err)
	}
	var resp struct {
		Data struct {
			FlowID          string `json:"flow_id"`
			PollToken       string `json:"poll_token"`
			AuthorizeURL    string `json:"authorize_url"`
			ExpiresAt       int64  `json:"expires_at"`
			PollIntervalSec int    `json:"poll_interval_sec"`
		} `json:"data"`
	}
	if err := o.postJSONWithBearer(ctx, o.brokerCLIInitURL, pollToken, map[string]string{"provider": "zai"}, "broker.cli.init", &resp); err != nil {
		return nil, err
	}
	d := resp.Data
	if d.FlowID == "" || d.AuthorizeURL == "" || d.PollToken == "" {
		return nil, fmt.Errorf("zcode broker cli init response missing flow_id/authorize_url/poll_token")
	}
	flow := &CLIFlow{
		FlowID:          d.FlowID,
		AuthorizeURL:    d.AuthorizeURL,
		PollToken:       d.PollToken,
		PollIntervalSec: d.PollIntervalSec,
	}
	if d.ExpiresAt > 0 {
		flow.ExpiresAt = time.Unix(d.ExpiresAt, 0)
	}
	return flow, nil
}

// CLIPollResult is the result of polling a broker CLI OAuth flow.
type CLIPollResult struct {
	Status         string // "pending", "ready", "failed"
	ZaiAccessToken string // upstream Z.AI OAuth access token (status=ready)
	ZcodeToken     string // broker zcode JWT (status=ready)
}

// PollCLIFlow polls a broker CLI OAuth flow for completion.
func (o *OAuth) PollCLIFlow(ctx context.Context, flowID, pollToken string) (*CLIPollResult, error) {
	url := strings.TrimSuffix(o.brokerCLIPollURL, "/") + "/" + url.PathEscape(flowID)
	var resp struct {
		Data struct {
			Status string `json:"status"`
			Token  string `json:"token"`
			Zai    struct {
				AccessToken string `json:"access_token"`
			} `json:"zai"`
		} `json:"data"`
	}
	if err := o.getJSON(ctx, url, pollToken, "broker.cli.poll", &resp); err != nil {
		return nil, err
	}
	return &CLIPollResult{
		Status:         resp.Data.Status,
		ZaiAccessToken: resp.Data.Zai.AccessToken,
		ZcodeToken:     resp.Data.Token,
	}, nil
}

// ExchangeCode exchanges an authorization code for credentials by running the
// full broker -> z/login -> provision pipeline.
func (o *OAuth) ExchangeCode(ctx context.Context, code, state, redirectURI string) (*Credentials, error) {
	if redirectURI == "" {
		redirectURI = o.redirectURI
	}
	// Broker exchange.
	brokerBody := map[string]string{
		"provider":     "zai",
		"code":         code,
		"redirect_uri": redirectURI,
		"state":        state,
	}
	var brokerResp struct {
		Data struct {
			Token string `json:"token"`
			Zai   struct {
				AccessToken string `json:"access_token"`
			} `json:"zai"`
		} `json:"data"`
	}
	if err := o.postJSON(ctx, o.brokerTokenURL, brokerBody, "broker", &brokerResp); err != nil {
		return nil, err
	}
	upstreamZaiAccess := brokerResp.Data.Zai.AccessToken
	if upstreamZaiAccess == "" {
		return nil, fmt.Errorf("zcode broker response missing data.zai.access_token")
	}
	return o.provisionFromUpstream(ctx, upstreamZaiAccess, brokerResp.Data.Token)
}

// Refresh re-provisions the Z.AI API key from the stored upstream token.
func (o *OAuth) Refresh(ctx context.Context, creds *Credentials) (*Credentials, error) {
	if creds == nil || creds.RefreshToken == "" {
		return nil, fmt.Errorf("zcode credentials require re-login; no stored upstream Z.AI token")
	}
	return o.provisionFromUpstream(ctx, creds.RefreshToken, "")
}

// ProvisionFromUpstream provisions a Z.AI API key from an upstream Z.AI OAuth
// access token (e.g. obtained via the broker CLI OAuth poll result).
func (o *OAuth) ProvisionFromUpstream(ctx context.Context, upstreamZaiAccess, zcodeToken string) (*Credentials, error) {
	return o.provisionFromUpstream(ctx, upstreamZaiAccess, zcodeToken)
}

// provisionFromUpstream runs z/login -> provision API key -> resolve identity.
func (o *OAuth) provisionFromUpstream(ctx context.Context, upstreamZaiAccess, zcodeToken string) (*Credentials, error) {
	// z/login -> business token.
	var loginResp struct {
		Data struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	if err := o.postJSON(ctx, o.zaiLoginURL, map[string]string{"token": upstreamZaiAccess}, "z/login", &loginResp); err != nil {
		return nil, err
	}
	businessToken := loginResp.Data.AccessToken
	if businessToken == "" {
		return nil, fmt.Errorf("zcode z/login response missing data.access_token")
	}

	// Provision (or reuse) the Z.AI API key.
	apiKey, identity, err := o.provisionZaiAPIKey(ctx, businessToken)
	if err != nil {
		return nil, err
	}

	// Resolve identity (userinfo, then JWT fallback).
	identity = o.resolveIdentity(ctx, upstreamZaiAccess, identity, []string{zcodeToken, businessToken})

	return &Credentials{
		AccessToken:  apiKey,
		RefreshToken: upstreamZaiAccess,
		ExpiresAt:    time.Now().Add(apiKeyTTL),
		Email:        identity.Email,
		AccountID:    identity.AccountID,
	}, nil
}

type identity struct {
	Email     string
	AccountID string
}

// provisionZaiAPIKey provisions (or reuses) a Z.AI API key named
// "zcode-api-key" using the business token. Returns "{apiKeyId}.{secretKey}".
func (o *OAuth) provisionZaiAPIKey(ctx context.Context, businessToken string) (string, identity, error) {
	// getCustomerInfo -> default org/project.
	var customerInfo struct {
		Data struct {
			ID            string `json:"id"`
			Email         string `json:"email"`
			Organizations []struct {
				OrganizationID string `json:"organizationId"`
				IsDefault      bool   `json:"isDefault"`
				Projects       []struct {
					ProjectID string `json:"projectId"`
					IsDefault bool   `json:"isDefault"`
				} `json:"projects"`
			} `json:"organizations"`
		} `json:"data"`
	}
	if err := o.getJSON(ctx, o.zaiAPIBase+"/api/biz/customer/getCustomerInfo", businessToken, "getCustomerInfo", &customerInfo); err != nil {
		return "", identity{}, err
	}
	data := customerInfo.Data
	var orgID, projectID string
	for _, org := range data.Organizations {
		if org.OrganizationID == "" {
			continue
		}
		if org.IsDefault || orgID == "" {
			orgID = org.OrganizationID
		}
		for _, proj := range org.Projects {
			if proj.ProjectID == "" {
				continue
			}
			if proj.IsDefault || projectID == "" {
				projectID = proj.ProjectID
			}
		}
	}
	if orgID == "" || projectID == "" {
		return "", identity{}, fmt.Errorf("zcode getCustomerInfo response missing default organization/project")
	}
	keysURL := fmt.Sprintf("%s/api/biz/v1/organization/%s/projects/%s/api_keys", o.zaiAPIBase, orgID, projectID)

	// List keys; find or create "zcode-api-key".
	var listResp struct {
		Data []struct {
			APIKey string `json:"apiKey"`
			ID     string `json:"id"`
			Name   string `json:"name"`
		} `json:"data"`
	}
	if err := o.getJSON(ctx, keysURL, businessToken, "api_keys.list", &listResp); err != nil {
		return "", identity{}, err
	}
	var apiKeyID string
	for _, k := range listResp.Data {
		if k.Name == apiKeyName {
			apiKeyID = strings.TrimSpace(k.APIKey)
			if apiKeyID == "" {
				apiKeyID = strings.TrimSpace(k.ID)
			}
			break
		}
	}
	if apiKeyID == "" {
		var createResp struct {
			Data struct {
				APIKey string `json:"apiKey"`
				ID     string `json:"id"`
			} `json:"data"`
		}
		if err := o.postJSON(ctx, keysURL, map[string]string{"name": apiKeyName}, "api_keys.create", &createResp); err != nil {
			return "", identity{}, err
		}
		apiKeyID = strings.TrimSpace(createResp.Data.APIKey)
		if apiKeyID == "" {
			apiKeyID = strings.TrimSpace(createResp.Data.ID)
		}
	}
	if apiKeyID == "" {
		return "", identity{}, fmt.Errorf("zcode api_keys response missing apiKey id")
	}

	// Copy the secret key.
	var copyResp struct {
		Data struct {
			SecretKey string `json:"secretKey"`
		} `json:"data"`
	}
	if err := o.getJSON(ctx, keysURL+"/copy/"+url.PathEscape(apiKeyID), businessToken, "api_keys.copy", &copyResp); err != nil {
		return "", identity{}, err
	}
	secretKey := strings.TrimSpace(copyResp.Data.SecretKey)
	if secretKey == "" {
		return "", identity{}, fmt.Errorf("zcode api_keys copy response missing secretKey")
	}

	ident := identity{}
	if data.Email != "" {
		ident.Email = strings.ToLower(data.Email)
	}
	if data.ID != "" {
		ident.AccountID = data.ID
	}
	return apiKeyID + "." + secretKey, ident, nil
}

// resolveIdentity resolves email/accountId from userinfo, falling back to JWT
// payloads. Returns the input identity if it already has email or accountId.
func (o *OAuth) resolveIdentity(ctx context.Context, upstreamZaiAccess string, fallback identity, jwtCandidates []string) identity {
	if fallback.Email != "" || fallback.AccountID != "" {
		return fallback
	}
	// userinfo.
	var userinfo struct {
		Data struct {
			Email string `json:"email"`
			ID    string `json:"id"`
			Sub   string `json:"sub"`
		} `json:"data"`
	}
	if err := o.getJSON(ctx, o.userinfoURL, upstreamZaiAccess, "userinfo", &userinfo); err == nil {
		ident := identity{}
		if userinfo.Data.Email != "" {
			ident.Email = strings.ToLower(userinfo.Data.Email)
		}
		if userinfo.Data.ID != "" {
			ident.AccountID = userinfo.Data.ID
		} else if userinfo.Data.Sub != "" {
			ident.AccountID = userinfo.Data.Sub
		}
		if ident.Email != "" || ident.AccountID != "" {
			return ident
		}
	}
	// JWT fallback.
	for _, token := range jwtCandidates {
		if token == "" {
			continue
		}
		payload := decodeJWTClaims(token)
		ident := identity{}
		if sub, ok := payload["sub"].(string); ok && sub != "" {
			ident.AccountID = sub
		}
		if email, ok := payload["email"].(string); ok && email != "" {
			ident.Email = strings.ToLower(email)
		}
		if ident.Email != "" || ident.AccountID != "" {
			return ident
		}
	}
	return identity{}
}

// decodeJWTClaims decodes the payload segment of a JWT into a claim map.
func decodeJWTClaims(token string) map[string]interface{} {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[1] == "" {
		return nil
	}
	decoded, err := base64URLDecode(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]interface{}
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return nil
	}
	return claims
}

func base64URLDecode(s string) ([]byte, error) {
	// Go's base64.RawURLEncoding handles unpadded base64url.
	return base64.RawURLEncoding.DecodeString(s)
}

// postJSON performs a POST with a JSON body and decodes the JSON response.
func (o *OAuth) postJSON(ctx context.Context, url string, body interface{}, label string, out interface{}) error {
	return o.postJSONWithBearer(ctx, url, "", body, label, out)
}

// postJSONWithBearer performs a POST with a JSON body and an optional bearer
// token, decoding the JSON response.
func (o *OAuth) postJSONWithBearer(ctx context.Context, url, bearer string, body interface{}, label string, out interface{}) error {
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("zcode %s: marshal body: %w", label, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(bodyBytes)))
	if err != nil {
		return fmt.Errorf("zcode %s: build request: %w", label, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := o.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("zcode %s request failed: %w", label, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyText, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("zcode %s request failed: %d %s", label, resp.StatusCode, redactSecrets(string(bodyText)))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("zcode %s: decode response: %w", label, err)
		}
	}
	return nil
}

// getJSON performs a GET with a bearer token and decodes the JSON response.
func (o *OAuth) getJSON(ctx context.Context, url, bearer, label string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("zcode %s: build request: %w", label, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := o.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("zcode %s request failed: %w", label, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyText, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("zcode %s request failed: %d %s", label, resp.StatusCode, redactSecrets(string(bodyText)))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("zcode %s: decode response: %w", label, err)
		}
	}
	return nil
}

// redactSecrets masks token-like substrings so tokens never leak into errors.
func redactSecrets(text string) string {
	// JWT pattern: eyJ...<base64url>.<base64url>.<base64url>
	text = jwtPattern.ReplaceAllString(text, "[redacted-jwt]")
	// Long base64-ish runs.
	text = longTokenPattern.ReplaceAllString(text, "[redacted]")
	return text
}

// randomHex returns n random bytes encoded as lowercase hex.
func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
