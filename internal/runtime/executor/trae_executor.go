package executor

import (
	"context"
	"fmt"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/trae"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type TraeExecutor struct {
	cfg *config.Config
}

func NewTraeExecutor(cfg *config.Config) *TraeExecutor {
	return &TraeExecutor{cfg: cfg}
}

func (e *TraeExecutor) Provider() string {
	return "trae"
}

func (e *TraeExecutor) Identifier() string {
	return "trae"
}

func (e *TraeExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("trae: Execute not implemented")
}

func (e *TraeExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	return nil, fmt.Errorf("trae: ExecuteStream not implemented")
}

func (e *TraeExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("trae: CountTokens not implemented")
}

func (e *TraeExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("trae executor: auth is nil")
	}
	var refreshToken string
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["refresh_token"].(string); ok && v != "" {
			refreshToken = v
		}
	}
	if refreshToken == "" && auth.Attributes != nil {
		refreshToken = auth.Attributes["refresh_token"]
	}
	if refreshToken == "" {
		return auth, nil
	}

	svc := trae.NewTraeAuth(e.cfg)
	td, err := svc.RefreshTokens(ctx, refreshToken)
	if err != nil {
		return nil, err
	}

	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = td.AccessToken
	if td.RefreshToken != "" {
		auth.Metadata["refresh_token"] = td.RefreshToken
	}
	auth.Metadata["email"] = td.Email
	auth.Metadata["expired"] = td.Expire
	auth.Metadata["type"] = "trae"

	return auth, nil
}

func (e *TraeExecutor) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("trae executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}

	httpReq := req.WithContext(ctx)

	accessToken := ""
	if auth != nil && auth.Metadata != nil {
		if v, ok := auth.Metadata["access_token"].(string); ok && v != "" {
			accessToken = v
		}
	}

	if accessToken == "" && auth != nil && auth.Attributes != nil {
		if v, ok := auth.Attributes["access_token"]; ok && v != "" {
			accessToken = v
		}
	}

	if accessToken == "" {
		return nil, fmt.Errorf("trae executor: missing access token in auth metadata or attributes")
	}

	httpReq.Header.Set("Authorization", "Bearer "+accessToken)

	if auth != nil && auth.Attributes != nil {
		util.ApplyCustomHeadersFromAttrs(httpReq, auth.Attributes)
	}

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}
