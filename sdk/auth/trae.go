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
