// Package trae provides OAuth2 authentication functionality for Trae API.
// This file implements Google OAuth flow with PKCE for Trae authentication.
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

// GoogleTokenData holds the token information from Google OAuth exchange with Trae backend.
type GoogleTokenData struct {
	Token     string `json:"token"`
	Email     string `json:"email"`
	ExpiresAt string `json:"expires_at"`
}

// GenerateGoogleAuthURL creates the Google OAuth authorization URL with PKCE.
// It constructs the URL with the necessary parameters for Google OAuth flow.
// The state parameter should be a cryptographically secure random string for CSRF protection.
// PKCE (Proof Key for Code Exchange) is required for Google OAuth.
func (o *TraeAuth) GenerateGoogleAuthURL(redirectURI, state string, pkceCodes *PKCECodes) (string, error) {
	if redirectURI == "" {
		return "", fmt.Errorf("redirect URI is required")
	}
	if state == "" {
		return "", fmt.Errorf("state parameter is required for CSRF protection")
	}
	if pkceCodes == nil {
		return "", fmt.Errorf("PKCE codes are required for Google OAuth")
	}

	params := url.Values{
		"client_id":             {googleClientID},
		"redirect_uri":          {redirectURI},
		"response_type":         {"code"},
		"scope":                 {"openid email profile"},
		"state":                 {state},
		"code_challenge":        {pkceCodes.CodeChallenge},
		"code_challenge_method": {"S256"},
		"access_type":           {"offline"}, // Request refresh token
		"prompt":                {"consent"}, // Force consent screen to get refresh token
	}

	authURL := fmt.Sprintf("https://accounts.google.com/o/oauth2/v2/auth?%s", params.Encode())
	return authURL, nil
}

// ExchangeGoogleCode exchanges the Google authorization code for a Trae JWT token.
// This method performs a multi-step process:
// 1. Exchanges the Google authorization code for a Google access token (with PKCE)
// 2. Sends the Google access token to Trae backend
// 3. Receives a Trae JWT token in return
//
// The Trae backend endpoint: POST /cloudide/api/v3/trae/GetUserGoogleToken
// Required headers: x-cthulhu-csrf: 1
func (o *TraeAuth) ExchangeGoogleCode(ctx context.Context, code string, pkceCodes *PKCECodes) (*TraeAuthBundle, error) {
	if code == "" {
		return nil, fmt.Errorf("authorization code is required")
	}
	if pkceCodes == nil {
		return nil, fmt.Errorf("PKCE codes are required for token exchange")
	}

	// Step 1: Exchange Google authorization code for Google access token
	googleToken, err := o.exchangeGoogleCodeForToken(ctx, code, pkceCodes)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange Google code: %w", err)
	}

	// Step 2: Exchange Google access token for Trae JWT token
	traeToken, err := o.exchangeGoogleTokenForTrae(ctx, googleToken)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange Google token for Trae token: %w", err)
	}

	// Step 3: Create TraeAuthBundle with formatted JWT token
	formattedToken := fmt.Sprintf("%s %s", traeJWTFormat, traeToken.Token)

	tokenData := TraeTokenData{
		AccessToken:  formattedToken,
		RefreshToken: "", // Google OAuth flow through Trae doesn't provide refresh token
		Email:        traeToken.Email,
		Expire:       traeToken.ExpiresAt,
	}

	bundle := &TraeAuthBundle{
		TokenData:   tokenData,
		LastRefresh: "", // Set by caller if needed
	}

	return bundle, nil
}

// exchangeGoogleCodeForToken exchanges the authorization code for a Google access token.
// This is the first step in the Google OAuth flow, using PKCE for security.
func (o *TraeAuth) exchangeGoogleCodeForToken(ctx context.Context, code string, pkceCodes *PKCECodes) (string, error) {
	tokenURL := "https://oauth2.googleapis.com/token"

	// Prepare request body with PKCE code verifier
	data := url.Values{
		"code":          {code},
		"client_id":     {googleClientID},
		"code_verifier": {pkceCodes.CodeVerifier},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {"http://localhost:8080/oauth2callback"}, // Must match the redirect_uri used in auth URL
	}

	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed to create token request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("token exchange request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("failed to close response body: %v", errClose)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("Google token exchange failed (status %d): %s", resp.StatusCode, string(body))
		return "", fmt.Errorf("token exchange failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Parse Google token response
	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		TokenType    string `json:"token_type"`
		Scope        string `json:"scope"`
		IDToken      string `json:"id_token"`
	}

	if err = json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("failed to parse token response: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return "", fmt.Errorf("received empty access token from Google")
	}

	return tokenResp.AccessToken, nil
}

// exchangeGoogleTokenForTrae exchanges a Google access token for a Trae JWT token.
// This is the second step, where we send the Google token to Trae backend.
func (o *TraeAuth) exchangeGoogleTokenForTrae(ctx context.Context, googleAccessToken string) (*GoogleTokenData, error) {
	// Prepare request body with Google access token and platform ID
	reqBody := map[string]interface{}{
		"code":        googleAccessToken, // Trae backend expects the token in "code" field
		"platform_id": googlePlatformID,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	// Construct Trae backend URL
	tokenURL := fmt.Sprintf("%s/cloudide/api/v3/trae/GetUserGoogleToken", traeBackendURL)

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
		log.Debugf("Trae token exchange failed (status %d): %s", resp.StatusCode, string(body))
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

	return &GoogleTokenData{
		Token:     tokenResp.Token,
		Email:     tokenResp.Email,
		ExpiresAt: tokenResp.ExpiresAt,
	}, nil
}
