package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/zcode"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// ZcodeAuthenticator implements the GLM ZCode OAuth flow (UNOFFICIAL, opt-in).
// It reuses ZCode's authorize page, broker, and a custom-protocol redirect; the
// CLI cannot catch the zcode:// redirect, so the user pastes the code/redirect
// URL. The flow provisions a real Z.AI API key used against api.z.ai.
type ZcodeAuthenticator struct{}

// NewZcodeAuthenticator constructs a zcode authenticator.
func NewZcodeAuthenticator() *ZcodeAuthenticator {
	return &ZcodeAuthenticator{}
}

// Provider returns the provider key for the authenticator.
func (a *ZcodeAuthenticator) Provider() string {
	return "zcode"
}

// RefreshLead indicates how soon before expiry a refresh should be attempted.
// The provisioned API key is long-lived (~10y), so no proactive refresh.
func (a *ZcodeAuthenticator) RefreshLead() *time.Duration {
	return nil
}

// Login runs the zcode OAuth flow and creates an auth record.
func (a *ZcodeAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	oauth := zcode.NewOAuth()

	// Generate the authorize URL and prompt the user to paste the code/redirect.
	state := fmt.Sprintf("zcode-%d", time.Now().UnixNano())
	authURL := oauth.GenerateAuthURL(state, "")
	instructions := "Complete Z.AI login in your browser. This is an UNOFFICIAL ZCode-based login — use at your own risk. Because this CLI cannot receive the zcode:// redirect, paste the final redirect URL or authorization code when prompted."
	fmt.Println(instructions)
	fmt.Printf("Open this URL to authorize:\n%s\n", authURL)

	var code string
	if opts != nil && opts.Prompt != nil {
		userInput, err := opts.Prompt("Paste the redirect URL or authorization code: ")
		if err != nil {
			return nil, fmt.Errorf("zcode auth: prompt failed: %w", err)
		}
		code = strings.TrimSpace(userInput)
	} else {
		fmt.Print("Paste the redirect URL or authorization code: ")
		if _, err := fmt.Scanln(&code); err != nil {
			return nil, fmt.Errorf("zcode auth: read code: %w", err)
		}
		code = strings.TrimSpace(code)
	}
	if code == "" {
		return nil, fmt.Errorf("zcode auth: no authorization code provided")
	}

	creds, err := oauth.ExchangeCode(ctx, code, state, "")
	if err != nil {
		return nil, fmt.Errorf("zcode auth: exchange failed: %w", err)
	}

	return a.createAuthRecord(creds)
}

// createAuthRecord builds the auth record from zcode credentials.
func (a *ZcodeAuthenticator) createAuthRecord(creds *zcode.Credentials) (*coreauth.Auth, error) {
	now := time.Now()
	seq := now.UnixNano() % 100000
	idPart := "zcode"
	if creds.Email != "" {
		idPart = sanitizeZcodeIdentifier(creds.Email)
	}
	fileName := fmt.Sprintf("zcode-%s-%05d.json", idPart, seq)

	record := &coreauth.Auth{
		ID:        fileName,
		Provider:  "zcode",
		FileName:  fileName,
		Label:     "zcode",
		Status:    coreauth.StatusActive,
		CreatedAt: now,
		UpdatedAt: now,
		Metadata: map[string]any{
			"type":          "zcode",
			"access_token":  creds.AccessToken,
			"refresh_token": creds.RefreshToken,
			"expires_at":    creds.ExpiresAt.Format(time.RFC3339),
			"email":         creds.Email,
			"account_id":    creds.AccountID,
			"zcode_token":   creds.ZcodeToken,
			"start_plan":    creds.StartPlanActive,
			"start_plan_limit": creds.StartPlanLimit,
			"start_plan_used":  creds.StartPlanUsed,
		},
		Attributes: map[string]string{
			"api_key":          creds.AccessToken,
			"base_url":         zcode.DefaultAnthropicBase,
			"email":            creds.Email,
			"source":           "zcode-oauth",
			"zcode_token":      creds.ZcodeToken,
			"start_plan_active": boolStr(creds.StartPlanActive),
		},
		NextRefreshAfter: creds.ExpiresAt.Add(-24 * time.Hour),
	}

	if creds.Email != "" {
		fmt.Printf("\n✓ ZCode authentication completed successfully! (Account: %s)\n", creds.Email)
	} else {
		fmt.Println("\n✓ ZCode authentication completed successfully!")
	}
	return record, nil
}

// sanitizeZcodeIdentifier sanitizes an email for use in a filename.
func sanitizeZcodeIdentifier(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

// boolStr formats a bool as "true"/"false" for storage in Attributes.
func boolStr(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
