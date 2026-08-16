package management

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cline"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor"
	kiloauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kilo"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kimi"
	kiro "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

type codexOAuthService interface {
	GenerateAuthURL(state string, pkceCodes *codex.PKCECodes) (string, error)
	ExchangeCodeForTokens(ctx context.Context, code string, pkceCodes *codex.PKCECodes) (*codex.CodexAuthBundle, error)
	CreateTokenStorage(bundle *codex.CodexAuthBundle) *codex.CodexTokenStorage
}

func (h *Handler) RequestAnthropicToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	fmt.Println("Initializing Claude authentication...")

	// Generate PKCE codes
	pkceCodes, err := claude.GeneratePKCECodes()
	if err != nil {
		log.Errorf("Failed to generate PKCE codes: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate PKCE codes"})
		return
	}

	// Generate random state parameter
	state, err := misc.GenerateRandomState()
	if err != nil {
		log.Errorf("Failed to generate state parameter: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}

	// Initialize Claude auth service
	anthropicAuth := claude.NewClaudeAuth(h.cfg)

	// Generate authorization URL (then override redirect_uri to reuse server port)
	authURL, state, err := anthropicAuth.GenerateAuthURL(state, pkceCodes)
	if err != nil {
		log.Errorf("Failed to generate authorization URL: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return
	}

	RegisterOAuthSession(state, "anthropic")

	isWebUI := isWebUIRequest(c)
	var forwarder *callbackForwarder
	if isWebUI {
		targetURL, errTarget := h.managementCallbackURL("/anthropic/callback")
		if errTarget != nil {
			log.WithError(errTarget).Error("failed to compute anthropic callback target")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
			return
		}
		var errStart error
		if forwarder, errStart = startCallbackForwarder(anthropicCallbackPort, "anthropic", targetURL); errStart != nil {
			log.WithError(errStart).Error("failed to start anthropic callback forwarder")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start callback server"})
			return
		}
	}

	go func() {
		if isWebUI {
			defer stopCallbackForwarderInstance(anthropicCallbackPort, forwarder)
		}

		// Helper: wait for callback file
		waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-anthropic-%s.oauth", state))
		waitForFile := func(path string, timeout time.Duration) (map[string]string, error) {
			deadline := time.Now().Add(timeout)
			for {
				if !IsOAuthSessionPending(state, "anthropic") {
					return nil, errOAuthSessionNotPending
				}
				if time.Now().After(deadline) {
					SetOAuthSessionError(state, "Timeout waiting for OAuth callback")
					return nil, fmt.Errorf("timeout waiting for OAuth callback")
				}
				data, errRead := os.ReadFile(path)
				if errRead == nil {
					var m map[string]string
					_ = json.Unmarshal(data, &m)
					_ = os.Remove(path)
					return m, nil
				}
				time.Sleep(500 * time.Millisecond)
			}
		}

		fmt.Println("Waiting for authentication callback...")
		// Wait up to 5 minutes
		resultMap, errWait := waitForFile(waitFile, 5*time.Minute)
		if errWait != nil {
			if errors.Is(errWait, errOAuthSessionNotPending) {
				return
			}
			authErr := claude.NewAuthenticationError(claude.ErrCallbackTimeout, errWait)
			log.Error(claude.GetUserFriendlyMessage(authErr))
			return
		}
		if errStr := resultMap["error"]; errStr != "" {
			oauthErr := claude.NewOAuthError(errStr, "", http.StatusBadRequest)
			log.Error(claude.GetUserFriendlyMessage(oauthErr))
			SetOAuthSessionError(state, "Bad request")
			return
		}
		if resultMap["state"] != state {
			authErr := claude.NewAuthenticationError(claude.ErrInvalidState, fmt.Errorf("expected %s, got %s", state, resultMap["state"]))
			log.Error(claude.GetUserFriendlyMessage(authErr))
			SetOAuthSessionError(state, "State code error")
			return
		}

		// Parse code (Claude may append state after '#')
		rawCode := resultMap["code"]
		code := strings.Split(rawCode, "#")[0]

		// Exchange code for tokens using internal auth service
		bundle, errExchange := anthropicAuth.ExchangeCodeForTokens(ctx, code, state, pkceCodes)
		if errExchange != nil {
			authErr := claude.NewAuthenticationError(claude.ErrCodeExchangeFailed, errExchange)
			log.Errorf("Failed to exchange authorization code for tokens: %v", authErr)
			SetOAuthSessionError(state, "Failed to exchange authorization code for tokens")
			return
		}

		// Create token storage
		tokenStorage := anthropicAuth.CreateTokenStorage(bundle)
		metadata := map[string]any{"email": tokenStorage.Email}
		if tokenStorage.AccountUUID != "" {
			metadata["account_uuid"] = tokenStorage.AccountUUID
		}
		if tokenStorage.OrganizationUUID != "" {
			metadata["organization_uuid"] = tokenStorage.OrganizationUUID
		}
		if tokenStorage.OrganizationName != "" {
			metadata["organization_name"] = tokenStorage.OrganizationName
		}
		if len(tokenStorage.DeviceIDs) > 0 {
			metadata[claude.ClaudeDeviceIDsMetadataKey] = append([]string(nil), tokenStorage.DeviceIDs...)
		}
		record := &coreauth.Auth{
			ID:       fmt.Sprintf("claude-%s.json", tokenStorage.Email),
			Provider: "claude",
			FileName: fmt.Sprintf("claude-%s.json", tokenStorage.Email),
			Storage:  tokenStorage,
			Metadata: metadata,
		}
		if errGuard := guardOAuthSessionPendingForSave(state, "anthropic"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save authentication tokens: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}

		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		if bundle.APIKey != "" {
			fmt.Println("API key obtained and saved")
		}
		fmt.Println("You can now use Claude services through this CLI")
		CompleteOAuthSession(state)
	}()

	c.JSON(200, gin.H{"status": "ok", "url": authURL, "state": state})
}

func (h *Handler) RequestCodexToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	fmt.Println("Initializing Codex authentication...")

	// Generate PKCE codes
	pkceCodes, err := codex.GeneratePKCECodes()
	if err != nil {
		log.Errorf("Failed to generate PKCE codes: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate PKCE codes"})
		return
	}

	// Generate random state parameter
	state, err := misc.GenerateRandomState()
	if err != nil {
		log.Errorf("Failed to generate state parameter: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}

	// Initialize Codex auth service
	openaiAuth := newCodexOAuthService(h.cfg)

	// Generate authorization URL
	authURL, err := openaiAuth.GenerateAuthURL(state, pkceCodes)
	if err != nil {
		log.Errorf("Failed to generate authorization URL: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return
	}

	RegisterOAuthSession(state, "codex")

	isWebUI := isWebUIRequest(c)
	var forwarder *callbackForwarder
	if isWebUI {
		targetURL, errTarget := h.managementCallbackURL("/codex/callback")
		if errTarget != nil {
			log.WithError(errTarget).Error("failed to compute codex callback target")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
			return
		}
		var errStart error
		if forwarder, errStart = startCallbackForwarder(codexCallbackPort, "codex", targetURL); errStart != nil {
			log.WithError(errStart).Error("failed to start codex callback forwarder")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start callback server"})
			return
		}
	}

	go func() {
		if isWebUI {
			defer stopCallbackForwarderInstance(codexCallbackPort, forwarder)
		}

		// Wait for callback file
		waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-codex-%s.oauth", state))
		deadline := time.Now().Add(5 * time.Minute)
		var code string
		for {
			if !IsOAuthSessionPending(state, "codex") {
				return
			}
			if time.Now().After(deadline) {
				authErr := codex.NewAuthenticationError(codex.ErrCallbackTimeout, fmt.Errorf("timeout waiting for OAuth callback"))
				log.Error(codex.GetUserFriendlyMessage(authErr))
				SetOAuthSessionError(state, "Timeout waiting for OAuth callback")
				return
			}
			if data, errR := os.ReadFile(waitFile); errR == nil {
				var m map[string]string
				_ = json.Unmarshal(data, &m)
				_ = os.Remove(waitFile)
				if errStr := m["error"]; errStr != "" {
					oauthErr := codex.NewOAuthError(errStr, "", http.StatusBadRequest)
					log.Error(codex.GetUserFriendlyMessage(oauthErr))
					SetOAuthSessionError(state, "Bad Request")
					return
				}
				if m["state"] != state {
					authErr := codex.NewAuthenticationError(codex.ErrInvalidState, fmt.Errorf("expected %s, got %s", state, m["state"]))
					SetOAuthSessionError(state, "State code error")
					log.Error(codex.GetUserFriendlyMessage(authErr))
					return
				}
				code = m["code"]
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		log.Debug("Authorization code received, exchanging for tokens...")
		// Exchange code for tokens using internal auth service
		bundle, errExchange := openaiAuth.ExchangeCodeForTokens(ctx, code, pkceCodes)
		if errExchange != nil {
			authErr := codex.NewAuthenticationError(codex.ErrCodeExchangeFailed, errExchange)
			SetOAuthSessionError(state, oauthSessionErrorWithCause("Failed to exchange authorization code for tokens", errExchange))
			log.Errorf("Failed to exchange authorization code for tokens: %v", authErr)
			return
		}

		// Extract additional info for filename generation
		claims, _ := codex.ParseJWTToken(bundle.TokenData.IDToken)
		planType := ""
		hashAccountID := ""
		if claims != nil {
			planType = strings.TrimSpace(claims.CodexAuthInfo.ChatgptPlanType)
			if accountID := claims.GetAccountID(); accountID != "" {
				digest := sha256.Sum256([]byte(accountID))
				hashAccountID = hex.EncodeToString(digest[:])[:8]
			}
		}

		// Create token storage and persist
		tokenStorage := openaiAuth.CreateTokenStorage(bundle)
		fileName := codex.CredentialFileName(tokenStorage.Email, planType, hashAccountID, true)
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "codex",
			FileName: fileName,
			Storage:  tokenStorage,
			Metadata: map[string]any{
				"email":      tokenStorage.Email,
				"account_id": tokenStorage.AccountID,
			},
		}
		if errGuard := guardOAuthSessionPendingForSave(state, "codex"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			log.Errorf("Failed to save authentication tokens: %v", errSave)
			return
		}
		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		if bundle.APIKey != "" {
			fmt.Println("API key obtained and saved")
		}
		fmt.Println("You can now use Codex services through this CLI")
		CompleteOAuthSession(state)
	}()

	c.JSON(200, gin.H{"status": "ok", "url": authURL, "state": state})
}

func (h *Handler) RequestAntigravityToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	fmt.Println("Initializing Antigravity authentication...")

	authSvc := antigravity.NewAntigravityAuth(h.cfg, nil)

	pkceCodes, errPKCE := antigravity.GeneratePKCECodes()
	if errPKCE != nil {
		log.Errorf("Failed to generate PKCE codes: %v", errPKCE)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate PKCE codes"})
		return
	}
	state, errState := antigravity.EncodeAntigravityState(pkceCodes.CodeVerifier, "")
	if errState != nil {
		log.Errorf("Failed to generate state parameter: %v", errState)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}

	redirectURI := antigravity.RedirectURI
	authURL := authSvc.BuildAuthURL(state, redirectURI, pkceCodes)

	RegisterOAuthSession(state, "antigravity")

	isWebUI := isWebUIRequest(c)
	var forwarder *callbackForwarder
	if isWebUI {
		targetURL, errTarget := h.managementCallbackURL("/antigravity/callback")
		if errTarget != nil {
			log.WithError(errTarget).Error("failed to compute antigravity callback target")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
			return
		}
		var errStart error
		if forwarder, errStart = startCallbackForwarder(antigravity.CallbackPort, "antigravity", targetURL); errStart != nil {
			log.WithError(errStart).Error("failed to start antigravity callback forwarder")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start callback server"})
			return
		}
	}

	go func() {
		if isWebUI {
			defer stopCallbackForwarderInstance(antigravity.CallbackPort, forwarder)
		}

		waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-antigravity-%s.oauth", state))
		deadline := time.Now().Add(5 * time.Minute)
		var authCode string
		for {
			if !IsOAuthSessionPending(state, "antigravity") {
				return
			}
			if time.Now().After(deadline) {
				log.Error("oauth flow timed out")
				SetOAuthSessionError(state, "OAuth flow timed out")
				return
			}
			if data, errReadFile := os.ReadFile(waitFile); errReadFile == nil {
				var payload map[string]string
				_ = json.Unmarshal(data, &payload)
				_ = os.Remove(waitFile)
				if errStr := strings.TrimSpace(payload["error"]); errStr != "" {
					log.Errorf("Authentication failed: %s", errStr)
					SetOAuthSessionError(state, "Authentication failed")
					return
				}
				if payloadState := strings.TrimSpace(payload["state"]); payloadState != "" && payloadState != state {
					log.Errorf("Authentication failed: state mismatch")
					SetOAuthSessionError(state, "Authentication failed: state mismatch")
					return
				}
				authCode = strings.TrimSpace(payload["code"])
				if authCode == "" {
					log.Error("Authentication failed: code not found")
					SetOAuthSessionError(state, "Authentication failed: code not found")
					return
				}
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		tokenResp, errToken := authSvc.ExchangeCodeForTokens(ctx, authCode, redirectURI, state, pkceCodes)
		if errToken != nil {
			log.Errorf("Failed to exchange token: %v", errToken)
			SetOAuthSessionError(state, "Failed to exchange token")
			return
		}

		accessToken := strings.TrimSpace(tokenResp.AccessToken)
		if accessToken == "" {
			log.Error("antigravity: token exchange returned empty access token")
			SetOAuthSessionError(state, "Failed to exchange token")
			return
		}

		email, errInfo := authSvc.FetchUserInfo(ctx, accessToken)
		if errInfo != nil {
			log.Errorf("Failed to fetch user info: %v", errInfo)
			SetOAuthSessionError(state, "Failed to fetch user info")
			return
		}
		email = strings.TrimSpace(email)
		if email == "" {
			log.Error("antigravity: user info returned empty email")
			SetOAuthSessionError(state, "Failed to fetch user info")
			return
		}

		projectID := ""
		if accessToken != "" {
			fetchedProjectID, errProject := authSvc.FetchProjectID(ctx, accessToken)
			if errProject != nil {
				log.Warnf("antigravity: failed to fetch project ID: %v", errProject)
			} else {
				projectID = fetchedProjectID
				log.Infof("antigravity: obtained project ID %s", util.HideAPIKey(projectID))
			}
		}

		now := time.Now()
		metadata := map[string]any{
			"type":          "antigravity",
			"access_token":  tokenResp.AccessToken,
			"refresh_token": tokenResp.RefreshToken,
			"expires_in":    tokenResp.ExpiresIn,
			"timestamp":     now.UnixMilli(),
			"expired":       now.Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339),
		}
		if email != "" {
			metadata["email"] = email
		}
		if projectID != "" {
			metadata["project_id"] = projectID
		}

		fileName := antigravity.CredentialFileName(email)
		label := strings.TrimSpace(email)
		if label == "" {
			label = "antigravity"
		}

		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "antigravity",
			FileName: fileName,
			Label:    label,
			Metadata: metadata,
		}
		if errGuard := guardOAuthSessionPendingForSave(state, "antigravity"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save token to file: %v", errSave)
			SetOAuthSessionError(state, "Failed to save token to file")
			return
		}

		CompleteOAuthSession(state)
		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		if projectID != "" {
			fmt.Printf("Using GCP project: %s\n", util.HideAPIKey(projectID))
		}
		fmt.Println("You can now use Antigravity services through this CLI")
	}()

	c.JSON(200, gin.H{"status": "ok", "url": authURL, "state": state})
}

func (h *Handler) RequestXAIToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	fmt.Println("Initializing xAI authentication...")

	state := fmt.Sprintf("xai-%d", time.Now().UnixNano())
	authSvc := xaiauth.NewXAIAuth(h.cfg)

	deviceFlow, errStartDeviceFlow := authSvc.StartDeviceFlow(ctx)
	if errStartDeviceFlow != nil {
		log.Errorf("Failed to start xAI device flow: %v", errStartDeviceFlow)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start device authorization flow"})
		return
	}
	authURL := strings.TrimSpace(deviceFlow.VerificationURIComplete)
	if authURL == "" {
		authURL = strings.TrimSpace(deviceFlow.VerificationURI)
	}

	RegisterOAuthSession(state, "xai")

	go func() {
		pollCtx, cancelPoll := context.WithCancel(ctx)
		defer cancelPoll()
		go watchOAuthSessionCancel(pollCtx, cancelPoll, state, "xai")

		fmt.Println("Waiting for xAI authentication...")
		bundle, errWaitForAuthorization := authSvc.WaitForAuthorization(pollCtx, deviceFlow)
		if errWaitForAuthorization != nil {
			if !IsOAuthSessionPending(state, "xai") {
				return
			}
			log.Errorf("xAI authentication failed: %v", errWaitForAuthorization)
			SetOAuthSessionError(state, oauthSessionErrorWithCause("Authentication failed", errWaitForAuthorization))
			return
		}
		if !IsOAuthSessionPending(state, "xai") {
			return
		}

		tokenStorage := authSvc.CreateTokenStorage(bundle)
		if tokenStorage == nil || strings.TrimSpace(tokenStorage.AccessToken) == "" {
			log.Error("xAI token exchange returned empty access token")
			SetOAuthSessionError(state, "Failed to exchange token")
			return
		}

		fileName := xaiauth.CredentialFileName(tokenStorage.Email, tokenStorage.Subject)
		label := strings.TrimSpace(tokenStorage.Email)
		if label == "" {
			label = "xAI"
		}

		metadata := map[string]any{
			"type":           "xai",
			"access_token":   tokenStorage.AccessToken,
			"refresh_token":  tokenStorage.RefreshToken,
			"id_token":       tokenStorage.IDToken,
			"token_type":     tokenStorage.TokenType,
			"expires_in":     tokenStorage.ExpiresIn,
			"expired":        tokenStorage.Expire,
			"last_refresh":   tokenStorage.LastRefresh,
			"base_url":       tokenStorage.BaseURL,
			"token_endpoint": tokenStorage.TokenEndpoint,
			"auth_kind":      "oauth",
		}
		if tokenStorage.Email != "" {
			metadata["email"] = tokenStorage.Email
		}
		if tokenStorage.Subject != "" {
			metadata["sub"] = tokenStorage.Subject
		}

		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "xai",
			FileName: fileName,
			Label:    label,
			Storage:  tokenStorage,
			Metadata: metadata,
			Attributes: map[string]string{
				"auth_kind": "oauth",
				"base_url":  tokenStorage.BaseURL,
			},
		}
		if errGuard := guardOAuthSessionPendingForSave(state, "xai"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save xAI token to file: %v", errSave)
			SetOAuthSessionError(state, "Failed to save token to file")
			return
		}

		CompleteOAuthSession(state)
		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		fmt.Println("You can now use xAI services through this CLI")
	}()

	response := gin.H{"status": "ok", "url": authURL, "state": state, "flow": "device"}
	if userCode := strings.TrimSpace(deviceFlow.UserCode); userCode != "" {
		response["user_code"] = userCode
	}
	if deviceFlow.ExpiresIn > 0 {
		response["expires_in"] = deviceFlow.ExpiresIn
	} else {
		response["expires_in"] = int(xaiauth.MaxPollDuration / time.Second)
	}
	c.JSON(200, response)
}

// ClineOAuthService is the minimal subset of the Cline authenticator the
// management handler depends on for the /v0/management/cline-auth-url flow.
// It mirrors the seam used by the Codex handler so tests can stub the
// network-bound parts without touching real Cline endpoints.
type ClineOAuthService interface {
	GenerateAuthURL(state, callbackURL string) string
	ExchangeCode(ctx context.Context, code, redirectURI string) (*cline.TokenResponse, error)
}

var newClineOAuthService = func(cfg *config.Config) ClineOAuthService {
	return cline.NewClineAuth(cfg)
}

// RequestClineToken implements the GET /v0/management/cline-auth-url handler.
// It mirrors the Anthropic/Codex pattern: generate a state, register the
// session, return the authorization URL, then wait in a goroutine for the
// callback file written by the WebUI forwarder (port 7829) or the generic
// /v0/management/oauth-callback endpoint and exchange the code for tokens.
func (h *Handler) RequestClineToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	log.Info("Initializing Cline authentication")

	state, err := misc.GenerateRandomState()
	if err != nil {
		log.Errorf("Failed to generate state parameter: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}

	authSvc := newClineOAuthService(h.cfg)

	// Cline WorkOS redirects the browser to the configured redirect_uri. For
	// the WebUI flow a forwarder listens on clineCallbackPort and bounces the
	// browser back to the generic management callback endpoint so the
	// single OAuth callback contract writes the result exactly once.
	callbackURL := fmt.Sprintf("http://localhost:%d/callback", clineCallbackPort)
	isWebUI := isWebUIRequest(c)
	var forwarder *callbackForwarder
	if isWebUI {
		targetURL, errTarget := h.managementCallbackURL("/v0/management/oauth-callback")
		if errTarget != nil {
			log.WithError(errTarget).Error("failed to compute cline callback target")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
			return
		}
		// 9Router/WorkOS does not echo the state parameter back in the callback
		// redirect, so embed it in the forwarder target. The generic
		// /v0/management/oauth-callback handler requires state to correlate the
		// session. The forwarder appends the raw query (code=...) with "&".
		separator := "?"
		if strings.Contains(targetURL, "?") {
			separator = "&"
		}
		targetURL = targetURL + separator + "state=" + url.QueryEscape(state)
		var errStart error
		if forwarder, errStart = startCallbackForwarder(clineCallbackPort, "cline", targetURL); errStart != nil {
			log.WithError(errStart).Error("failed to start cline callback forwarder")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start callback server"})
			return
		}
	}

	authURL := authSvc.GenerateAuthURL(state, callbackURL)
	RegisterOAuthSession(state, "cline")

	go func() {
		if isWebUI {
			defer stopCallbackForwarderInstance(clineCallbackPort, forwarder)
		}

		waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-cline-%s.oauth", state))
		deadline := time.Now().Add(5 * time.Minute)
		var code string
		var resultErr string
		for {
			if !IsOAuthSessionPending(state, "cline") {
				return
			}
			if time.Now().After(deadline) {
				SetOAuthSessionError(state, "Timeout waiting for OAuth callback")
				log.Error("Timeout waiting for Cline OAuth callback")
				return
			}
			if data, errR := os.ReadFile(waitFile); errR == nil {
				var m map[string]string
				_ = json.Unmarshal(data, &m)
				_ = os.Remove(waitFile)
				resultErr = m["error"]
				if resultErr == "" {
					code = m["code"]
				}
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		if resultErr != "" {
			SetOAuthSessionError(state, resultErr)
			log.WithFields(log.Fields{"provider": "cline", "stage": "callback"}).Error("Cline OAuth callback returned error")
			return
		}
		if strings.TrimSpace(code) == "" {
			SetOAuthSessionError(state, "Missing authorization code")
			log.WithField("provider", "cline").Error("Cline OAuth callback missing authorization code")
			return
		}

		// The 9Router contract delivers the token payload base64-encoded inside
		// the code parameter. Decode it first; only fall back to a server-side
		// token exchange when the payload is not a valid base64 token blob.
		tokenResp, errResolve := resolveClineCallbackToken(ctx, authSvc, code, callbackURL)
		if errResolve != nil {
			SetOAuthSessionError(state, "Failed to exchange authorization code for tokens")
			log.WithFields(log.Fields{"provider": "cline", "stage": "resolve"}).WithError(errResolve).Error("Failed to resolve Cline authorization code")
			return
		}
		if tokenResp == nil || strings.TrimSpace(tokenResp.AccessToken) == "" {
			SetOAuthSessionError(state, "Missing access token in response")
			log.WithField("provider", "cline").Error("Cline token response missing access token")
			return
		}

		email := strings.TrimSpace(tokenResp.UserEmail())
		if email == "" {
			SetOAuthSessionError(state, "Missing account email")
			log.WithField("provider", "cline").Error("Cline token response missing account email")
			return
		}

		expiresAt := cline.ParseExpiresAt(tokenResp.ExpiresAt)
		storedAccess := strings.TrimPrefix(strings.TrimSpace(tokenResp.AccessToken), "workos:")

		ts := &cline.ClineTokenStorage{
			AccessToken:  storedAccess,
			RefreshToken: strings.TrimSpace(tokenResp.RefreshToken),
			ExpiresAt:    expiresAt,
			Email:        email,
			Type:         "cline",
		}

		fileName := cline.CredentialFileName(email)
		metadata := cline.SyncMetadata(ts, map[string]any{
			"email":           email,
			"workos_prefixed": true,
		})

		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "cline",
			FileName: fileName,
			Storage:  ts,
			Metadata: metadata,
		}
		if errGuard := guardOAuthSessionPendingForSave(state, "cline"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.WithField("provider", "cline").WithError(errSave).Error("Failed to save Cline authentication tokens")
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}
		log.WithFields(log.Fields{"provider": "cline", "email": email, "path": savedPath}).Info("Cline authentication successful")
		CompleteOAuthSession(state)
	}()

	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": authURL, "state": state})
}

// resolveClineCallbackToken implements the 9Router contract for the management
// flow: the Cline callback usually delivers the token payload base64-encoded
// inside the code parameter, so decode it first and only fall back to the
// server-side /api/v1/auth/token exchange when the payload is not a token blob.
func resolveClineCallbackToken(ctx context.Context, authSvc ClineOAuthService, code, callbackURL string) (*cline.TokenResponse, error) {
	if tokenResp, ok := decodeClineCallbackTokenPayload(code); ok {
		return tokenResp, nil
	}
	tokenResp, errExchange := authSvc.ExchangeCode(ctx, code, callbackURL)
	if errExchange != nil {
		return nil, fmt.Errorf("cline token exchange failed: %w", errExchange)
	}
	return tokenResp, nil
}

// decodeClineCallbackTokenPayload mirrors sdk/auth's decodeClineBase64Token:
// pad to a multiple of 4, decode (Std then URL variants), then JSON-parse up to
// the final '}' to drop any trailing junk appended by the browser redirect.
func decodeClineCallbackTokenPayload(code string) (*cline.TokenResponse, bool) {
	trimmed := strings.TrimSpace(code)
	if trimmed == "" {
		return nil, false
	}
	if padding := 4 - (len(trimmed) % 4); padding != 4 {
		trimmed += strings.Repeat("=", padding)
	}

	decoders := []*base64.Encoding{base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding}
	var decoded []byte
	var decodeErr error
	for _, enc := range decoders {
		if buf, err := enc.DecodeString(trimmed); err == nil {
			decoded = buf
			decodeErr = nil
			break
		} else {
			decodeErr = err
		}
	}
	if decodeErr != nil || len(decoded) == 0 {
		return nil, false
	}

	lastBrace := strings.LastIndex(string(decoded), "}")
	if lastBrace == -1 {
		return nil, false
	}
	var tokenResp cline.TokenResponse
	if err := json.Unmarshal([]byte(string(decoded)[:lastBrace+1]), &tokenResp); err != nil {
		return nil, false
	}
	if strings.TrimSpace(tokenResp.AccessToken) == "" {
		return nil, false
	}
	return &tokenResp, true
}

func (h *Handler) RequestKimiToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	fmt.Println("Initializing Kimi authentication...")

	state := fmt.Sprintf("kmi-%d", time.Now().UnixNano())
	// Initialize Kimi auth service
	kimiAuth := kimi.NewKimiAuth(h.cfg)

	// Generate authorization URL
	deviceFlow, errStartDeviceFlow := kimiAuth.StartDeviceFlow(ctx)
	if errStartDeviceFlow != nil {
		log.Errorf("Failed to generate authorization URL: %v", errStartDeviceFlow)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return
	}
	authURL := deviceFlow.VerificationURIComplete
	if authURL == "" {
		authURL = deviceFlow.VerificationURI
	}

	RegisterOAuthSession(state, "kimi")

	go func() {
		pollCtx, cancelPoll := context.WithCancel(ctx)
		defer cancelPoll()
		go watchOAuthSessionCancel(pollCtx, cancelPoll, state, "kimi")

		fmt.Println("Waiting for authentication...")
		authBundle, errWaitForAuthorization := kimiAuth.WaitForAuthorization(pollCtx, deviceFlow)
		if errWaitForAuthorization != nil {
			if !IsOAuthSessionPending(state, "kimi") {
				return
			}
			SetOAuthSessionError(state, oauthSessionErrorWithCause("Authentication failed", errWaitForAuthorization))
			fmt.Printf("Authentication failed: %v\n", errWaitForAuthorization)
			return
		}
		if !IsOAuthSessionPending(state, "kimi") {
			return
		}

		// Create token storage
		tokenStorage := kimiAuth.CreateTokenStorage(authBundle)

		metadata := map[string]any{
			"type":          "kimi",
			"access_token":  authBundle.TokenData.AccessToken,
			"refresh_token": authBundle.TokenData.RefreshToken,
			"token_type":    authBundle.TokenData.TokenType,
			"scope":         authBundle.TokenData.Scope,
			"timestamp":     time.Now().UnixMilli(),
		}
		if authBundle.TokenData.ExpiresAt > 0 {
			expired := time.Unix(authBundle.TokenData.ExpiresAt, 0).UTC().Format(time.RFC3339)
			metadata["expired"] = expired
		}
		if strings.TrimSpace(authBundle.DeviceID) != "" {
			metadata["device_id"] = strings.TrimSpace(authBundle.DeviceID)
		}

		fileName := fmt.Sprintf("kimi-%d.json", time.Now().UnixMilli())
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "kimi",
			FileName: fileName,
			Label:    "Kimi User",
			Storage:  tokenStorage,
			Metadata: metadata,
		}
		if errGuard := guardOAuthSessionPendingForSave(state, "kimi"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save authentication tokens: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}
		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		fmt.Println("You can now use Kimi services through this CLI")
		CompleteOAuthSession(state)
	}()

	response := gin.H{"status": "ok", "url": authURL, "state": state, "flow": "device"}
	if userCode := strings.TrimSpace(deviceFlow.UserCode); userCode != "" {
		response["user_code"] = userCode
	}
	if deviceFlow.ExpiresIn > 0 {
		response["expires_in"] = deviceFlow.ExpiresIn
	}
	c.JSON(200, response)
}

// RequestCursorToken starts Cursor PKCE login and polls until the user authorizes.
func (h *Handler) RequestCursorToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	fmt.Println("Initializing Cursor authentication...")

	authParams, err := cursor.GenerateAuthParams()
	if err != nil {
		log.Errorf("Failed to generate Cursor authorization URL: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return
	}

	state := fmt.Sprintf("csr-%d", time.Now().UnixNano())
	RegisterOAuthSession(state, "cursor")

	go func() {
		pollCtx, cancelPoll := context.WithCancel(ctx)
		defer cancelPoll()
		go watchOAuthSessionCancel(pollCtx, cancelPoll, state, "cursor")

		fmt.Println("Waiting for authentication...")
		tokens, errWait := cursor.PollForAuth(pollCtx, authParams.UUID, authParams.Verifier)
		if errWait != nil {
			if !IsOAuthSessionPending(state, "cursor") {
				return
			}
			SetOAuthSessionError(state, oauthSessionErrorWithCause("Authentication failed", errWait))
			fmt.Printf("Authentication failed: %v\n", errWait)
			return
		}
		if !IsOAuthSessionPending(state, "cursor") {
			return
		}

		expiresAt := cursor.GetTokenExpiry(tokens.AccessToken)
		sub := cursor.ParseJWTSub(tokens.AccessToken)
		subHash := cursor.SubToShortHash(sub)
		fileName := cursor.CredentialFileName("", subHash)

		metadata := map[string]any{
			"type":          "cursor",
			"access_token":  tokens.AccessToken,
			"refresh_token": tokens.RefreshToken,
			"expires_at":    expiresAt.Format(time.RFC3339),
			"timestamp":     time.Now().UnixMilli(),
		}
		if sub != "" {
			metadata["sub"] = sub
		}

		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "cursor",
			FileName: fileName,
			Label:    cursor.DisplayLabel("", subHash),
			Metadata: metadata,
		}
		if errGuard := guardOAuthSessionPendingForSave(state, "cursor"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save authentication tokens: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}
		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		fmt.Println("You can now use Cursor services through this CLI")
		CompleteOAuthSession(state)
	}()

	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": authParams.LoginURL, "state": state, "flow": "device"})
}

// RequestKiloToken implements GET /v0/management/kilo-auth-url.
// Kilo uses a device-code flow (no local callback): POST /api/device-auth/codes
// then poll GET /api/device-auth/codes/{code} until approved.
func (h *Handler) RequestKiloToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	fmt.Println("Initializing Kilo authentication...")

	state := fmt.Sprintf("kilo-%d", time.Now().UnixNano())
	authSvc := kiloauth.NewKiloAuth()

	deviceFlow, errStart := authSvc.InitiateDeviceFlow(ctx)
	if errStart != nil {
		log.Errorf("Failed to start Kilo device flow: %v", errStart)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start device authorization flow"})
		return
	}
	authURL := strings.TrimSpace(deviceFlow.VerificationURL)
	if authURL == "" {
		log.Error("Kilo device flow returned empty verification URL")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start device authorization flow"})
		return
	}

	RegisterOAuthSession(state, "kilo")

	go func() {
		pollCtx, cancelPoll := context.WithCancel(ctx)
		defer cancelPoll()
		go watchOAuthSessionCancel(pollCtx, cancelPoll, state, "kilo")

		fmt.Println("Waiting for Kilo authentication...")
		status, errWait := authSvc.PollForToken(pollCtx, deviceFlow.Code)
		if errWait != nil {
			if !IsOAuthSessionPending(state, "kilo") {
				return
			}
			log.Errorf("Kilo authentication failed: %v", errWait)
			SetOAuthSessionError(state, oauthSessionErrorWithCause("Authentication failed", errWait))
			return
		}
		if !IsOAuthSessionPending(state, "kilo") {
			return
		}
		if status == nil || strings.TrimSpace(status.Token) == "" {
			log.Error("Kilo token poll returned empty access token")
			SetOAuthSessionError(state, "Failed to exchange token")
			return
		}

		email := strings.TrimSpace(status.UserEmail)
		orgID := ""
		model := ""
		profile, errProfile := authSvc.GetProfile(pollCtx, status.Token)
		if errProfile != nil {
			log.Warnf("Kilo profile fetch failed: %v", errProfile)
		} else if profile != nil {
			if email == "" {
				email = strings.TrimSpace(profile.Email)
			}
			if len(profile.Orgs) > 0 {
				orgID = strings.TrimSpace(profile.Orgs[0].ID)
			}
		}
		if defaults, errDefaults := authSvc.GetDefaults(pollCtx, status.Token, orgID); errDefaults != nil {
			log.Warnf("Kilo defaults fetch failed: %v", errDefaults)
		} else if defaults != nil {
			model = strings.TrimSpace(defaults.Model)
		}
		if email == "" {
			email = "kilo"
		}

		ts := &kiloauth.KiloTokenStorage{
			Token:          status.Token,
			OrganizationID: orgID,
			Model:          model,
			Email:          email,
			Type:           "kilo",
		}
		fileName := kiloauth.CredentialFileName(email)
		label := email

		metadata := map[string]any{
			"type":                   "kilo",
			"kilocodeToken":          ts.Token,
			"kilocodeOrganizationId": ts.OrganizationID,
			"kilocodeModel":          ts.Model,
			"email":                  email,
			"auth_kind":              "oauth",
		}

		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "kilo",
			FileName: fileName,
			Label:    label,
			Storage:  ts,
			Metadata: metadata,
			Attributes: map[string]string{
				"auth_kind": "oauth",
			},
		}
		if errGuard := guardOAuthSessionPendingForSave(state, "kilo"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save Kilo token to file: %v", errSave)
			SetOAuthSessionError(state, "Failed to save token to file")
			return
		}

		CompleteOAuthSession(state)
		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		fmt.Println("You can now use Kilo services through this CLI")
	}()

	response := gin.H{"status": "ok", "url": authURL, "state": state, "flow": "device"}
	if userCode := strings.TrimSpace(deviceFlow.Code); userCode != "" {
		response["user_code"] = userCode
	}
	if deviceFlow.ExpiresIn > 0 {
		response["expires_in"] = deviceFlow.ExpiresIn
	} else {
		response["expires_in"] = 300
	}
	c.JSON(http.StatusOK, response)
}

// RequestKiroToken implements GET /v0/management/kiro-auth-url.
// Mirrors kiro-lb's dashboard device login: the operator picks Builder ID
// (default), Google, or GitHub via the provider query, approves in the
// browser, and the resulting refresh token is registered as a Kiro auth file.
// No local callback is needed — the management handler polls upstream.
func (h *Handler) RequestKiroToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	kind, errKind := kiro.ParseDeviceLoginKind(strings.TrimSpace(c.Query("provider")))
	if errKind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errKind.Error()})
		return
	}

	fmt.Printf("Initializing Kiro authentication (%s)...\n", kind)

	client := kiro.NewDeviceLoginClient(h.cfg)
	flow, errStart := client.Start(ctx, kind)
	if errStart != nil {
		log.Errorf("Failed to start Kiro device login: %v", errStart)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start device authorization flow"})
		return
	}

	authURL := strings.TrimSpace(flow.VerificationURIComplete)
	if authURL == "" {
		authURL = strings.TrimSpace(flow.VerificationURI)
	}
	if authURL == "" {
		log.Error("Kiro device login returned empty verification URL")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start device authorization flow"})
		return
	}

	state := fmt.Sprintf("kiro-%d", time.Now().UnixNano())
	RegisterOAuthSession(state, "kiro")

	go func() {
		pollCtx, cancelPoll := context.WithCancel(ctx)
		defer cancelPoll()
		go watchOAuthSessionCancel(pollCtx, cancelPoll, state, "kiro")

		fmt.Println("Waiting for Kiro authentication...")
		token, errWait := client.Wait(pollCtx, flow)
		if errWait != nil {
			if !IsOAuthSessionPending(state, "kiro") {
				return
			}
			log.Errorf("Kiro authentication failed: %v", errWait)
			SetOAuthSessionError(state, oauthSessionErrorWithCause("Authentication failed", errWait))
			return
		}
		if !IsOAuthSessionPending(state, "kiro") {
			return
		}

		tokenData := token.ToTokenData()
		storage := &kiro.KiroTokenStorage{
			Type:         "kiro",
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

		fileName := kiro.GenerateTokenFileName(tokenData)
		label := fmt.Sprintf("kiro-%s", tokenData.AuthMethod)
		if label == "kiro-" {
			label = "kiro"
		}

		metadata := map[string]any{
			"type":          "kiro",
			"access_token":  tokenData.AccessToken,
			"refresh_token": tokenData.RefreshToken,
			"profile_arn":   tokenData.ProfileArn,
			"expires_at":    tokenData.ExpiresAt,
			"auth_method":   tokenData.AuthMethod,
			"provider":      tokenData.Provider,
			"client_id":     tokenData.ClientID,
			"client_secret": tokenData.ClientSecret,
			"email":         tokenData.Email,
			"auth_kind":     "oauth",
		}
		if tokenData.Region != "" {
			metadata["region"] = tokenData.Region
		}
		if tokenData.StartURL != "" {
			metadata["start_url"] = tokenData.StartURL
		}

		attributes := map[string]string{
			"auth_kind": "oauth",
			"email":     tokenData.Email,
			"region":    tokenData.Region,
		}
		if tokenData.ProfileArn != "" {
			attributes["profile_arn"] = tokenData.ProfileArn
		}
		if tokenData.AuthMethod == "builder-id" {
			attributes["source"] = "aws-builder-id"
		} else if tokenData.AuthMethod != "" {
			attributes["source"] = "kiro-" + tokenData.AuthMethod
		} else {
			attributes["source"] = "kiro-device"
		}

		record := &coreauth.Auth{
			ID:         fileName,
			Provider:   "kiro",
			FileName:   fileName,
			Label:      label,
			Storage:    storage,
			Metadata:   metadata,
			Attributes: attributes,
		}

		if errGuard := guardOAuthSessionPendingForSave(state, "kiro"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save Kiro token to file: %v", errSave)
			SetOAuthSessionError(state, "Failed to save token to file")
			return
		}

		CompleteOAuthSession(state)
		fmt.Printf("Kiro authentication successful! Token saved to %s\n", savedPath)
	}()

	response := gin.H{
		"status": "ok",
		"url":    authURL,
		"state":  state,
		"flow":   "device",
	}
	if userCode := strings.TrimSpace(flow.UserCode); userCode != "" {
		response["user_code"] = userCode
	}
	expiresIn := int(flow.ExpiresIn.Seconds())
	if expiresIn <= 0 {
		expiresIn = 300
	}
	response["expires_in"] = expiresIn
	c.JSON(http.StatusOK, response)
}

// watchOAuthSessionCancel cancels pollCtx once the OAuth session is no longer pending.
func watchOAuthSessionCancel(pollCtx context.Context, cancel context.CancelFunc, state, provider string) {
	if cancel == nil {
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-pollCtx.Done():
			return
		case <-ticker.C:
			if !IsOAuthSessionPending(state, provider) {
				cancel()
				return
			}
		}
	}
}

// CancelAuthSession cancels a pending OAuth session identified by state.
// Protected by management auth. Safe for both callback and device-code flows:
// waiters check IsOAuthSessionPending and exit without saving credentials.
func (h *Handler) CancelAuthSession(c *gin.Context) {
	state := strings.TrimSpace(c.Query("state"))
	if state == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "missing state"})
		return
	}
	if err := ValidateOAuthState(state); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid state"})
		return
	}
	cancelled := CancelOAuthSession(state)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "cancelled": cancelled})
}

func (h *Handler) GetAuthStatus(c *gin.Context) {
	state := strings.TrimSpace(c.Query("state"))
	if state == "" {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}
	if err := ValidateOAuthState(state); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid state"})
		return
	}

	provider, status, isPlugin, metadata, completed, ok := GetOAuthSessionDetails(state)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"status": "error", "error": "unknown or expired state"})
		return
	}
	if completed {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}
	if status != "" {
		c.JSON(http.StatusOK, gin.H{"status": "error", "error": status})
		return
	}
	h.mu.Lock()
	host := h.pluginHost
	h.mu.Unlock()
	if isPlugin && host != nil && host.HasAuthProvider(provider) {
		ctx := PopulateAuthContext(context.Background(), c)
		resp, handled, errPoll := host.PollLogin(ctx, provider, state, metadata)
		if handled {
			if errPoll != nil {
				message := strings.TrimSpace(errPoll.Error())
				if message == "" {
					message = "Authentication failed"
				}
				SetOAuthSessionError(state, message)
				c.JSON(http.StatusOK, gin.H{"status": "error", "error": message})
				return
			}
			switch resp.Status {
			case "", pluginapi.AuthLoginStatusPending:
				c.JSON(http.StatusOK, gin.H{"status": "wait"})
				return
			case pluginapi.AuthLoginStatusError:
				message := strings.TrimSpace(resp.Message)
				if message == "" {
					message = "Authentication failed"
				}
				SetOAuthSessionError(state, message)
				c.JSON(http.StatusOK, gin.H{"status": "error", "error": message})
				return
			case pluginapi.AuthLoginStatusSuccess:
				records := pluginLoginPollAuths(host, resp)
				if len(records) == 0 {
					SetOAuthSessionError(state, "Authentication failed")
					c.JSON(http.StatusOK, gin.H{"status": "error", "error": "Authentication failed"})
					return
				}
				if errSave := h.savePluginLoginRecords(ctx, records); errSave != nil {
					log.WithError(errSave).WithField("provider", provider).Error("failed to save plugin auth tokens")
					SetOAuthSessionError(state, "Failed to save authentication tokens")
					c.JSON(http.StatusOK, gin.H{"status": "error", "error": "Failed to save authentication tokens"})
					return
				}
				CompleteOAuthSession(state)
				c.JSON(http.StatusOK, gin.H{"status": "ok"})
				return
			default:
				c.JSON(http.StatusOK, gin.H{"status": "wait"})
				return
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "wait"})
}

func pluginLoginPollAuths(host *pluginhost.Host, resp pluginapi.AuthLoginPollResponse) []*coreauth.Auth {
	if host == nil {
		return nil
	}
	authDatas := resp.Auths
	if len(authDatas) == 0 {
		authDatas = []pluginapi.AuthData{resp.Auth}
	}
	records := make([]*coreauth.Auth, 0, len(authDatas))
	for _, authData := range authDatas {
		record := host.AuthDataToCoreAuth(authData, "", "")
		if record == nil {
			return nil
		}
		records = append(records, record)
	}
	return records
}

func (h *Handler) savePluginLoginRecords(ctx context.Context, records []*coreauth.Auth) error {
	savedPaths := make([]string, 0, len(records))
	for _, record := range records {
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if strings.TrimSpace(savedPath) != "" {
			savedPaths = append(savedPaths, savedPath)
		}
		if errSave != nil {
			h.rollbackSavedTokenRecords(ctx, savedPaths)
			return errSave
		}
	}
	return nil
}

func (h *Handler) rollbackSavedTokenRecords(ctx context.Context, savedPaths []string) {
	for i := len(savedPaths) - 1; i >= 0; i-- {
		path := strings.TrimSpace(savedPaths[i])
		if path == "" {
			continue
		}
		if errDelete := h.deleteTokenRecord(ctx, path); errDelete != nil {
			log.WithError(errDelete).WithField("path", path).Warn("failed to roll back plugin auth token")
		}
		h.removeAuthsForPath(ctx, path, path)
	}
}

// PopulateAuthContext extracts request info and adds it to the context
func PopulateAuthContext(ctx context.Context, c *gin.Context) context.Context {
	info := &coreauth.RequestInfo{
		Query:   c.Request.URL.Query(),
		Headers: c.Request.Header,
	}
	return coreauth.WithRequestInfo(ctx, info)
}
