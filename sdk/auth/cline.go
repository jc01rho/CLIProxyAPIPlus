package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cline"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Metadata keys persisted into coreauth.Auth.Metadata for the Cline provider.
const (
	metadataClineEmail      = "email"
	metadataClineExpiresAt  = "expiresAt"
	metadataClineUserID     = "userId"
	metadataClineWorkOSPref = "workos_prefixed"
)

const defaultClineCallbackPort = 1455

// ClineAuthenticator implements the shared authenticator interface for Cline.
type ClineAuthenticator struct {
	CallbackPort int
}

// NewClineAuthenticator creates a Cline authenticator with the default local
// callback port.
func NewClineAuthenticator() *ClineAuthenticator {
	return &ClineAuthenticator{CallbackPort: defaultClineCallbackPort}
}

// Provider returns the provider identifier.
func (a *ClineAuthenticator) Provider() string {
	return "cline"
}

// RefreshLead returns the refresh lead (5 minutes) used by the scheduler.
func (a *ClineAuthenticator) RefreshLead() *time.Duration {
	d := 5 * time.Minute
	return &d
}

// Login walks the user through the Cline OAuth flow and returns a fully
// populated coreauth.Auth ready to be persisted.
func (a *ClineAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	callbackPort := a.CallbackPort
	if opts.CallbackPort > 0 {
		callbackPort = opts.CallbackPort
	}

	state, err := misc.GenerateRandomState()
	if err != nil {
		return nil, fmt.Errorf("cline state generation failed: %w", err)
	}

	callbackURL := fmt.Sprintf("http://localhost:%d/callback", callbackPort)
	authSvc := cline.NewClineAuth(cfg)
	authURL := authSvc.GenerateAuthURL(state, callbackURL)

	if !opts.NoBrowser {
		fmt.Println("Opening browser for Cline authentication")
		if !browser.IsAvailable() {
			log.Warn("No browser available; please open the URL manually")
			util.PrintSSHTunnelInstructions(callbackPort)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		} else if err = browser.OpenURL(authURL); err != nil {
			log.Warnf("Failed to open browser automatically: %v", err)
			util.PrintSSHTunnelInstructions(callbackPort)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		}
	} else {
		util.PrintSSHTunnelInstructions(callbackPort)
		fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
	}

	fmt.Println("Waiting for Cline authentication callback...")
	result, err := waitForClineCallback(ctx, callbackPort, opts.Prompt)
	if err != nil {
		return nil, err
	}

	if result.Error != "" {
		if result.ErrorDescription != "" {
			return nil, fmt.Errorf("cline oauth error: %s (%s)", result.Error, result.ErrorDescription)
		}
		return nil, fmt.Errorf("cline oauth error: %s", result.Error)
	}
	// Cline may not return state in callback, only validate if both are present
	if result.State != "" && state != "" && result.State != state {
		return nil, fmt.Errorf("cline authentication failed: state mismatch")
	}

	tokenResp, err := decodeClineCodeOrExchange(ctx, authSvc, result.Code, callbackURL)
	if err != nil {
		return nil, err
	}
	if tokenResp == nil || strings.TrimSpace(tokenResp.AccessToken) == "" {
		return nil, fmt.Errorf("cline authentication failed: no access token in response")
	}

	email := strings.TrimSpace(tokenResp.UserEmail())
	if email == "" {
		return nil, fmt.Errorf("cline authentication failed: missing account email")
	}

	expiresAtInt := cline.ParseExpiresAt(tokenResp.ExpiresAt)

	// Strip the workos: prefix from the access token for storage; the
	// Authorization header is rebuilt with the prefix by the executor.
	storedAccess := strings.TrimPrefix(strings.TrimSpace(tokenResp.AccessToken), "workos:")

	ts := &cline.ClineTokenStorage{
		AccessToken:  storedAccess,
		RefreshToken: strings.TrimSpace(tokenResp.RefreshToken),
		ExpiresAt:    expiresAtInt,
		Email:        email,
		Type:         "cline",
	}

	fileName := cline.CredentialFileName(email)
	metadata := cline.SyncMetadata(ts, map[string]any{
		metadataClineEmail:      email,
		metadataClineWorkOSPref: true,
	})

	fmt.Printf("Cline authentication successful for %s\n", email)

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Storage:  ts,
		Metadata: metadata,
	}, nil
}

// Refresh exchanges the refresh token on the storage and returns a new auth
// record with updated tokens and expiry. The metadata on the supplied auth is
// mutated in-place via cline.SyncMetadata so subsequent requests and reload
// see the rotated values with both snake_case and camelCase keys populated.
func (a *ClineAuthenticator) Refresh(ctx context.Context, cfg *config.Config, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("cline refresh: auth is nil")
	}
	storage, ok := auth.Storage.(*cline.ClineTokenStorage)
	if !ok || storage == nil {
		return nil, fmt.Errorf("cline refresh: invalid token storage")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	authSvc := cline.NewClineAuth(cfg)
	tokenResp, err := authSvc.RefreshToken(ctx, storage.RefreshToken)
	if err != nil {
		return nil, fmt.Errorf("cline refresh: %w", err)
	}
	if tokenResp == nil || strings.TrimSpace(tokenResp.AccessToken) == "" {
		return nil, fmt.Errorf("cline refresh: empty access token in response")
	}

	newAccess := strings.TrimPrefix(strings.TrimSpace(tokenResp.AccessToken), "workos:")
	storage.AccessToken = newAccess
	if tokenResp.RefreshToken != "" {
		storage.RefreshToken = strings.TrimSpace(tokenResp.RefreshToken)
	}
	expiresAt := cline.ParseExpiresAt(tokenResp.ExpiresAt)
	if expiresAt > 0 {
		storage.ExpiresAt = expiresAt
	}
	if email := strings.TrimSpace(tokenResp.UserEmail()); email != "" {
		storage.Email = email
	}

	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	cline.SyncMetadata(storage, auth.Metadata)
	auth.Metadata[metadataClineWorkOSPref] = true

	return auth, nil
}

// decodeClineCodeOrExchange implements the 9Router contract: the Cline callback
// delivers the token payload base64-encoded inside the `code` query parameter.
// If the payload parses cleanly we use it; otherwise we fall back to a server
// round-trip via /api/v1/auth/token.
func decodeClineCodeOrExchange(ctx context.Context, authSvc *cline.ClineAuth, code, callbackURL string) (*cline.TokenResponse, error) {
	if tokenResp, ok := decodeClineBase64Token(code); ok {
		return tokenResp, nil
	}
	tokenResp, err := authSvc.ExchangeCode(ctx, code, callbackURL)
	if err != nil {
		return nil, fmt.Errorf("cline token exchange failed: %w", err)
	}
	return tokenResp, nil
}

// decodeClineBase64Token mirrors 9Router's `Buffer.from(base64)` parsing:
// pad to a multiple of 4, decode (trying Std first, then URL), then
// JSON-parse up to the final `}` to drop any trailing junk.
func decodeClineBase64Token(code string) (*cline.TokenResponse, bool) {
	trimmed := strings.TrimSpace(code)
	if trimmed == "" {
		return nil, false
	}
	padding := 4 - (len(trimmed) % 4)
	if padding != 4 {
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
	payload := string(decoded)[:lastBrace+1]
	var tokenResp cline.TokenResponse
	if err := json.Unmarshal([]byte(payload), &tokenResp); err != nil {
		return nil, false
	}
	if strings.TrimSpace(tokenResp.AccessToken) == "" {
		return nil, false
	}
	return &tokenResp, true
}

type clineOAuthResult struct {
	Code             string
	State            string
	Error            string
	ErrorDescription string
}

func waitForClineCallback(ctx context.Context, callbackPort int, prompt func(prompt string) (string, error)) (*clineOAuthResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	resultCh := make(chan *clineOAuthResult, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	server := &http.Server{
		Addr:              ":" + strconv.Itoa(callbackPort),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		res := &clineOAuthResult{
			Code:             strings.TrimSpace(q.Get("code")),
			State:            strings.TrimSpace(q.Get("state")),
			Error:            strings.TrimSpace(q.Get("error")),
			ErrorDescription: strings.TrimSpace(q.Get("error_description")),
		}

		select {
		case resultCh <- res:
		default:
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body><h2>Cline login complete</h2><p>You can close this window and return to CLI.</p></body></html>"))
	})

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("cline callback server failed: %w", err)
		}
	}()

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Warnf("cline callback server shutdown error: %v", err)
		}
	}()

	var manualTimer *time.Timer
	var manualTimerC <-chan time.Time
	if prompt != nil {
		manualTimer = time.NewTimer(15 * time.Second)
		manualTimerC = manualTimer.C
		defer manualTimer.Stop()
	}

	timeout := cline.AuthTimeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}
	timeoutTimer := time.NewTimer(timeout)
	defer timeoutTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timeoutTimer.C:
			return nil, fmt.Errorf("cline callback wait timeout after %s", timeout.String())
		case err := <-errCh:
			return nil, err
		case res := <-resultCh:
			return res, nil
		case <-manualTimerC:
			manualTimerC = nil
			input, err := prompt("Paste the Cline callback URL (or press Enter to keep waiting): ")
			if err != nil {
				return nil, err
			}
			parsed, err := misc.ParseOAuthCallback(input)
			if err != nil {
				return nil, err
			}
			if parsed == nil {
				continue
			}
			return &clineOAuthResult{
				Code:             parsed.Code,
				State:            parsed.State,
				Error:            parsed.Error,
				ErrorDescription: parsed.ErrorDescription,
			}, nil
		}
	}
}
