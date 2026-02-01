package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/kilocode"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// KilocodeAuthenticator implements the device flow login for Kilocode.
type KilocodeAuthenticator struct{}

// NewKilocodeAuthenticator constructs a new Kilocode authenticator.
func NewKilocodeAuthenticator() Authenticator {
	return &KilocodeAuthenticator{}
}

// Provider returns the provider key for kilocode.
func (KilocodeAuthenticator) Provider() string {
	return "kilocode"
}

// RefreshLead returns nil since Kilocode tokens don't expire traditionally.
func (KilocodeAuthenticator) RefreshLead() *time.Duration {
	return nil
}

// Login initiates the device flow authentication for Kilocode.
func (a KilocodeAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	authSvc := kilocode.NewKilocodeAuth(cfg)

	// Start the device flow
	fmt.Println("Starting Kilocode authentication...")
	deviceCode, err := authSvc.StartDeviceFlow(ctx)
	if err != nil {
		return nil, fmt.Errorf("kilocode: failed to start device flow: %w", err)
	}

	// Display the user code and verification URL
	fmt.Printf("\nTo authenticate, please visit: %s\n", deviceCode.VerificationURL)
	fmt.Printf("And enter the code: %s\n\n", deviceCode.Code)

	// Try to open the browser automatically
	if !opts.NoBrowser {
		if browser.IsAvailable() {
			if errOpen := browser.OpenURL(deviceCode.VerificationURL); errOpen != nil {
				log.Warnf("Failed to open browser automatically: %v", errOpen)
			}
		}
	}

	fmt.Println("Waiting for Kilocode authorization...")
	fmt.Printf("(This will timeout in %d seconds if not authorized)\n", deviceCode.ExpiresIn)

	// Wait for user authorization
	authBundle, err := authSvc.WaitForAuthorization(ctx, deviceCode)
	if err != nil {
		errMsg := kilocode.GetUserFriendlyMessage(err)
		return nil, fmt.Errorf("kilocode: %s", errMsg)
	}

	// Create the token storage
	tokenStorage := authSvc.CreateTokenStorage(authBundle)

	// Build metadata with token information for the executor
	metadata := map[string]any{
		"type":      "kilocode",
		"user_id":   authBundle.UserID,
		"email":     authBundle.UserEmail,
		"token":     authBundle.Token,
		"timestamp": time.Now().UnixMilli(),
	}

	fileName := fmt.Sprintf("kilocode-%s.json", authBundle.UserID)
	label := authBundle.UserEmail
	if label == "" {
		label = authBundle.UserID
	}

	fmt.Printf("\nKilocode authentication successful for user: %s\n", label)

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Label:    label,
		Storage:  tokenStorage,
		Metadata: metadata,
	}, nil
}
