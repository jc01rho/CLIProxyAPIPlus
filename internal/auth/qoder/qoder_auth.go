// Package qoder provides authentication and token management for the Qoder API.
// It handles the device flow + PKCE authentication and token refresh lifecycle.
package qoder

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// QoderAuth handles the Qoder authentication flow.
type QoderAuth struct {
	deviceClient *DeviceFlowClient
	cfg          *config.Config
}

// NewQoderAuth creates a new QoderAuth service instance.
func NewQoderAuth(cfg *config.Config) *QoderAuth {
	return &QoderAuth{
		deviceClient: NewDeviceFlowClient(cfg),
		cfg:          cfg,
	}
}

// StartDeviceFlow initiates the device flow by generating PKCE material and the browser URL.
func (q *QoderAuth) StartDeviceFlow(ctx context.Context) (*QoderDeviceFlow, error) {
	return q.deviceClient.StartDeviceFlow(ctx)
}

// WaitForAuthorization polls until the user authorizes the device flow and returns the auth bundle.
func (q *QoderAuth) WaitForAuthorization(ctx context.Context, flow *QoderDeviceFlow) (*QoderAuthBundle, error) {
	tokenData, err := q.deviceClient.PollForToken(ctx, flow)
	if err != nil {
		return nil, err
	}
	return &QoderAuthBundle{TokenData: tokenData}, nil
}

// RefreshToken exchanges a refresh token for a new access token.
func (q *QoderAuth) RefreshToken(ctx context.Context, refreshToken string) (*QoderTokenData, error) {
	return q.deviceClient.RefreshToken(ctx, refreshToken)
}

// ExchangePAT exchanges a personal access token (pt-xxx) for a session token.
func (q *QoderAuth) ExchangePAT(ctx context.Context, personalToken string) (*QoderAuthBundle, error) {
	tokenData, err := q.deviceClient.ExchangePAT(ctx, personalToken)
	if err != nil {
		return nil, err
	}
	return &QoderAuthBundle{TokenData: tokenData}, nil
}

// CreateTokenStorage builds a QoderTokenStorage from an auth bundle.
func (q *QoderAuth) CreateTokenStorage(bundle *QoderAuthBundle) *QoderTokenStorage {
	return &QoderTokenStorage{
		UserID:                bundle.TokenData.UserID,
		UserName:              bundle.TokenData.UserName,
		AccessToken:           bundle.TokenData.AccessToken,
		RefreshToken:          bundle.TokenData.RefreshToken,
		ExpiresAt:             bundle.TokenData.ExpiresAt,
		RefreshTokenExpiresAt: bundle.TokenData.RefreshTokenExpiresAt,
		Type:                  "qoder",
	}
}
