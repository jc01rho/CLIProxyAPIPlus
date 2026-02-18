package auth

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/cline"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// ClineAuthenticator implements the login flow for Cline accounts.
type ClineAuthenticator struct{}

// NewClineAuthenticator constructs a Cline authenticator.
func NewClineAuthenticator() *ClineAuthenticator {
	return &ClineAuthenticator{}
}

func (a *ClineAuthenticator) Provider() string {
	return "cline"
}

func (a *ClineAuthenticator) RefreshLead() *time.Duration {
	lead := 5 * time.Minute
	return &lead
}

// Login manages the OAuth authentication flow for Cline.
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

	clineAuth := cline.NewClineAuth()

	port := 48801
	if opts.CallbackPort > 0 {
		port = opts.CallbackPort
	}

	var listener net.Listener
	var err error
	for p := port; p <= 48811; p++ {
		listener, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			port = p
			listener.Close()
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("failed to find available port: %w", err)
	}

	callbackURL := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	fmt.Println("Initiating Cline OAuth authentication...")
	authURL, state, err := clineAuth.InitiateOAuth(ctx, callbackURL)
	if err != nil {
		return nil, fmt.Errorf("failed to initiate OAuth: %w", err)
	}

	fmt.Printf("\nTo authenticate, please visit: %s\n\n", authURL)

	if !opts.NoBrowser {
		if browser.IsAvailable() {
			if errOpen := browser.OpenURL(authURL); errOpen != nil {
				log.Warnf("Failed to open browser automatically: %v", errOpen)
			}
		}
	}

	fmt.Println("Waiting for authorization...")
	code, callbackState, err := clineAuth.StartCallbackServer(ctx, port)
	if err != nil {
		return nil, fmt.Errorf("failed to receive callback: %w", err)
	}

	// State verification: only check if both sides provided state
	if state != "" && callbackState != "" && callbackState != state {
		return nil, fmt.Errorf("state mismatch: expected %s, got %s", state, callbackState)
	}

	// Try server-side token exchange first, fall back to direct parsing
	tokenResp, err := clineAuth.ExchangeCode(ctx, code, callbackURL)
	if err != nil {
		log.Warnf("Cline ExchangeCode failed, trying direct token parsing: %v", err)
		tokenResp, err = cline.ParseCallbackToken(code)
		if err != nil {
			return nil, fmt.Errorf("failed to parse callback token: %w", err)
		}
	}

	fmt.Printf("Authentication successful for %s\n", tokenResp.UserInfo.Email)

	ts := &cline.ClineTokenStorage{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresAt:    tokenResp.ExpiresAt,
		Email:        tokenResp.UserInfo.Email,
		UserID:       tokenResp.UserInfo.ID,
		DisplayName:  tokenResp.UserInfo.DisplayName,
		Type:         "cline",
	}

	fileName := cline.CredentialFileName(tokenResp.UserInfo.Email)
	metadata := map[string]any{
		"email":        tokenResp.UserInfo.Email,
		"userId":       tokenResp.UserInfo.ID,
		"displayName":  tokenResp.UserInfo.DisplayName,
		"accessToken":  tokenResp.AccessToken,
		"refreshToken": tokenResp.RefreshToken,
		"expiresAt":    tokenResp.ExpiresAt,
	}

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Storage:  ts,
		Metadata: metadata,
	}, nil
}
