package executor

import (
	"context"
	"fmt"
	"strings"
	"time"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	claudeAccountProfileCheckedAtKey = "claude_account_profile_checked_at"
	claudeAccountProfileRefreshAge   = 24 * time.Hour
	claudeAccountProfileTimeout      = 10 * time.Second
)

type claudeOAuthProfileFetcher func(context.Context, *cliproxyauth.Auth, string) (*claudeauth.OAuthProfile, error)

func (e *ClaudeExecutor) ShouldPrepareRequestAuth(auth *cliproxyauth.Auth) bool {
	apiKey, _ := claudeCreds(auth)
	if !isClaudeOAuthToken(apiKey) || auth == nil {
		return false
	}
	if !claudeauth.HasCanonicalDeviceIDPool(auth.Metadata[claudeauth.ClaudeDeviceIDsMetadataKey]) {
		return true
	}
	if helps.ClaudeCredentialAccountUUID(auth) != "" {
		return false
	}
	return claudeAccountProfileLookupDue(auth.Metadata, time.Now())
}

func claudeAccountProfileLookupDue(metadata map[string]any, now time.Time) bool {
	if metadata == nil {
		return true
	}
	checkedAt, _ := metadata[claudeAccountProfileCheckedAtKey].(string)
	checkedAt = strings.TrimSpace(checkedAt)
	if checkedAt == "" {
		return true
	}
	parsed, errParse := time.Parse(time.RFC3339, checkedAt)
	return errParse != nil || !parsed.Add(claudeAccountProfileRefreshAge).After(now)
}

func (e *ClaudeExecutor) PrepareRequestAuth(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil || !e.ShouldPrepareRequestAuth(auth) {
		return auth, nil
	}
	apiKey, _ := claudeCreds(auth)
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	if _, errDeviceIDs := helps.EnsureClaudeCredentialDevicePoolRequired(ctx, auth); errDeviceIDs != nil {
		return nil, errDeviceIDs
	}
	if helps.ClaudeCredentialAccountUUID(auth) != "" || !claudeAccountProfileLookupDue(auth.Metadata, time.Now()) {
		return auth, nil
	}

	auth.Metadata[claudeAccountProfileCheckedAtKey] = time.Now().UTC().Format(time.RFC3339)
	profile, errProfile := e.fetchClaudeOAuthProfile(ctx, auth, apiKey)
	if errProfile != nil {
		if errContext := ctx.Err(); errContext != nil {
			return nil, errContext
		}
		log.WithError(errProfile).Warn("claude executor: unable to populate OAuth account profile")
		return auth, nil
	}
	if profile == nil {
		return auth, nil
	}
	if accountUUID := strings.TrimSpace(profile.Account.UUID); accountUUID != "" {
		auth.Metadata["account_uuid"] = accountUUID
	}
	if email := strings.TrimSpace(profile.Account.Email); email != "" {
		auth.Metadata["email"] = email
	}
	if organizationUUID := strings.TrimSpace(profile.Organization.UUID); organizationUUID != "" {
		auth.Metadata["organization_uuid"] = organizationUUID
	}
	if organizationName := strings.TrimSpace(profile.Organization.Name); organizationName != "" {
		auth.Metadata["organization_name"] = organizationName
	}
	return auth, nil
}

func (e *ClaudeExecutor) fetchClaudeOAuthProfile(ctx context.Context, auth *cliproxyauth.Auth, apiKey string) (*claudeauth.OAuthProfile, error) {
	if e == nil {
		return nil, fmt.Errorf("fetch Claude OAuth profile: executor is nil")
	}
	if e.oauthProfileFetcher != nil {
		return e.oauthProfileFetcher(ctx, auth, apiKey)
	}
	if auth == nil {
		return nil, fmt.Errorf("fetch Claude OAuth profile: auth is nil")
	}
	profileCtx, cancelProfile := context.WithTimeout(ctx, claudeAccountProfileTimeout)
	defer cancelProfile()
	service := claudeauth.NewClaudeAuthWithProxyURL(e.cfg, auth.ProxyURL)
	return service.FetchOAuthProfile(profileCtx, apiKey)
}

func (e *ClaudeExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("claude executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, fmt.Errorf("claude executor: auth is nil")
	}
	var refreshToken string
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["refresh_token"].(string); ok && v != "" {
			refreshToken = v
		}
	}
	if refreshToken == "" {
		return auth, nil
	}
	svc := claudeauth.NewClaudeAuthWithProxyURL(e.cfg, auth.ProxyURL)
	td, err := svc.RefreshTokensWithRetry(ctx, refreshToken, 3)
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
	if td.AccountUUID != "" {
		auth.Metadata["account_uuid"] = td.AccountUUID
	}
	if td.OrganizationUUID != "" {
		auth.Metadata["organization_uuid"] = td.OrganizationUUID
	}
	if td.OrganizationName != "" {
		auth.Metadata["organization_name"] = td.OrganizationName
	}
	auth.Metadata["expired"] = td.Expire
	auth.Metadata["type"] = "claude"
	now := time.Now().Format(time.RFC3339)
	auth.Metadata["last_refresh"] = now
	return auth, nil
}
