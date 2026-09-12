// Package copilot provides authentication and token management for GitHub Copilot API.
// It handles the OAuth2 device flow for secure authentication with the Copilot API.
package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	// copilotAPITokenURL is the endpoint for getting Copilot API tokens from GitHub token.
	copilotAPITokenURL = "https://api.github.com/copilot_internal/v2/token"
	// copilotAPIEndpoint is the base URL for making API requests.
	copilotAPIEndpoint = "https://api.githubcopilot.com"

	// Request identity mirrors OmniRoute's GitHub Copilot CLI profile
	// (open-sse/config/providerHeaderProfiles.ts): the @github/copilot CLI
	// wire identity, NOT the VS Code Copilot Chat extension. The CLI's
	// `copilot-developer-cli` integration id is the catalog-unlock lever: it
	// exposes the full entitled model set where `vscode-chat` returns a
	// narrower list.
	copilotCLIVersionDefault = "1.0.81-6"
	copilotAPIVersionDefault = "2026-08-01"
	copilotRefreshUserAgent  = "GithubCopilot/1.0"
)

// Exported CLI identity values shared with the executor so both packages
// send the identical fingerprint.
const (
	CopilotIntegrationID   = "copilot-developer-cli"
	CopilotOpenAIIntent    = "conversation-agent"
	CopilotInteractionType = "conversation-user"
	CopilotHarnessID       = "copilot-sdk"
	// CopilotAnthropicAPIVersion is the anthropic-version header for
	// Claude models routed through the Anthropic-native shim.
	CopilotAnthropicAPIVersion = "2023-06-01"
	// CopilotNoRepositorySentinel is the CLI's no-repo literal for the
	// X-GitHub-Repository-Nwo/Host correlation headers.
	CopilotNoRepositorySentinel = "__no_repository__"
)

const (
	copilotCLIVersionEnv     = "GITHUB_COPILOT_CLI_VERSION"
	copilotMachineIDEnv      = "GITHUB_COPILOT_MACHINE_ID"
	copilotVersionSafePattern = "^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$"
)

var (
	copilotVersionRe    = regexp.MustCompile(copilotVersionSafePattern)
	copilotMachineIDOnce sync.Once
	copilotMachineIDValue string
)

// CopilotCLIVersion returns the pinned Copilot CLI version string,
// overridable via GITHUB_COPILOT_CLI_VERSION (OmniRoute #12417 parity).
func CopilotCLIVersion() string {
	if v := strings.TrimSpace(os.Getenv(copilotCLIVersionEnv)); v != "" && copilotVersionRe.MatchString(v) {
		return v
	}
	return copilotCLIVersionDefault
}

// CopilotAPIVersion returns the Copilot API version header value.
func CopilotAPIVersion() string { return copilotAPIVersionDefault }

// CopilotEditorVersion is the `editor-version` header value (`copilot/<v>`).
func CopilotEditorVersion() string { return "copilot/" + CopilotCLIVersion() }

// CopilotUserAgent is the request-time `user-agent` header value (`copilot/<v>`).
func CopilotUserAgent() string { return "copilot/" + CopilotCLIVersion() }

// CopilotChatUserAgent is the internal user headers UA (`GitHubCopilotChat/<v>`).
func CopilotChatUserAgent() string { return "GitHubCopilotChat/" + CopilotCLIVersion() }

// CopilotMachineID returns a stable per-install device fingerprint for the
// X-Client-Machine-Id header. The real CLI sends ONE stable UUID on every
// inference + /models call; a per-call random id would itself be an
// anti-fingerprint tell. One UUID is minted per process and cached
// (env-overridable via GITHUB_COPILOT_MACHINE_ID).
func CopilotMachineID() string {
	copilotMachineIDOnce.Do(func() {
		copilotMachineIDValue = strings.TrimSpace(os.Getenv(copilotMachineIDEnv))
		if copilotMachineIDValue == "" {
			copilotMachineIDValue = uuid.NewString()
		}
	})
	return copilotMachineIDValue
}

// CopilotAPIToken represents the Copilot API token response.
type CopilotAPIToken struct {
	// Token is the JWT token for authenticating with the Copilot API.
	Token string `json:"token"`
	// ExpiresAt is the Unix timestamp when the token expires.
	ExpiresAt int64 `json:"expires_at"`
	// Endpoints contains the available API endpoints.
	Endpoints struct {
		API           string `json:"api"`
		Proxy         string `json:"proxy"`
		OriginTracker string `json:"origin-tracker"`
		Telemetry     string `json:"telemetry"`
	} `json:"endpoints,omitempty"`
	// ErrorDetails contains error information if the request failed.
	ErrorDetails *struct {
		URL              string `json:"url"`
		Message          string `json:"message"`
		DocumentationURL string `json:"documentation_url"`
	} `json:"error_details,omitempty"`
}

// CopilotAuth handles GitHub Copilot authentication flow.
// It provides methods for device flow authentication and token management.
type CopilotAuth struct {
	httpClient   *http.Client
	deviceClient *DeviceFlowClient
	cfg          *config.Config
	// domain is the GitHub domain this auth instance is bound to; empty
	// means github.com. Enterprise credentials carry their own domain.
	domain string
}

// NewCopilotAuth creates a new CopilotAuth service instance.
// It initializes an HTTP client with proxy settings from the provided configuration.
func NewCopilotAuth(cfg *config.Config) *CopilotAuth {
	return NewCopilotAuthWithDomain(cfg, "")
}

// NewCopilotAuthWithDomain creates a CopilotAuth bound to a specific GitHub
// domain (empty means github.com) so GitHub Enterprise accounts use their
// own device/token/API endpoints.
func NewCopilotAuthWithDomain(cfg *config.Config, domain string) *CopilotAuth {
	return &CopilotAuth{
		httpClient:   util.SetProxy(&cfg.SDKConfig, &http.Client{Timeout: 30 * time.Second}),
		deviceClient: NewDeviceFlowClientForDomain(cfg, domain),
		cfg:          cfg,
		domain:       strings.TrimSpace(domain),
	}
}

// Domain returns the GitHub domain this instance is bound to ("" = github.com).
func (c *CopilotAuth) Domain() string {
	if c == nil {
		return ""
	}
	return c.domain
}

// StartDeviceFlow initiates the device flow authentication.
// Returns the device code response containing the user code and verification URI.
func (c *CopilotAuth) StartDeviceFlow(ctx context.Context) (*DeviceCodeResponse, error) {
	return c.deviceClient.RequestDeviceCode(ctx)
}

// WaitForAuthorization polls for user authorization and returns the auth bundle.
func (c *CopilotAuth) WaitForAuthorization(ctx context.Context, deviceCode *DeviceCodeResponse) (*CopilotAuthBundle, error) {
	tokenData, err := c.deviceClient.PollForToken(ctx, deviceCode)
	if err != nil {
		return nil, err
	}

	// Fetch the GitHub username
	userInfo, err := c.deviceClient.FetchUserInfo(ctx, tokenData.AccessToken)
	if err != nil {
		log.Warnf("copilot: failed to fetch user info: %v", err)
	}

	username := userInfo.Login
	if username == "" {
		username = "github-user"
	}

	return &CopilotAuthBundle{
		TokenData: tokenData,
		Username:  username,
		Email:     userInfo.Email,
		Name:      userInfo.Name,
	}, nil
}

// GetCopilotAPIToken exchanges a GitHub access token for a Copilot API token.
// This token is used to make authenticated requests to the Copilot API.
func (c *CopilotAuth) GetCopilotAPIToken(ctx context.Context, githubAccessToken string) (*CopilotAPIToken, error) {
	if githubAccessToken == "" {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, fmt.Errorf("github access token is empty"))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotAPITokenURLForDomain(c.domain), nil)
	if err != nil {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, err)
	}

	req.Header.Set("Authorization", "token "+githubAccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", copilotRefreshUserAgent)
	req.Header.Set("Editor-Version", CopilotEditorVersion())
	req.Header.Set("Editor-Plugin-Version", CopilotEditorVersion())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("copilot api token: close body error: %v", errClose)
		}
	}()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, err)
	}

	if !isHTTPSuccess(resp.StatusCode) {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed,
			fmt.Errorf("status %d: %s", resp.StatusCode, string(bodyBytes)))
	}

	var apiToken CopilotAPIToken
	if err = json.Unmarshal(bodyBytes, &apiToken); err != nil {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, err)
	}

	if apiToken.Token == "" {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, fmt.Errorf("empty copilot api token"))
	}

	return &apiToken, nil
}

// ValidateToken checks if a GitHub access token is valid by attempting to fetch user info.
func (c *CopilotAuth) ValidateToken(ctx context.Context, accessToken string) (bool, string, error) {
	if accessToken == "" {
		return false, "", nil
	}

	userInfo, err := c.deviceClient.FetchUserInfo(ctx, accessToken)
	if err != nil {
		return false, "", err
	}

	return true, userInfo.Login, nil
}

// CreateTokenStorage creates a new CopilotTokenStorage from auth bundle.
func (c *CopilotAuth) CreateTokenStorage(bundle *CopilotAuthBundle) *CopilotTokenStorage {
	return &CopilotTokenStorage{
		AccessToken: bundle.TokenData.AccessToken,
		TokenType:   bundle.TokenData.TokenType,
		Scope:       bundle.TokenData.Scope,
		Username:    bundle.Username,
		Email:       bundle.Email,
		Name:        bundle.Name,
		Type:        "github-copilot",
	}
}

// LoadAndValidateToken loads a token from storage and validates it.
// Returns the storage if valid, or an error if the token is invalid or expired.
func (c *CopilotAuth) LoadAndValidateToken(ctx context.Context, storage *CopilotTokenStorage) (bool, error) {
	if storage == nil || storage.AccessToken == "" {
		return false, fmt.Errorf("no token available")
	}

	// Check if we can still use the GitHub token to get a Copilot API token
	apiToken, err := c.GetCopilotAPIToken(ctx, storage.AccessToken)
	if err != nil {
		return false, err
	}

	// Check if the API token is expired
	if apiToken.ExpiresAt > 0 && time.Now().Unix() >= apiToken.ExpiresAt {
		return false, fmt.Errorf("copilot api token expired")
	}

	return true, nil
}

// GetAPIEndpoint returns the Copilot API endpoint URL.
func (c *CopilotAuth) GetAPIEndpoint() string {
	return FallbackAPIEndpointForDomain(c.domain)
}

// ResolveAPIBaseURL determines the Copilot API base URL for a token. It
// prefers the token response's endpoints.api when the host is trusted
// (static allowlist or the credential's enterprise domain) and falls back
// to the domain default. The trust check prevents SSRF via hostile token
// responses.
func (c *CopilotAuth) ResolveAPIBaseURL(apiToken *CopilotAPIToken) string {
	if apiToken != nil {
		if ep := strings.TrimRight(strings.TrimSpace(apiToken.Endpoints.API), "/"); ep != "" {
			if parsed, err := url.Parse(ep); err == nil && parsed.Scheme == "https" && isAllowedCopilotHostForDomain(parsed.Host, c.domain) {
				return ep
			}
			log.Warnf("copilot: ignoring untrusted API endpoint %q, using default", ep)
		}
	}
	return FallbackAPIEndpointForDomain(c.domain)
}

// MakeAuthenticatedRequest creates an authenticated HTTP request to the Copilot API.
func (c *CopilotAuth) MakeAuthenticatedRequest(ctx context.Context, method, url string, body io.Reader, apiToken *CopilotAPIToken) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+apiToken.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", CopilotUserAgent())
	req.Header.Set("Editor-Version", CopilotEditorVersion())
	req.Header.Set("Openai-Intent", CopilotOpenAIIntent)
	req.Header.Set("X-Interaction-Type", CopilotInteractionType)
	req.Header.Set("Copilot-Integration-Id", CopilotIntegrationID)
	req.Header.Set("Copilot-Harness-Id", CopilotHarnessID)
	req.Header.Set("X-GitHub-Api-Version", CopilotAPIVersion())
	req.Header.Set("X-Client-Machine-Id", CopilotMachineID())

	return req, nil
}

// CopilotModelEntry represents a single model entry returned by the Copilot /models API.
type CopilotModelEntry struct {
	ID           string         `json:"id"`
	Object       string         `json:"object"`
	Created      int64          `json:"created"`
	OwnedBy      string         `json:"owned_by"`
	Name         string         `json:"name,omitempty"`
	Version      string         `json:"version,omitempty"`
	Capabilities map[string]any `json:"capabilities,omitempty"`
	// Policy carries the account policy state for the model ("enabled",
	// "disabled", ...). Models whose policy is disabled cannot be used.
	Policy *CopilotModelPolicy `json:"policy,omitempty"`
	// ModelPickerEnabled reports whether the model is enabled in the
	// account's Copilot model picker.
	ModelPickerEnabled *bool `json:"model_picker_enabled,omitempty"`
	// SupportedEndpoints lists the API endpoints the model is served on
	// (top-level `supported_endpoints` in the /models response).
	SupportedEndpoints []string `json:"supported_endpoints,omitempty"`
}

// CopilotModelPolicy is the policy object attached to a Copilot model.
type CopilotModelPolicy struct {
	State string `json:"state,omitempty"`
}

// PolicyState returns the model policy state ("" when absent).
func (e *CopilotModelEntry) PolicyState() string {
	if e == nil || e.Policy == nil {
		return ""
	}
	return strings.TrimSpace(e.Policy.State)
}

// IsModelPickerEnabled reports the explicit model_picker_enabled flag,
// defaulting to false when the API omits it.
func (e *CopilotModelEntry) IsModelPickerEnabled() bool {
	return e != nil && e.ModelPickerEnabled != nil && *e.ModelPickerEnabled
}

// SupportsToolCalls reports whether the model supports tool calls. Only an
// explicit capabilities.supports.tool_calls=false disables it; missing
// capability metadata keeps the model available. Mirrors senpi's
// parseAvailableCopilotModelIds() check.
func (e *CopilotModelEntry) SupportsToolCalls() bool {
	if e == nil || e.Capabilities == nil {
		return true
	}
	supportsRaw, ok := e.Capabilities["supports"]
	if !ok {
		return true
	}
	supports, ok := supportsRaw.(map[string]any)
	if !ok {
		return true
	}
	if toolCalls, ok := supports["tool_calls"].(bool); ok && !toolCalls {
		return false
	}
	return true
}

// IsRoutableChatModel reports whether a live /models row is a routable chat
// model. Capability-driven (rename-robust) rather than an id allowlist: any
// model the account is entitled to whose capabilities.type is "chat" (or that
// carries a chat-shaped supported_endpoints list) is kept, so a
// newly-entitled model shows up with no code change. Also filters out rows
// when policy.state is set and != "enabled", or when model_picker_enabled=false.
// Explicitly non-chat rows (embeddings / completion) are dropped as well.
// Port of OmniRoute's isRoutableChatModel().
func (e *CopilotModelEntry) IsRoutableChatModel() bool {
	if e == nil {
		return false
	}
	if policyState := e.PolicyState(); policyState != "" && policyState != "enabled" {
		return false
	}
	if e.ModelPickerEnabled != nil && !*e.ModelPickerEnabled {
		return false
	}

	if capType := capabilityString(e.Capabilities, "type"); capType != "" {
		return capType == "chat"
	}

	// No capabilities.type present — fall back to supported_endpoints shape.
	// A chat model exposes /chat/completions, /responses, or /v1/messages.
	endpoints := e.chatEndpointCandidates()
	if len(endpoints) > 0 {
		for _, endpoint := range endpoints {
			switch {
			case strings.Contains(endpoint, "/chat/completions"),
				strings.Contains(endpoint, "/responses"),
				strings.Contains(endpoint, "/v1/messages"):
				return true
			}
		}
		return false
	}

	// Neither signal present: keep it unless its id looks like a known non-chat
	// utility (embedding / completion sentinels). This keeps discovery
	// permissive without re-introducing a brittle positive allowlist.
	id := strings.ToLower(strings.TrimSpace(e.ID))
	if id == "" {
		return false
	}
	return !strings.Contains(id, "embedding") && id != "gpt-41-copilot"
}

// chatEndpointCandidates collects supported_endpoints from the top-level
// field first, then from the capabilities map.
func (e *CopilotModelEntry) chatEndpointCandidates() []string {
	candidates := make([]string, 0, len(e.SupportedEndpoints)+4)
	candidates = append(candidates, e.SupportedEndpoints...)
	if raw, ok := e.Capabilities["supported_endpoints"]; ok {
		if list, ok := raw.([]any); ok {
			for _, item := range list {
				if s, ok := item.(string); ok {
					candidates = append(candidates, s)
				}
			}
		}
	}
	return candidates
}

// capabilityString reads a top-level string value from the capabilities map.
func capabilityString(capabilities map[string]any, key string) string {
	if capabilities == nil {
		return ""
	}
	if v, ok := capabilities[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// FilterAvailableCopilotModels applies OmniRoute's availability rules to a raw
// /models response: capability-driven chat filtering (IsRoutableChatModel).
// Unlike the previous id-allowlist approach, this keeps every entitled chat
// model the catalog returns (grok-*, mai-*, newly-added gemini previews, ...)
// so newly-entitled models appear without a code change.
func FilterAvailableCopilotModels(entries []CopilotModelEntry) []CopilotModelEntry {
	filtered := make([]CopilotModelEntry, 0, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.ID) == "" {
			continue
		}
		if !entry.IsRoutableChatModel() {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

// CopilotModelLimits holds the token limits returned by the Copilot /models API
// under capabilities.limits. These limits vary by account type (individual vs
// business) and are the authoritative source for enforcing prompt size.
type CopilotModelLimits struct {
	// MaxContextWindowTokens is the total context window (prompt + output).
	MaxContextWindowTokens int
	// MaxPromptTokens is the hard limit on input/prompt tokens.
	// Exceeding this triggers a 400 error from the Copilot API.
	MaxPromptTokens int
	// MaxOutputTokens is the maximum number of output/completion tokens.
	MaxOutputTokens int
}

// Limits extracts the token limits from the model's capabilities map.
// Returns nil if no limits are available or the structure is unexpected.
//
// Expected Copilot API shape:
//
//	"capabilities": {
//	    "limits": {
//	        "max_context_window_tokens": 200000,
//	        "max_prompt_tokens": 168000,
//	        "max_output_tokens": 32000
//	    }
//	}
func (e *CopilotModelEntry) Limits() *CopilotModelLimits {
	if e.Capabilities == nil {
		return nil
	}
	limitsRaw, ok := e.Capabilities["limits"]
	if !ok {
		return nil
	}
	limitsMap, ok := limitsRaw.(map[string]any)
	if !ok {
		return nil
	}

	result := &CopilotModelLimits{
		MaxContextWindowTokens: anyToInt(limitsMap["max_context_window_tokens"]),
		MaxPromptTokens:        anyToInt(limitsMap["max_prompt_tokens"]),
		MaxOutputTokens:        anyToInt(limitsMap["max_output_tokens"]),
	}

	// Only return if at least one field is populated.
	if result.MaxContextWindowTokens == 0 && result.MaxPromptTokens == 0 && result.MaxOutputTokens == 0 {
		return nil
	}
	return result
}

// anyToInt converts a JSON-decoded numeric value to int.
// Go's encoding/json decodes numbers into float64 when the target is any/interface{}.
func anyToInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case float32:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	default:
		return 0
	}
}

// CopilotModelsResponse represents the response from the Copilot /models endpoint.
type CopilotModelsResponse struct {
	Data   []CopilotModelEntry `json:"data"`
	Object string              `json:"object"`
}

// maxModelsResponseSize is the maximum allowed response size from the /models endpoint (2 MB).
const maxModelsResponseSize = 2 * 1024 * 1024

// allowedCopilotAPIHosts is the set of hosts that are considered safe for Copilot API requests.
var allowedCopilotAPIHosts = map[string]bool{
	"api.githubcopilot.com":               true,
	"api.individual.githubcopilot.com":    true,
	"api.business.githubcopilot.com":      true,
	"copilot-proxy.githubusercontent.com": true,
}

// ListModels fetches the list of available models from the Copilot API.
// It requires a valid Copilot API token (not the GitHub access token).
func (c *CopilotAuth) ListModels(ctx context.Context, apiToken *CopilotAPIToken) ([]CopilotModelEntry, error) {
	if apiToken == nil || apiToken.Token == "" {
		return nil, fmt.Errorf("copilot: api token is required for listing models")
	}

	// Build models URL, validating the endpoint host to prevent SSRF.
	modelsURL := c.ResolveAPIBaseURL(apiToken) + "/models"

	req, err := c.MakeAuthenticatedRequest(ctx, http.MethodGet, modelsURL, nil, apiToken)
	if err != nil {
		return nil, fmt.Errorf("copilot: failed to create models request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("copilot: models request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("copilot list models: close body error: %v", errClose)
		}
	}()

	// Limit response body to prevent memory exhaustion.
	limitedReader := io.LimitReader(resp.Body, maxModelsResponseSize)
	bodyBytes, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("copilot: failed to read models response: %w", err)
	}

	if !isHTTPSuccess(resp.StatusCode) {
		return nil, fmt.Errorf("copilot: list models failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var modelsResp CopilotModelsResponse
	if err = json.Unmarshal(bodyBytes, &modelsResp); err != nil {
		return nil, fmt.Errorf("copilot: failed to parse models response: %w", err)
	}

	return modelsResp.Data, nil
}

// ListModelsWithGitHubToken is a convenience method that exchanges a GitHub access token
// for a Copilot API token and then fetches the available models.
func (c *CopilotAuth) ListModelsWithGitHubToken(ctx context.Context, githubAccessToken string) ([]CopilotModelEntry, error) {
	apiToken, err := c.GetCopilotAPIToken(ctx, githubAccessToken)
	if err != nil {
		return nil, fmt.Errorf("copilot: failed to get API token for model listing: %w", err)
	}

	return c.ListModels(ctx, apiToken)
}

// buildChatCompletionURL builds the URL for chat completions API.
func buildChatCompletionURL() string {
	return copilotAPIEndpoint + "/chat/completions"
}

// isHTTPSuccess checks if the status code indicates success (2xx).
func isHTTPSuccess(statusCode int) bool {
	return statusCode >= 200 && statusCode < 300
}
