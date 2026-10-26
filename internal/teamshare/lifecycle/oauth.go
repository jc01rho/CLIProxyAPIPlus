package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/teamshare/oauthlogin"
)

const defaultCallbackPrefix = "http://localhost:54545/callback"

// OAuthStep is a browser step of the login script. {email} in Value is replaced.
type OAuthStep struct {
	Action      string `json:"action"`
	Selector    string `json:"selector,omitempty"`
	Value       string `json:"value,omitempty"`
	URLContains string `json:"url_contains,omitempty"`
	TimeoutSecs int    `json:"timeout_seconds,omitempty"`
}

// OAuthConfig describes the unattended login: the service passkey held in a server-side
// credential file and the page steps, which depend on the login pages and are captured
// from one real login.
type OAuthConfig struct {
	ChromiumPath      string      `json:"chromium_path,omitempty"`
	Headless          *bool       `json:"headless,omitempty"`
	CredentialFile    string      `json:"credential_file"`
	CallbackURLPrefix string      `json:"callback_url_prefix,omitempty"`
	Steps             []OAuthStep `json:"steps"`
	TimeoutSecs       int         `json:"timeout_seconds,omitempty"`
}

// LoadOAuthConfig reads the login description.
func LoadOAuthConfig(path string) (OAuthConfig, error) {
	var cfg OAuthConfig
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, err
	}
	if cfg.CredentialFile == "" || len(cfg.Steps) == 0 {
		return cfg, errors.New("oauth config must define credential_file and steps")
	}
	return cfg, nil
}

type storedCredential struct {
	CredentialID    string `json:"credential_id"`
	PrivateKeyPKCS8 string `json:"private_key_pkcs8"`
	RPID            string `json:"rp_id"`
	UserHandle      string `json:"user_handle,omitempty"`
	SignCount       uint32 `json:"sign_count"`
}

func loadCredential(path string) (storedCredential, error) {
	var cred storedCredential
	info, err := os.Stat(path)
	if err != nil {
		return cred, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return cred, fmt.Errorf("credential file %s must not be readable by group or others (chmod 600)", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return cred, err
	}
	if err := json.Unmarshal(raw, &cred); err != nil {
		return cred, err
	}
	if cred.CredentialID == "" || cred.PrivateKeyPKCS8 == "" || cred.RPID == "" {
		return cred, errors.New("credential file must define credential_id, private_key_pkcs8 and rp_id")
	}
	return cred, nil
}

func saveCredential(path string, cred storedCredential) error {
	raw, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// BrowserOAuth registers a Claude credential by letting a headless Chromium complete the
// authorization URL with the service passkey and handing the callback to the proxy.
type BrowserOAuth struct {
	Proxy        *HTTPProxy
	Config       OAuthConfig
	Run          func(ctx context.Context, authURL string, opts oauthlogin.Options) (oauthlogin.Result, error)
	PollInterval time.Duration
	PollTimeout  time.Duration
	Logf         func(string, ...any)
}

// Register performs the login for email and returns the stored credential file name.
func (b *BrowserOAuth) Register(ctx context.Context, email string) (string, error) {
	cred, err := loadCredential(b.Config.CredentialFile)
	if err != nil {
		return "", err
	}
	authURL, state, err := b.Proxy.AnthropicAuthURL(ctx)
	if err != nil {
		return "", fmt.Errorf("start oauth session: %w", err)
	}
	steps := make([]oauthlogin.Step, 0, len(b.Config.Steps))
	for _, s := range b.Config.Steps {
		steps = append(steps, oauthlogin.Step{
			Action:      s.Action,
			Selector:    s.Selector,
			Value:       strings.ReplaceAll(s.Value, "{email}", email),
			URLContains: s.URLContains,
			Timeout:     time.Duration(s.TimeoutSecs) * time.Second,
		})
	}
	opts := oauthlogin.Options{
		ChromiumPath: b.Config.ChromiumPath,
		Headless:     b.Config.Headless == nil || *b.Config.Headless,
		Credential: &oauthlogin.VirtualCredential{
			CredentialID: cred.CredentialID, PrivateKeyPKCS8: cred.PrivateKeyPKCS8,
			RPID: cred.RPID, UserHandle: cred.UserHandle, SignCount: cred.SignCount,
		},
		Steps:             steps,
		CallbackURLPrefix: firstNonEmpty(b.Config.CallbackURLPrefix, defaultCallbackPrefix),
		Timeout:           time.Duration(b.Config.TimeoutSecs) * time.Second,
		Logf:              b.Logf,
	}
	run := b.Run
	if run == nil {
		run = oauthlogin.Run
	}
	result, err := run(ctx, authURL, opts)
	if result.SignCount > cred.SignCount {
		cred.SignCount = result.SignCount
		if saveErr := saveCredential(b.Config.CredentialFile, cred); saveErr != nil {
			return "", fmt.Errorf("persist passkey counter: %w", saveErr)
		}
	}
	if err != nil {
		return "", fmt.Errorf("browser login: %w", err)
	}
	if err := b.Proxy.SubmitOAuthCallback(ctx, result.FinalURL); err != nil {
		return "", fmt.Errorf("submit oauth callback: %w", err)
	}
	if err := b.waitCompleted(ctx, state); err != nil {
		return "", err
	}
	file, err := b.Proxy.FindAuthFile(ctx, email)
	if err != nil {
		return "", err
	}
	if file == "" {
		return "", fmt.Errorf("oauth completed but no credential file for %s was stored (the signed-in account may differ from the seat email)", email)
	}
	return file, nil
}

func (b *BrowserOAuth) waitCompleted(ctx context.Context, state string) error {
	interval, timeout := b.PollInterval, b.PollTimeout
	if interval <= 0 {
		interval = time.Second
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		status, err := b.Proxy.OAuthStatus(ctx, state)
		if err != nil {
			return err
		}
		if status == "ok" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("oauth session did not complete within %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
