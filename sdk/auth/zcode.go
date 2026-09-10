package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/zcode"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// zcodeOAuthFlow is the subset of the zcode OAuth client the login flow uses.
// It is an interface so tests can drive the broker device flow without network.
type zcodeOAuthFlow interface {
	StartCLIFlow(ctx context.Context) (*zcode.CLIFlow, error)
	PollCLIFlow(ctx context.Context, flowID, pollToken string) (*zcode.CLIPollResult, error)
	ProvisionFromUpstream(ctx context.Context, upstreamZaiAccess, zcodeTokenForIdentity string) (*zcode.Credentials, error)
	GenerateAuthURL(state, redirectURI string) string
	ExchangeCode(ctx context.Context, code, state, redirectURI string) (*zcode.Credentials, error)
}

// zcodePollFloor is the smallest poll interval accepted from the broker.
const zcodePollFloor = time.Second

// ZcodeAuthenticator implements the GLM ZCode OAuth flow (UNOFFICIAL, opt-in).
// It drives the broker CLI device flow first (the browser completes the
// authorization, so no redirect handling is needed) and falls back to pasting
// the zcode:// redirect URL or authorization code when the broker is
// unavailable. The flow provisions a real Z.AI API key used against api.z.ai.
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

// Login runs the zcode OAuth flow and creates an auth record. The broker CLI
// device flow is the default; when the broker cannot be reached (or its
// contract changed), login degrades to the manual paste flow so Z.AI login
// still works without the unofficial broker.
func (a *ZcodeAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	creds, err := runZcodeLogin(ctx, zcode.NewOAuth(), opts)
	if err != nil {
		return nil, err
	}
	return a.createAuthRecord(creds)
}

// runZcodeLogin performs the credentialed part of login so tests can inject a
// fake broker client.
func runZcodeLogin(ctx context.Context, oauth zcodeOAuthFlow, opts *LoginOptions) (*zcode.Credentials, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	flow, errInit := oauth.StartCLIFlow(ctx)
	if errInit != nil {
		if errCtx := ctx.Err(); errCtx != nil {
			return nil, errCtx
		}
		log.Warnf("zcode auth: broker device flow unavailable (%v); falling back to manual paste", errInit)
		return zcodePasteLogin(ctx, oauth, opts)
	}

	fmt.Println("Complete Z.AI login in your browser. This is an UNOFFICIAL ZCode-based login — use at your own risk.")
	fmt.Printf("Open this URL to authorize:\n%s\n", flow.AuthorizeURL)
	if opts == nil || !opts.NoBrowser {
		if !browser.IsAvailable() {
			log.Warn("No browser available; please open the URL manually")
		} else if errOpen := browser.OpenURL(flow.AuthorizeURL); errOpen != nil {
			log.Warnf("Failed to open browser automatically: %v", errOpen)
		}
	}
	fmt.Println("Waiting for Z.AI authorization in the browser...")
	return zcodePollCLIFlow(ctx, oauth, flow)
}

// zcodePollCLIFlow waits for the browser authorization to complete. Transient
// broker errors keep the loop alive (the broker polls report 408/429/5xx while
// the user is still authorizing); a failed status, the broker deadline, or
// context cancellation aborts the login.
func zcodePollCLIFlow(ctx context.Context, oauth zcodeOAuthFlow, flow *zcode.CLIFlow) (*zcode.Credentials, error) {
	interval := time.Duration(flow.PollIntervalSec) * time.Second
	if interval < zcodePollFloor {
		interval = 2 * time.Second
	}
	for {
		if errCtx := ctx.Err(); errCtx != nil {
			return nil, errCtx
		}
		if !flow.ExpiresAt.IsZero() && !time.Now().Before(flow.ExpiresAt) {
			return nil, fmt.Errorf("zcode auth: authorization flow expired before completion")
		}
		result, errPoll := oauth.PollCLIFlow(ctx, flow.FlowID, flow.PollToken)
		switch {
		case errPoll != nil:
			if !zcode.TransientPollError(errPoll) {
				return nil, fmt.Errorf("zcode auth: broker poll failed: %w", errPoll)
			}
			log.Debugf("zcode auth: transient poll error, retrying: %v", errPoll)
		case result != nil && result.Status == "failed":
			return nil, fmt.Errorf("zcode auth: Z.AI rejected or cancelled the authorization")
		case result.Ready():
			creds, errProvision := oauth.ProvisionFromUpstream(ctx, result.ZaiAccessToken, result.ZcodeToken)
			if errProvision != nil {
				return nil, fmt.Errorf("zcode auth: provision failed: %w", errProvision)
			}
			return creds, nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// zcodePasteLogin is the fallback flow: the CLI cannot catch the zcode://
// redirect, so the user pastes the final redirect URL or authorization code.
func zcodePasteLogin(ctx context.Context, oauth zcodeOAuthFlow, opts *LoginOptions) (*zcode.Credentials, error) {
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
	return creds, nil
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
		},
		Attributes: map[string]string{
			"api_key":  creds.AccessToken,
			"base_url": zcode.DefaultAnthropicBase,
			"email":    creds.Email,
			"source":   "zcode-oauth",
		},
		NextRefreshAfter: creds.ExpiresAt.Add(-24 * time.Hour),
	}
	// Persist the broker JWT and start plan state: the executor's start plan
	// routing reads Metadata["zcode_token"]/["start_plan_active"] after a
	// reload from disk.
	if creds.ZcodeToken != "" {
		record.Metadata["zcode_token"] = creds.ZcodeToken
		record.Attributes["zcode_token"] = creds.ZcodeToken
		record.Metadata["start_plan_active"] = creds.StartPlanActive
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
