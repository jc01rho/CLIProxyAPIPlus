package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"

	log "github.com/sirupsen/logrus"
)

// AntigravityProjectInfo contains project ID and subscription tier info
type AntigravityProjectInfo struct {
	ProjectID string
	TierID    string // "ultra", "pro", "standard", "free", or "unknown"
	TierName  string // Display name from API (e.g., "Gemini Code Assist Pro")
	IsPaid    bool   // true if tier is "pro" or "ultra"
}

const (
	antigravityCallbackPort = 51121
)

// AntigravityAuthenticator implements OAuth login for the antigravity provider.
type AntigravityAuthenticator struct{}

// NewAntigravityAuthenticator constructs a new authenticator instance.
func NewAntigravityAuthenticator() Authenticator { return &AntigravityAuthenticator{} }

// Provider returns the provider key for antigravity.
func (AntigravityAuthenticator) Provider() string { return "antigravity" }

// RefreshLead instructs the manager to refresh five minutes before expiry.
func (AntigravityAuthenticator) RefreshLead() *time.Duration {
	lead := 5 * time.Minute
	return &lead
}

// Login launches a local OAuth flow to obtain antigravity tokens and persists them.
func (AntigravityAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	callbackPort := antigravityCallbackPort
	if opts.CallbackPort > 0 {
		callbackPort = opts.CallbackPort
	}

	httpClient := util.SetProxy(&cfg.SDKConfig, &http.Client{})
	authSvc := antigravity.NewAntigravityAuth(cfg, httpClient)

	pkceCodes, errPKCE := antigravity.GeneratePKCECodes()
	if errPKCE != nil {
		return nil, fmt.Errorf("antigravity: failed to generate PKCE codes: %w", errPKCE)
	}

	state, errState := antigravity.EncodeAntigravityState(pkceCodes.CodeVerifier, "")
	if errState != nil {
		return nil, fmt.Errorf("antigravity: failed to generate state: %w", errState)
	}

	srv, port, cbChan, errServer := startAntigravityCallbackServer(callbackPort)
	if errServer != nil {
		return nil, fmt.Errorf("antigravity: failed to start callback server: %w", errServer)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	redirectURI := fmt.Sprintf("http://localhost:%d/oauth-callback", port)
	authURL := authSvc.BuildAuthURL(state, redirectURI, pkceCodes)

	if !opts.NoBrowser {
		fmt.Println("Opening browser for antigravity authentication")
		if !browser.IsAvailable() {
			log.Warn("No browser available; please open the URL manually")
			util.PrintSSHTunnelInstructions(port)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		} else if errOpen := browser.OpenURL(authURL); errOpen != nil {
			log.Warnf("Failed to open browser automatically: %v", errOpen)
			util.PrintSSHTunnelInstructions(port)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		}
	} else {
		util.PrintSSHTunnelInstructions(port)
		fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
	}

	fmt.Println("Waiting for antigravity authentication callback...")

	var cbRes callbackResult
	timeoutTimer := time.NewTimer(5 * time.Minute)
	defer timeoutTimer.Stop()

	var manualPromptTimer *time.Timer
	var manualPromptC <-chan time.Time
	if opts.Prompt != nil {
		manualPromptTimer = time.NewTimer(15 * time.Second)
		manualPromptC = manualPromptTimer.C
		defer manualPromptTimer.Stop()
	}

	var manualInputCh <-chan string
	var manualInputErrCh <-chan error

waitForCallback:
	for {
		select {
		case res := <-cbChan:
			cbRes = res
			break waitForCallback
		case <-manualPromptC:
			manualPromptC = nil
			if manualPromptTimer != nil {
				manualPromptTimer.Stop()
			}
			select {
			case res := <-cbChan:
				cbRes = res
				break waitForCallback
			default:
			}
			manualInputCh, manualInputErrCh = misc.AsyncPrompt(opts.Prompt, "Paste the antigravity callback URL (or press Enter to keep waiting): ")
			continue
		case input := <-manualInputCh:
			manualInputCh = nil
			manualInputErrCh = nil
			parsed, errParse := misc.ParseOAuthCallback(input)
			if errParse != nil {
				return nil, errParse
			}
			if parsed == nil {
				continue
			}
			cbRes = callbackResult{
				Code:  parsed.Code,
				State: parsed.State,
				Error: parsed.Error,
			}
			break waitForCallback
		case errManual := <-manualInputErrCh:
			return nil, errManual
		case <-timeoutTimer.C:
			return nil, fmt.Errorf("antigravity: authentication timed out")
		}
	}

	if cbRes.Error != "" {
		return nil, fmt.Errorf("antigravity: authentication failed: %s", cbRes.Error)
	}
	if cbRes.State != state {
		return nil, fmt.Errorf("antigravity: invalid state")
	}
	if cbRes.Code == "" {
		return nil, fmt.Errorf("antigravity: missing authorization code")
	}

	tokenResp, errToken := authSvc.ExchangeCodeForTokens(ctx, cbRes.Code, redirectURI, state, pkceCodes)
	if errToken != nil {
		return nil, fmt.Errorf("antigravity: token exchange failed: %w", errToken)
	}

	email := ""
	if tokenResp.AccessToken != "" {
		if fetched, errInfo := authSvc.FetchUserInfo(ctx, tokenResp.AccessToken); errInfo == nil && strings.TrimSpace(fetched) != "" {
			email = strings.TrimSpace(fetched)
		}
	}

	// Fetch project ID via loadCodeAssist (same approach as Gemini CLI)
	projectID := ""
	tierID := "unknown"
	tierName := "Unknown"
	tierIsPaid := false
	if tokenResp.AccessToken != "" {
		projectInfo, errProject := FetchAntigravityProjectInfo(ctx, tokenResp.AccessToken, httpClient)
		if errProject != nil {
			log.Warnf("antigravity: failed to fetch project info: %v", errProject)
		} else {
			projectID = projectInfo.ProjectID
			tierID = projectInfo.TierID
			tierName = projectInfo.TierName
			tierIsPaid = projectInfo.IsPaid
			log.Infof("antigravity: obtained project ID %s, tier %s", projectID, tierID)
		}
	}
	if strings.TrimSpace(projectID) == "" {
		return nil, fmt.Errorf("antigravity: project ID discovery returned empty project")
	}

	now := time.Now()
	metadata := map[string]any{
		"type":          "antigravity",
		"access_token":  tokenResp.AccessToken,
		"refresh_token": tokenResp.RefreshToken,
		"expires_in":    tokenResp.ExpiresIn,
		"timestamp":     now.UnixMilli(),
		"expired":       now.Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339),
		"tier_id":       tierID,
		"tier_name":     tierName,
		"tier_is_paid":  tierIsPaid,
	}
	if email != "" {
		metadata["email"] = email
	}
	if projectID != "" {
		metadata["project_id"] = projectID
	}

	fileName := sanitizeAntigravityFileName(email)
	label := email
	if label == "" {
		label = "antigravity"
	}

	fmt.Println("Antigravity authentication successful")
	if projectID != "" {
		fmt.Printf("Using GCP project: %s\n", util.HideAPIKey(projectID))
	}
	return &coreauth.Auth{
		ID:       fileName,
		Provider: "antigravity",
		FileName: fileName,
		Label:    label,
		Metadata: metadata,
	}, nil
}

type callbackResult struct {
	Code  string
	Error string
	State string
}

func startAntigravityCallbackServer(port int) (*http.Server, int, <-chan callbackResult, error) {
	if port <= 0 {
		port = antigravityCallbackPort
	}
	addr := fmt.Sprintf(":%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, 0, nil, err
	}
	port = listener.Addr().(*net.TCPAddr).Port
	resultCh := make(chan callbackResult, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth-callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		res := callbackResult{
			Code:  strings.TrimSpace(q.Get("code")),
			Error: strings.TrimSpace(q.Get("error")),
			State: strings.TrimSpace(q.Get("state")),
		}
		resultCh <- res
		if res.Code != "" && res.Error == "" {
			_, _ = w.Write([]byte("<h1>Login successful</h1><p>You can close this window.</p>"))
		} else {
			_, _ = w.Write([]byte("<h1>Login failed</h1><p>Please check the CLI output.</p>"))
		}
	})

	srv := &http.Server{Handler: mux}
	go func() {
		if errServe := srv.Serve(listener); errServe != nil && !strings.Contains(errServe.Error(), "Server closed") {
			log.Warnf("antigravity callback server error: %v", errServe)
		}
	}()

	return srv, port, resultCh, nil
}


func sanitizeAntigravityFileName(email string) string {
	if strings.TrimSpace(email) == "" {
		return "antigravity.json"
	}
	replacer := strings.NewReplacer("@", "_", ".", "_")
	return fmt.Sprintf("antigravity-%s.json", replacer.Replace(email))
}

func extractTierInfo(resp map[string]any) (tierID, tierName string, isPaid bool) {
	var effectiveTier map[string]any
	if pt, ok := resp["paidTier"].(map[string]any); ok && pt != nil {
		effectiveTier = pt
	} else if ct, ok := resp["currentTier"].(map[string]any); ok {
		effectiveTier = ct
	}

	if effectiveTier == nil {
		return "unknown", "Unknown", false
	}

	id, _ := effectiveTier["id"].(string)
	name, _ := effectiveTier["name"].(string)

	idLower := strings.ToLower(id)
	nameLower := strings.ToLower(name)

	// Check tier by ID first, then by name patterns
	switch {
	case strings.Contains(idLower, "ultra"):
		return "ultra", name, true
	case strings.Contains(idLower, "pro"):
		return "pro", name, true
	case strings.Contains(idLower, "standard"), strings.Contains(idLower, "free"):
		return "free", name, false
	// Check by tier name patterns when ID doesn't match
	case strings.Contains(nameLower, "google one ai pro"):
		// "Gemini Code Assist in Google One AI Pro" -> Pro tier
		return "pro", name, true
	case strings.Contains(nameLower, "for individuals"):
		// "Gemini Code Assist for individuals" -> Free tier
		return "free", name, false
	default:
		return id, name, false
	}
}

// Antigravity API constants for project discovery
const (
	antigravityAPIEndpoint    = "https://cloudcode-pa.googleapis.com"
	antigravityAPIVersion     = "v1internal"
	antigravityAPIUserAgent   = "google-api-nodejs-client/9.15.1"
	antigravityAPIClient      = "google-cloud-sdk vscode_cloudshelleditor/0.1"
	antigravityClientMetadata = `{"ideType":"IDE_UNSPECIFIED","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"}`
)

// FetchAntigravityProjectID exposes project discovery for external callers.
func FetchAntigravityProjectID(ctx context.Context, accessToken string, httpClient *http.Client) (string, error) {
	info, err := FetchAntigravityProjectInfo(ctx, accessToken, httpClient)
	if err != nil {
		return "", err
	}
	return info.ProjectID, nil
}

// FetchAntigravityProjectInfo fetches project ID and tier info from the Antigravity API.
func FetchAntigravityProjectInfo(ctx context.Context, accessToken string, httpClient *http.Client) (*AntigravityProjectInfo, error) {
	loadReqBody := map[string]any{
		"metadata": map[string]string{
			"ideType":    "ANTIGRAVITY",
			"platform":   "PLATFORM_UNSPECIFIED",
			"pluginType": "GEMINI",
		},
	}

	rawBody, errMarshal := json.Marshal(loadReqBody)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal request body: %w", errMarshal)
	}

	endpointURL := fmt.Sprintf("%s/%s:loadCodeAssist", antigravityAPIEndpoint, antigravityAPIVersion)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, strings.NewReader(string(rawBody)))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", antigravityAPIUserAgent)
	req.Header.Set("X-Goog-Api-Client", antigravityAPIClient)
	req.Header.Set("Client-Metadata", antigravityClientMetadata)

	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("execute request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("antigravity loadCodeAssist: close body error: %v", errClose)
		}
	}()

	bodyBytes, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return nil, fmt.Errorf("read response: %w", errRead)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	var loadResp map[string]any
	if errDecode := json.Unmarshal(bodyBytes, &loadResp); errDecode != nil {
		return nil, fmt.Errorf("decode response: %w", errDecode)
	}

	tierID, tierName, isPaid := extractTierInfo(loadResp)

	projectID := ""
	if id, ok := loadResp["cloudaicompanionProject"].(string); ok {
		projectID = strings.TrimSpace(id)
	}
	if projectID == "" {
		if projectMap, ok := loadResp["cloudaicompanionProject"].(map[string]any); ok {
			if id, okID := projectMap["id"].(string); okID {
				projectID = strings.TrimSpace(id)
			}
		}
	}

	if projectID == "" {
		onboardTierID := "legacy-tier"
		if tiers, okTiers := loadResp["allowedTiers"].([]any); okTiers {
			for _, rawTier := range tiers {
				tier, okTier := rawTier.(map[string]any)
				if !okTier {
					continue
				}
				if isDefault, okDefault := tier["isDefault"].(bool); okDefault && isDefault {
					if id, okID := tier["id"].(string); okID && strings.TrimSpace(id) != "" {
						onboardTierID = strings.TrimSpace(id)
						break
					}
				}
			}
		}

		projectID, err = antigravityOnboardUser(ctx, accessToken, onboardTierID, httpClient)
		if err != nil {
			return nil, err
		}
	}

	return &AntigravityProjectInfo{
		ProjectID: projectID,
		TierID:    tierID,
		TierName:  tierName,
		IsPaid:    isPaid,
	}, nil
}

// antigravityOnboardUser attempts to fetch the project ID via onboardUser by polling for completion.
// It returns an empty string when the operation times out or completes without a project ID.
func antigravityOnboardUser(ctx context.Context, accessToken, tierID string, httpClient *http.Client) (string, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	fmt.Println("Antigravity: onboarding user...", tierID)
	requestBody := map[string]any{
		"tierId": tierID,
		"metadata": map[string]string{
			"ideType":    "ANTIGRAVITY",
			"platform":   "PLATFORM_UNSPECIFIED",
			"pluginType": "GEMINI",
		},
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

		endpointURL := fmt.Sprintf("%s/%s:onboardUser", antigravityAPIEndpoint, antigravityAPIVersion)
		req, errRequest := http.NewRequestWithContext(reqCtx, http.MethodPost, endpointURL, strings.NewReader(string(rawBody)))
		if errRequest != nil {
			cancel()
			return "", fmt.Errorf("create request: %w", errRequest)
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", antigravityAPIUserAgent)
		req.Header.Set("X-Goog-Api-Client", antigravityAPIClient)
		req.Header.Set("Client-Metadata", antigravityClientMetadata)

		resp, errDo := httpClient.Do(req)
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
					switch projectValue := responseData["cloudaicompanionProject"].(type) {
					case map[string]any:
						if id, okID := projectValue["id"].(string); okID {
							projectID = strings.TrimSpace(id)
						}
					case string:
						projectID = strings.TrimSpace(projectValue)
					}
				}

				if projectID != "" {
					log.Infof("Successfully fetched project_id: %s", projectID)
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

	return "", nil
}
