package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/workbuddy"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// WorkBuddyAuthenticator implements the browser OAuth polling flow for WorkBuddy.
type WorkBuddyAuthenticator struct{}

// NewWorkBuddyAuthenticator constructs a new WorkBuddy authenticator.
func NewWorkBuddyAuthenticator() Authenticator {
	return &WorkBuddyAuthenticator{}
}

// Provider returns the provider key for workbuddy.
func (WorkBuddyAuthenticator) Provider() string {
	return "workbuddy"
}

// workBuddyRefreshLead is the duration before token expiry when a refresh should be attempted.
var workBuddyRefreshLead = 24 * time.Hour

// RefreshLead returns how soon before expiry a refresh should be attempted.
func (WorkBuddyAuthenticator) RefreshLead() *time.Duration {
	return &workBuddyRefreshLead
}

// Login initiates the browser OAuth flow for WorkBuddy and returns the persisted auth record.
func (a WorkBuddyAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("workbuddy: configuration is required")
	}
	if opts == nil {
		opts = &LoginOptions{}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	authSvc := workbuddy.NewWorkBuddyAuth(cfg)

	authState, err := authSvc.FetchAuthState(ctx)
	if err != nil {
		return nil, fmt.Errorf("workbuddy: failed to fetch auth state: %w", err)
	}

	fmt.Printf("\nPlease open the following URL in your browser to login:\n\n  %s\n\n", authState.AuthURL)
	fmt.Println("Waiting for authorization...")

	if !opts.NoBrowser {
		if browser.IsAvailable() {
			if errOpen := browser.OpenURL(authState.AuthURL); errOpen != nil {
				log.Debugf("workbuddy: failed to open browser: %v", errOpen)
			}
		}
	}

	storage, err := authSvc.PollForToken(ctx, authState.State)
	if err != nil {
		return nil, fmt.Errorf("workbuddy: %s: %w", workbuddy.GetUserFriendlyMessage(err), err)
	}

	// Resolve the account identity used by WorkBuddy request headers. Identity lookup
	// failure is non-fatal: the JWT sub claim is a valid fallback.
	if account, errAccount := authSvc.FetchAccountIdentity(ctx, authState.State, storage.AccessToken); errAccount != nil {
		log.Warnf("workbuddy: failed to fetch account identity: %v", errAccount)
	} else {
		storage.UserID = account.UserID
		storage.EnterpriseID = account.EnterpriseID
		storage.Nickname = account.Nickname
	}
	if storage.UserID == "" {
		storage.UserID, _ = authSvc.DecodeUserID(storage.AccessToken)
	}

	authID := fmt.Sprintf("workbuddy-%s.json", storage.UserID)
	if storage.UserID == "" {
		authID = fmt.Sprintf("workbuddy-%s.json", uuid.NewString()[:8])
	}

	label := storage.Nickname
	if label == "" {
		label = storage.UserID
	}
	if label == "" {
		label = "workbuddy-user"
	}

	fmt.Printf("\nSuccessfully logged in! (User ID: %s)\n", storage.UserID)

	return &coreauth.Auth{
		ID:       authID,
		Provider: a.Provider(),
		FileName: authID,
		Label:    label,
		Storage:  storage,
		Metadata: map[string]any{
			"access_token":  storage.AccessToken,
			"refresh_token": storage.RefreshToken,
			"uid":           storage.UserID,
			"domain":        storage.Domain,
			"realm":         storage.Realm,
			"expires_at":    storage.ExpiresAt,
			"enterprise_id": storage.EnterpriseID,
			"nickname":      storage.Nickname,
			"device_token":  storage.DeviceToken,
		},
	}, nil
}
