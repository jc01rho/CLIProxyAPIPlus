// Package antigravity provides OAuth2 authentication functionality for the Antigravity provider.
package antigravity

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/oauthform"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
)

// TokenResponse represents OAuth token response from Google
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

// userInfo represents Google user profile
type userInfo struct {
	Email string `json:"email"`
}

// antigravityState is the reconstructed PKCE state returned from the OAuth
// provider's redirect. The reference cortexkit client uses the same shape.
type antigravityState struct {
	Index     string `json:"index"` // PKCE verifier – kept as "index" for compatibility with the reference client's decode path
	ProjectId string `json:"projectId"`
}

// AntigravityAuth handles Antigravity OAuth authentication
type AntigravityAuth struct {
	httpClient *http.Client
	cfg        *config.Config
}

func NewAntigravityAuth(cfg *config.Config, httpClient *http.Client) *AntigravityAuth {
	if cfg == nil {
		cfg = &config.Config{}
	}
	if httpClient != nil {
		return &AntigravityAuth{httpClient: httpClient, cfg: cfg}
	}
	return &AntigravityAuth{
		httpClient: util.SetProxy(&cfg.SDKConfig, &http.Client{}),
		cfg:        cfg,
	}
}

func (o *AntigravityAuth) tokenEndpoint() string {
	if o.cfg != nil {
		if ep := o.cfg.GetOAuthEndpointOverride("antigravity").TokenURL; ep != "" {
			return ep
		}
	}
	return TokenEndpoint
}

func (o *AntigravityAuth) authEndpoint() string {
	if o.cfg != nil {
		if ep := o.cfg.GetOAuthEndpointOverride("antigravity").AuthorizeURL; ep != "" {
			return ep
		}
	}
	return AuthEndpoint
}

func (o *AntigravityAuth) userinfoEndpoint() string {
	if o.cfg != nil {
		if ep := o.cfg.GetOAuthEndpointOverride("antigravity").UserinfoURL; ep != "" {
			return ep
		}
	}
	return UserInfoEndpoint
}

func (o *AntigravityAuth) shortUserAgent() string {
	return misc.AntigravityRequestUserAgent("")
}

func (o *AntigravityAuth) nodeUserAgent() string {
	return misc.AntigravityLoadCodeAssistUserAgent("")
}

func antigravityLoadCodeAssistMetadata() map[string]string {
	return map[string]string{
		"ideType": "ANTIGRAVITY",
	}
}

func antigravityControlPlaneMetadata(userAgent string) map[string]string {
	return map[string]string{
		"ide_type":    "ANTIGRAVITY",
		"ide_version": misc.AntigravityVersionFromUserAgent(userAgent),
		"ide_name":    "antigravity",
	}
}

func extractCloudaicompanionProject(data map[string]any) string {
	if data == nil {
		return ""
	}
	for _, key := range []string{"cloudaicompanionProject", "projectId", "project"} {
		switch value := data[key].(type) {
		case string:
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		case map[string]any:
			if id, ok := value["id"].(string); ok {
				if trimmed := strings.TrimSpace(id); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	return ""
}

func defaultAntigravityTierID(loadResp map[string]any) string {
	if tiers, okTiers := loadResp["allowedTiers"].([]any); okTiers {
		for _, rawTier := range tiers {
			tier, okTier := rawTier.(map[string]any)
			if !okTier {
				continue
			}
			if isDefault, okDefault := tier["isDefault"].(bool); !okDefault || !isDefault {
				continue
			}
			if id, okID := tier["id"].(string); okID {
				if trimmed := strings.TrimSpace(id); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	if currentTier, okTier := loadResp["currentTier"].(map[string]any); okTier {
		if id, okID := currentTier["id"].(string); okID {
			if trimmed := strings.TrimSpace(id); trimmed != "" {
				return trimmed
			}
		}
	}
	return "free-tier"
}

// BuildAuthURL generates the OAuth authorization URL.
//
// state is opaque from the caller's perspective — for PKCE flows it is expected
// to be the base64url(JSON) state produced by encodeAntigravityState which
// embeds the PKCE verifier and optional projectId, matching the cortexkit
// reference client.
//
// redirectURI is the local OAuth callback URI to embed in the request.
//
// pkceCodes is required for Google OAuth on public clients; pass nil only when
// running against a custom authorization server that does not require PKCE.
func (o *AntigravityAuth) BuildAuthURL(state, redirectURI string, pkceCodes *PKCECodes) string {
	if strings.TrimSpace(redirectURI) == "" {
		redirectURI = fmt.Sprintf("http://localhost:%d/oauth-callback", CallbackPort)
	}
	query := oauthform.Encode(
		oauthform.Pair{Key: "client_id", Value: ClientID},
		oauthform.Pair{Key: "response_type", Value: "code"},
		oauthform.Pair{Key: "redirect_uri", Value: redirectURI},
		oauthform.Pair{Key: "scope", Value: strings.Join(Scopes, " ")},
		oauthform.Pair{Key: "code_challenge", Value: pkceCodeChallenge(pkceCodes)},
		oauthform.Pair{Key: "code_challenge_method", Value: "S256"},
		oauthform.Pair{Key: "state", Value: state},
		oauthform.Pair{Key: "access_type", Value: "offline"},
		oauthform.Pair{Key: "prompt", Value: "consent"},
	)
	return o.authEndpoint() + "?" + query
}

func pkceCodeChallenge(pkceCodes *PKCECodes) string {
	if pkceCodes == nil {
		return ""
	}
	return pkceCodes.CodeChallenge
}

// ExchangeCodeForTokens exchanges authorization code for access and refresh tokens.
//
// state must be the value originally passed to BuildAuthURL. For PKCE flows
// the state encodes the verifier; this function decodes the state and sends
// the code_verifier in the token POST body, matching the cortexkit
// reference client.
//
// When state does not carry a PKCE verifier (legacy flows), pkceCodes can be
// passed explicitly to provide the code_verifier directly.
func (o *AntigravityAuth) ExchangeCodeForTokens(ctx context.Context, code, redirectURI, state string, pkceCodes *PKCECodes) (*TokenResponse, error) {
	verifier := ""
	if decoded, ok := DecodeAntigravityState(state); ok {
		verifier = decoded.Index
	}
	if verifier == "" && pkceCodes != nil {
		verifier = pkceCodes.CodeVerifier
	}

	// Field order mirrors cortexkit/antigravity-auth exchangeAntigravity():
	// client_id, client_secret, code, grant_type, redirect_uri, code_verifier.
	body := oauthform.Encode(
		oauthform.Pair{Key: "client_id", Value: ClientID},
		oauthform.Pair{Key: "client_secret", Value: ClientSecret},
		oauthform.Pair{Key: "code", Value: code},
		oauthform.Pair{Key: "grant_type", Value: "authorization_code"},
		oauthform.Pair{Key: "redirect_uri", Value: redirectURI},
		oauthform.Pair{Key: "code_verifier", Value: verifier},
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.tokenEndpoint(), strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("antigravity token exchange: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=UTF-8")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	req.Header.Set("User-Agent", GeminicliUserAgent)

	resp, errDo := o.httpClient.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("antigravity token exchange: execute request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("antigravity token exchange: close body error: %v", errClose)
		}
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		bodyBytes, errRead := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		if errRead != nil {
			return nil, fmt.Errorf("antigravity token exchange: read response: %w", errRead)
		}
		body := strings.TrimSpace(string(bodyBytes))
		if body == "" {
			return nil, fmt.Errorf("antigravity token exchange: request failed: status %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("antigravity token exchange: request failed: status %d: %s", resp.StatusCode, oauthform.MaskSensitive(body))
	}

	var token TokenResponse
	if errDecode := json.NewDecoder(resp.Body).Decode(&token); errDecode != nil {
		return nil, fmt.Errorf("antigravity token exchange: decode response: %w", errDecode)
	}
	return &token, nil
}

// FetchUserInfo retrieves user email from Google
func (o *AntigravityAuth) FetchUserInfo(ctx context.Context, accessToken string) (string, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return "", fmt.Errorf("antigravity userinfo: missing access token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.userinfoEndpoint(), nil)
	if err != nil {
		return "", fmt.Errorf("antigravity userinfo: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", GeminicliUserAgent)

	resp, errDo := o.httpClient.Do(req)
	if errDo != nil {
		return "", fmt.Errorf("antigravity userinfo: execute request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("antigravity userinfo: close body error: %v", errClose)
		}
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		bodyBytes, errRead := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		if errRead != nil {
			return "", fmt.Errorf("antigravity userinfo: read response: %w", errRead)
		}
		body := strings.TrimSpace(string(bodyBytes))
		if body == "" {
			return "", fmt.Errorf("antigravity userinfo: request failed: status %d", resp.StatusCode)
		}
		return "", fmt.Errorf("antigravity userinfo: request failed: status %d: %s", resp.StatusCode, body)
	}
	var info userInfo
	if errDecode := json.NewDecoder(resp.Body).Decode(&info); errDecode != nil {
		return "", fmt.Errorf("antigravity userinfo: decode response: %w", errDecode)
	}
	email := strings.TrimSpace(info.Email)
	if email == "" {
		return "", fmt.Errorf("antigravity userinfo: response missing email")
	}
	return email, nil
}

// FetchProjectID retrieves the project ID for the authenticated user via loadCodeAssist
func (o *AntigravityAuth) FetchProjectID(ctx context.Context, accessToken string) (string, error) {
	userAgent := o.shortUserAgent()
	loadReqBody := map[string]any{
		"metadata": antigravityLoadCodeAssistMetadata(),
	}

	rawBody, errMarshal := json.Marshal(loadReqBody)
	if errMarshal != nil {
		return "", fmt.Errorf("marshal request body: %w", errMarshal)
	}

	endpointURL := fmt.Sprintf("%s/%s:loadCodeAssist", APIEndpoint, APIVersion)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, strings.NewReader(string(rawBody)))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, errDo := o.httpClient.Do(req)
	if errDo != nil {
		return "", fmt.Errorf("execute request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("antigravity loadCodeAssist: close body error: %v", errClose)
		}
	}()

	bodyBytes, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return "", fmt.Errorf("read response: %w", errRead)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	var loadResp map[string]any
	if errDecode := json.Unmarshal(bodyBytes, &loadResp); errDecode != nil {
		return "", fmt.Errorf("decode response: %w", errDecode)
	}

	projectID := extractCloudaicompanionProject(loadResp)

	if projectID == "" {
		projectID, err = o.OnboardUser(ctx, accessToken, defaultAntigravityTierID(loadResp))
		if err != nil {
			return "", err
		}
		if projectID == "" {
			return "", fmt.Errorf("project id not found in loadCodeAssist or onboardUser response")
		}
		return projectID, nil
	}

	return projectID, nil
}

// OnboardUser attempts to fetch the project ID via onboardUser by polling for completion
func (o *AntigravityAuth) OnboardUser(ctx context.Context, accessToken, tierID string) (string, error) {
	log.Infof("Antigravity: onboarding user with tier: %s", tierID)
	userAgent := o.nodeUserAgent()
	requestBody := map[string]any{
		"tier_id":  tierID,
		"metadata": antigravityControlPlaneMetadata(userAgent),
	}

	rawBody, errMarshal := json.Marshal(requestBody)
	if errMarshal != nil {
		return "", fmt.Errorf("marshal request body: %w", errMarshal)
	}

	maxAttempts := 5
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		log.Debugf("Polling attempt %d/%d", attempt, maxAttempts)

		reqCtx := ctx
		var cancel context.CancelFunc
		if reqCtx == nil {
			reqCtx = context.Background()
		}
		reqCtx, cancel = context.WithTimeout(reqCtx, 30*time.Second)

		endpointURL := fmt.Sprintf("%s/%s:onboardUser", DailyAPIEndpoint, APIVersion)
		req, errRequest := http.NewRequestWithContext(reqCtx, http.MethodPost, endpointURL, strings.NewReader(string(rawBody)))
		if errRequest != nil {
			cancel()
			return "", fmt.Errorf("create request: %w", errRequest)
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Accept", "*/*")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("X-Goog-Api-Client", misc.AntigravityGoogAPIClientUA)

		resp, errDo := o.httpClient.Do(req)
		if errDo != nil {
			cancel()
			return "", fmt.Errorf("execute request: %w", errDo)
		}

		bodyBytes, errRead := io.ReadAll(resp.Body)
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("close body error: %v", errClose)
		}
		cancel()

		if errRead != nil {
			return "", fmt.Errorf("read response: %w", errRead)
		}

		if resp.StatusCode == http.StatusOK {
			var data map[string]any
			if errDecode := json.Unmarshal(bodyBytes, &data); errDecode != nil {
				return "", fmt.Errorf("decode response: %w", errDecode)
			}

			if done, okDone := data["done"].(bool); okDone && done {
				projectID := ""
				if responseData, okResp := data["response"].(map[string]any); okResp {
					projectID = extractCloudaicompanionProject(responseData)
				}

				if projectID != "" {
					log.Infof("Successfully fetched project_id: %s", util.HideAPIKey(projectID))
					return projectID, nil
				}

				return "", fmt.Errorf("no project_id in response")
			}

			time.Sleep(2 * time.Second)
			continue
		}

		responsePreview := strings.TrimSpace(string(bodyBytes))
		if len(responsePreview) > 500 {
			responsePreview = responsePreview[:500]
		}

		responseErr := responsePreview
		if len(responseErr) > 200 {
			responseErr = responseErr[:200]
		}
		return "", fmt.Errorf("http %d: %s", resp.StatusCode, responseErr)
	}

	return "", fmt.Errorf("onboard user did not complete after %d attempts", maxAttempts)
}

// EncodeAntigravityState produces a base64url-encoded JSON blob containing the
// PKCE verifier and the optional project id, matching the cortexkit reference
// client's state encoding. Exposed as a public function for callers that need
// to mint a state token before invoking BuildAuthURL.
func EncodeAntigravityState(verifier, projectID string) (string, error) {
	payload := antigravityState{Index: verifier, ProjectId: projectID}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("antigravity encode state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodeAntigravityState parses a state value produced by EncodeAntigravityState
// and returns the embedded PKCE verifier and project id. The boolean result is
// false if the state is empty, malformed, or uses an unexpected shape.
func DecodeAntigravityState(state string) (antigravityState, bool) {
	state = strings.TrimSpace(state)
	if state == "" {
		return antigravityState{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(state)
	if err != nil {
		// The state may be a plain random token from a non-PKCE flow; treat it
		// as "no embedded verifier" rather than failing the parse.
		return antigravityState{}, false
	}
	var decoded antigravityState
	if errDecode := json.Unmarshal(raw, &decoded); errDecode != nil {
		return antigravityState{}, false
	}
	return decoded, true
}
