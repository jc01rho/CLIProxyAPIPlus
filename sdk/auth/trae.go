package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
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
	// Login logic is currently handled in management handlers for Trae.
	// This serves as a placeholder to satisfy the Authenticator interface.
	return nil, fmt.Errorf("trae login not implemented via Authenticator interface yet")
}
