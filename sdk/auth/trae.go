package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/trae"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// TraeAuthenticator implements the OAuth login flow for Trae accounts.
type TraeAuthenticator struct {
	CallbackPort int
}

// NewTraeAuthenticator constructs a Trae authenticator with default settings.
func NewTraeAuthenticator() *TraeAuthenticator {
	return &TraeAuthenticator{CallbackPort: 9877}
}

func (a *TraeAuthenticator) Provider() string {
	return "trae"
}

func (a *TraeAuthenticator) RefreshLead() *time.Duration {
	d := 20 * time.Minute
	return &d
}

func (a *TraeAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	authSvc := trae.NewTraeAuth(cfg)

	pkceCodes, err := trae.GeneratePKCECodes()
	if err != nil {
		return nil, fmt.Errorf("trae: failed to generate PKCE codes: %w", err)
	}

	server := trae.NewOAuthServer(a.CallbackPort)
	if err := server.Start(); err != nil {
		return nil, fmt.Errorf("trae: failed to start OAuth server: %w", err)
	}
	defer func() {
		_ = server.Stop(context.Background())
	}()

	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", a.CallbackPort)
	state := fmt.Sprintf("trae-%d", time.Now().UnixNano())
	authURL, _, err := authSvc.GenerateAuthURL(redirectURI, state, pkceCodes)
	if err != nil {
		return nil, fmt.Errorf("trae: failed to generate auth URL: %w", err)
	}

	if !opts.NoBrowser {
		fmt.Println("Opening browser for Trae authentication")
		if !browser.IsAvailable() {
			log.Warn("No browser available; please open the URL manually")
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		} else if err = browser.OpenURL(authURL); err != nil {
			log.Warnf("Failed to open browser automatically: %v", err)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		}
	} else {
		fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
	}

	fmt.Println("Waiting for Trae authentication...")

	result, err := server.WaitForCallback(5 * time.Minute)
	if err != nil {
		return nil, fmt.Errorf("trae: authentication timeout or error: %w", err)
	}

	if result.Error != "" {
		return nil, fmt.Errorf("trae: OAuth error: %s", result.Error)
	}

	bundle, err := authSvc.ExchangeCodeForTokens(ctx, redirectURI, result.Code, result.State, pkceCodes)
	if err != nil {
		return nil, fmt.Errorf("trae: failed to exchange code for tokens: %w", err)
	}

	tokenStorage := authSvc.CreateTokenStorage(&bundle.TokenData)

	email := ""
	if opts.Metadata != nil {
		email = opts.Metadata["email"]
		if email == "" {
			email = opts.Metadata["alias"]
		}
	}

	if email == "" && bundle.TokenData.Email != "" {
		email = bundle.TokenData.Email
	}

	if email == "" && opts.Prompt != nil {
		email, err = opts.Prompt("Please input your email address or alias for Trae:")
		if err != nil {
			return nil, err
		}
	}

	email = strings.TrimSpace(email)
	if email == "" {
		return nil, &EmailRequiredError{Prompt: "Please provide an email address or alias for Trae."}
	}

	tokenStorage.Email = email

	fileName := fmt.Sprintf("trae-%s.json", tokenStorage.Email)
	metadata := map[string]any{
		"email": tokenStorage.Email,
	}

	fmt.Println("Trae authentication successful")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Storage:  tokenStorage,
		Metadata: metadata,
	}, nil
}

const traeAppVersion = "2.3.6266"

// LoginWithNative performs Trae authentication using the Native OAuth flow.
// This uses the /authorize endpoint instead of /callback for handling the token exchange.
func (a *TraeAuthenticator) LoginWithNative(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	// Create OAuth server for native callback
	server := trae.NewOAuthServer(a.CallbackPort)
	if err := server.Start(); err != nil {
		return nil, fmt.Errorf("trae: failed to start OAuth server: %w", err)
	}
	defer func() {
		_ = server.Stop(context.Background())
	}()

	// Generate native auth URL with /authorize callback
	callbackURL := fmt.Sprintf("http://127.0.0.1:%d/authorize", a.CallbackPort)
	authURL, loginTraceID, err := trae.GenerateNativeAuthURL(callbackURL, traeAppVersion)
	if err != nil {
		return nil, fmt.Errorf("trae: failed to generate native auth URL: %w", err)
	}

	log.Debugf("Generated native auth URL with login trace ID: %s", loginTraceID)

	// Open browser for authentication
	if !opts.NoBrowser {
		fmt.Println("Opening browser for Trae Native OAuth authentication")
		if !browser.IsAvailable() {
			log.Warn("No browser available; please open the URL manually")
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		} else if err = browser.OpenURL(authURL); err != nil {
			log.Warnf("Failed to open browser automatically: %v", err)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		}
	} else {
		fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
	}

	fmt.Println("Waiting for Trae Native OAuth authentication...")

	// Wait for native callback
	result, err := server.WaitForNativeCallback(5 * time.Minute)
	if err != nil {
		return nil, fmt.Errorf("trae: native authentication timeout or error: %w", err)
	}

	if result.Error != "" {
		return nil, fmt.Errorf("trae: native OAuth error: %s", result.Error)
	}

	// Extract tokens from native result
	if result.UserJWT == nil {
		return nil, fmt.Errorf("trae: no user JWT received from native callback")
	}

	// Create token storage from native OAuth result
	tokenStorage := &trae.TraeTokenStorage{
		AccessToken:  result.UserJWT.Token,
		RefreshToken: result.UserJWT.RefreshToken,
		LastRefresh:  fmt.Sprintf("%d", time.Now().Unix()),
		Type:         "trae",
		Expire:       fmt.Sprintf("%d", result.UserJWT.TokenExpireAt),
	}

	// Extract email from user info or prompt
	email := ""
	if result.UserInfo != nil && result.UserInfo.ScreenName != "" {
		email = result.UserInfo.ScreenName
	}

	if opts.Metadata != nil {
		if metaEmail := opts.Metadata["email"]; metaEmail != "" {
			email = metaEmail
		} else if alias := opts.Metadata["alias"]; alias != "" {
			email = alias
		}
	}

	if email == "" && opts.Prompt != nil {
		email, err = opts.Prompt("Please input your email address or alias for Trae:")
		if err != nil {
			return nil, err
		}
	}

	email = strings.TrimSpace(email)
	if email == "" {
		return nil, &EmailRequiredError{Prompt: "Please provide an email address or alias for Trae."}
	}

	tokenStorage.Email = email

	fileName := fmt.Sprintf("trae-%s.json", tokenStorage.Email)
	metadata := map[string]any{
		"email": tokenStorage.Email,
	}

	fmt.Println("Trae Native OAuth authentication successful")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Storage:  tokenStorage,
		Metadata: metadata,
	}, nil
}
