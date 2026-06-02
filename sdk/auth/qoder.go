package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// qoderRefreshLead is the duration before token expiry when a refresh should be triggered.
var qoderRefreshLead = 5 * time.Minute

// QoderAuthenticator implements the OAuth device flow + PKCE login for Qoder.
type QoderAuthenticator struct{}

// NewQoderAuthenticator constructs a new Qoder authenticator.
func NewQoderAuthenticator() Authenticator {
	return &QoderAuthenticator{}
}

// Provider returns the provider key for qoder.
func (QoderAuthenticator) Provider() string {
	return "qoder"
}

// RefreshLead returns the duration before expiry when a token refresh should occur.
func (QoderAuthenticator) RefreshLead() *time.Duration {
	return &qoderRefreshLead
}

// Login initiates Qoder authentication via device flow + PKCE, or via PAT exchange
// when opts.Metadata["personal_token"] is set.
func (a QoderAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	authSvc := qoder.NewQoderAuth(cfg)

	// PAT exchange path: skip device flow when a personal token is provided.
	if pat := opts.Metadata["personal_token"]; pat != "" {
		return a.loginWithPAT(ctx, authSvc, pat)
	}

	return a.loginWithDeviceFlow(ctx, authSvc, opts)
}

// loginWithDeviceFlow runs the browser-based device flow + PKCE authentication.
func (a QoderAuthenticator) loginWithDeviceFlow(ctx context.Context, authSvc *qoder.QoderAuth, opts *LoginOptions) (*coreauth.Auth, error) {
	fmt.Println("Starting Qoder authentication...")

	flow, err := authSvc.StartDeviceFlow(ctx)
	if err != nil {
		return nil, fmt.Errorf("qoder: failed to start device flow: %w", err)
	}

	fmt.Printf("\nTo authenticate, please visit:\n%s\n\n", flow.VerificationURI)

	if !opts.NoBrowser {
		if browser.IsAvailable() {
			if errOpen := browser.OpenURL(flow.VerificationURI); errOpen != nil {
				log.Warnf("Failed to open browser automatically: %v", errOpen)
			} else {
				fmt.Println("Browser opened automatically.")
			}
		}
	}

	fmt.Println("Waiting for Qoder authorization...")
	if flow.ExpiresIn > 0 {
		fmt.Printf("(This will timeout in %d seconds if not authorized)\n", flow.ExpiresIn)
	}

	authBundle, err := authSvc.WaitForAuthorization(ctx, flow)
	if err != nil {
		return nil, fmt.Errorf("qoder: %s", qoder.GetUserFriendlyMessage(err))
	}

	return a.buildAuth(authSvc, authBundle)
}

// loginWithPAT exchanges a personal access token for a session token.
func (a QoderAuthenticator) loginWithPAT(ctx context.Context, authSvc *qoder.QoderAuth, pat string) (*coreauth.Auth, error) {
	fmt.Println("Exchanging Qoder personal access token...")

	authBundle, err := authSvc.ExchangePAT(ctx, pat)
	if err != nil {
		return nil, fmt.Errorf("qoder: %s", qoder.GetUserFriendlyMessage(err))
	}

	return a.buildAuth(authSvc, authBundle)
}

// buildAuth constructs the coreauth.Auth from a completed auth bundle.
func (a QoderAuthenticator) buildAuth(authSvc *qoder.QoderAuth, bundle *qoder.QoderAuthBundle) (*coreauth.Auth, error) {
	tokenStorage := authSvc.CreateTokenStorage(bundle)

	label := bundle.TokenData.UserName
	if label == "" {
		label = bundle.TokenData.UserID
	}
	if label == "" {
		label = "qoder-user"
	}

	metadata := map[string]any{
		"type":          "qoder",
		"user_id":       bundle.TokenData.UserID,
		"user_name":     bundle.TokenData.UserName,
		"access_token":  bundle.TokenData.AccessToken,
		"refresh_token": bundle.TokenData.RefreshToken,
		"timestamp":     time.Now().UnixMilli(),
	}
	if bundle.TokenData.ExpiresAt != "" {
		metadata["expires_at"] = bundle.TokenData.ExpiresAt
	}

	fileName := fmt.Sprintf("qoder-%s.json", sanitizeFileName(label))

	fmt.Printf("\nQoder authentication successful for user: %s\n", label)

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Label:    label,
		Storage:  tokenStorage,
		Metadata: metadata,
	}, nil
}

// sanitizeFileName replaces characters that are unsafe in file names.
func sanitizeFileName(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			out = append(out, c)
		} else {
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "user"
	}
	return string(out)
}

// RefreshQoderToken refreshes the Qoder access token using the stored refresh token.
func RefreshQoderToken(ctx context.Context, cfg *config.Config, storage *qoder.QoderTokenStorage) error {
	if storage == nil || storage.RefreshToken == "" {
		return fmt.Errorf("qoder: no refresh token available")
	}

	authSvc := qoder.NewQoderAuth(cfg)
	tokenData, err := authSvc.RefreshToken(ctx, storage.RefreshToken)
	if err != nil {
		return fmt.Errorf("qoder: token refresh failed: %w", err)
	}

	storage.AccessToken = tokenData.AccessToken
	if tokenData.RefreshToken != "" {
		storage.RefreshToken = tokenData.RefreshToken
	}
	if tokenData.ExpiresAt != "" {
		storage.ExpiresAt = tokenData.ExpiresAt
	}
	if tokenData.RefreshTokenExpiresAt != "" {
		storage.RefreshTokenExpiresAt = tokenData.RefreshTokenExpiresAt
	}

	return nil
}

// ImportFromCredentialFile reads a Qoder CLI credential file and converts
// it to a coreauth.Auth suitable for registration in the auth manager.
// The loader tries the well-known default paths (~/.qoder/.auth/user,
// ~/.qoderwork/.auth/user) and picks the first one that exists.
func (a QoderAuthenticator) ImportFromCredentialFile() (*coreauth.Auth, error) {
	storage, path, err := qoder.LoadCredentialFromAnyDefault()
	if err != nil {
		return nil, fmt.Errorf("qoder: credential file import failed: %w", err)
	}

	label := storage.UserName
	if label == "" {
		label = storage.UserID
	}
	if label == "" {
		label = "qoder-user"
	}

	now := time.Now()
	fileName := fmt.Sprintf("qoder-imported-%s.json", sanitizeFileName(label))

	metadata := map[string]any{
		"type":          "qoder",
		"user_id":       storage.UserID,
		"user_name":     storage.UserName,
		"access_token":  storage.AccessToken,
		"refresh_token": storage.RefreshToken,
		"timestamp":     now.UnixMilli(),
	}
	if storage.ExpiresAt != "" {
		metadata["expires_at"] = storage.ExpiresAt
	}

	fmt.Printf("Imported Qoder credentials from %s (user: %s)\n", path, label)

	return &coreauth.Auth{
		ID:        fileName,
		Provider:  a.Provider(),
		FileName:  fileName,
		Label:     label,
		Storage:   storage,
		Metadata:  metadata,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

