// Package trae provides OAuth2 authentication functionality for Trae API.
// This file implements GitHub OAuth flow for Trae authentication.
package trae

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	log "github.com/sirupsen/logrus"
)

// GitHubTokenData holds the token information from GitHub OAuth exchange with Trae backend.
type GitHubTokenData struct {
	Token     string `json:"token"`
	Email     string `json:"email"`
	ExpiresAt string `json:"expires_at"`
}

// GenerateGitHubAuthURL creates the GitHub OAuth authorization URL.
// It constructs the URL with the necessary parameters for GitHub OAuth flow.
// The state parameter should be a cryptographically secure random string for CSRF protection.
func (o *TraeAuth) GenerateGitHubAuthURL(redirectURI, state string) (string, error) {
	if redirectURI == "" {
		return "", fmt.Errorf("redirect URI is required")
	}
	if state == "" {
		return "", fmt.Errorf("state parameter is required for CSRF protection")
	}

	params := url.Values{
		"client_id":    {githubClientID},
		"redirect_uri": {redirectURI},
		"state":        {state},
		"scope":        {"user:email"}, // Request email scope
	}

	authURL := fmt.Sprintf("https://github.com/login/oauth/authorize?%s", params.Encode())
	return authURL, nil
}

// ExchangeGitHubCode exchanges the GitHub authorization code for a Trae JWT token.
// This method performs a two-step process:
// 1. Sends the GitHub code to Trae backend
// 2. Receives a Trae JWT token in return
//
// The Trae backend endpoint: POST /cloudide/api/v3/trae/GetUserGitHubToken
// Required headers: x-cthulhu-csrf: 1
func (o *TraeAuth) ExchangeGitHubCode(ctx context.Context, code string) (*GitHubTokenData, error) {
	if code == "" {
		return nil, fmt.Errorf("authorization code is required")
	}

	// Prepare request body with GitHub code and platform ID
	reqBody := map[string]interface{}{
		"code":        code,
		"platform_id": githubPlatformID,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	// Construct Trae backend URL
	tokenURL := fmt.Sprintf("%s/cloudide/api/v3/trae/GetUserGitHubToken", traeBackendURL)

	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(string(jsonBody)))
	if err != nil {
		return nil, fmt.Errorf("failed to create token request: %w", err)
	}

	// Set required headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-cthulhu-csrf", "1") // Required by Trae backend

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("failed to close response body: %v", errClose)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("GitHub token exchange failed (status %d): %s", resp.StatusCode, string(body))
		return nil, fmt.Errorf("token exchange failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Parse response from Trae backend
	var tokenResp struct {
		Token     string `json:"token"`
		Email     string `json:"email"`
		ExpiresAt string `json:"expires_at"`
	}

	if err = json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	// Validate response
	if tokenResp.Token == "" {
		return nil, fmt.Errorf("received empty token from Trae backend")
	}

	return &GitHubTokenData{
		Token:     tokenResp.Token,
		Email:     tokenResp.Email,
		ExpiresAt: tokenResp.ExpiresAt,
	}, nil
}

// ExchangeTraeToken exchanges a GitHub token for a complete Trae authentication bundle.
// This method takes the token received from ExchangeGitHubCode and creates a TraeAuthBundle
// with the properly formatted JWT token.
//
// The JWT token format used by Trae: "Cloud-IDE-JWT {token}"
func (o *TraeAuth) ExchangeTraeToken(githubToken string) (*TraeAuthBundle, error) {
	if githubToken == "" {
		return nil, fmt.Errorf("GitHub token is required")
	}

	// Format the token with Trae's JWT format
	formattedToken := fmt.Sprintf("%s %s", traeJWTFormat, githubToken)

	// Create token data
	tokenData := TraeTokenData{
		AccessToken:  formattedToken,
		RefreshToken: "", // GitHub OAuth flow doesn't provide refresh token
		Email:        "", // Email should be extracted from the token or provided separately
		Expire:       "", // Expiration should be set based on token response
	}

	// Create auth bundle
	bundle := &TraeAuthBundle{
		TokenData:   tokenData,
		LastRefresh: "", // Set by caller if needed
	}

	return bundle, nil
}
