package auth

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/cline"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const defaultClineCallbackPort = 1455

type ClineAuthenticator struct {
	CallbackPort int
}

func NewClineAuthenticator() *ClineAuthenticator {
	return &ClineAuthenticator{CallbackPort: defaultClineCallbackPort}
}

func (a *ClineAuthenticator) Provider() string {
	return "cline"
}

func (a *ClineAuthenticator) RefreshLead() *time.Duration {
	d := 5 * time.Minute
	return &d
}

func (a *ClineAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	callbackPort := a.CallbackPort
	if opts.CallbackPort > 0 {
		callbackPort = opts.CallbackPort
	}

	state, err := misc.GenerateRandomState()
	if err != nil {
		return nil, fmt.Errorf("cline state generation failed: %w", err)
	}

	callbackURL := fmt.Sprintf("http://localhost:%d/callback", callbackPort)
	authSvc := cline.NewClineAuth(cfg)
	authURL := authSvc.GenerateAuthURL(state, callbackURL)

	if !opts.NoBrowser {
		fmt.Println("Opening browser for Cline authentication")
		if !browser.IsAvailable() {
			log.Warn("No browser available; please open the URL manually")
			util.PrintSSHTunnelInstructions(callbackPort)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		} else if err = browser.OpenURL(authURL); err != nil {
			log.Warnf("Failed to open browser automatically: %v", err)
			util.PrintSSHTunnelInstructions(callbackPort)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		}
	} else {
		util.PrintSSHTunnelInstructions(callbackPort)
		fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
	}

	fmt.Println("Waiting for Cline authentication callback...")
	result, err := waitForClineCallback(ctx, callbackPort, opts.Prompt)
	if err != nil {
		return nil, err
	}

	if result.Error != "" {
		if result.ErrorDescription != "" {
			return nil, fmt.Errorf("cline oauth error: %s (%s)", result.Error, result.ErrorDescription)
		}
		return nil, fmt.Errorf("cline oauth error: %s", result.Error)
	}
	if result.State != state {
		return nil, fmt.Errorf("cline authentication failed: state mismatch")
	}

	tokenResp, err := authSvc.ExchangeCode(ctx, result.Code, callbackURL)
	if err != nil {
		return nil, fmt.Errorf("cline token exchange failed: %w", err)
	}

	email := strings.TrimSpace(tokenResp.Email)
	if email == "" {
		return nil, fmt.Errorf("cline authentication failed: missing account email")
	}

	ts := &cline.ClineTokenStorage{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresAt:    tokenResp.ExpiresAt,
		Email:        email,
		Type:         "cline",
	}

	fileName := cline.CredentialFileName(email)
	metadata := map[string]any{
		"email":      email,
		"fileName":   fileName,
		"expires_at": tokenResp.ExpiresAt,
	}

	fmt.Printf("Cline authentication successful for %s\n", email)

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Storage:  ts,
		Metadata: metadata,
	}, nil
}

type clineOAuthResult struct {
	Code             string
	State            string
	Error            string
	ErrorDescription string
}

func waitForClineCallback(ctx context.Context, callbackPort int, prompt func(prompt string) (string, error)) (*clineOAuthResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	resultCh := make(chan *clineOAuthResult, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	server := &http.Server{
		Addr:              ":" + strconv.Itoa(callbackPort),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		res := &clineOAuthResult{
			Code:             strings.TrimSpace(q.Get("code")),
			State:            strings.TrimSpace(q.Get("state")),
			Error:            strings.TrimSpace(q.Get("error")),
			ErrorDescription: strings.TrimSpace(q.Get("error_description")),
		}

		select {
		case resultCh <- res:
		default:
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body><h2>Cline login complete</h2><p>You can close this window and return to CLI.</p></body></html>"))
	})

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("cline callback server failed: %w", err)
		}
	}()

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Warnf("cline callback server shutdown error: %v", err)
		}
	}()

	var manualTimer *time.Timer
	var manualTimerC <-chan time.Time
	if prompt != nil {
		manualTimer = time.NewTimer(15 * time.Second)
		manualTimerC = manualTimer.C
		defer manualTimer.Stop()
	}

	timeout := cline.AuthTimeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}
	timeoutTimer := time.NewTimer(timeout)
	defer timeoutTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timeoutTimer.C:
			return nil, fmt.Errorf("cline callback wait timeout after %s", timeout.String())
		case err := <-errCh:
			return nil, err
		case res := <-resultCh:
			return res, nil
		case <-manualTimerC:
			manualTimerC = nil
			input, err := prompt("Paste the Cline callback URL (or press Enter to keep waiting): ")
			if err != nil {
				return nil, err
			}
			parsed, err := misc.ParseOAuthCallback(input)
			if err != nil {
				return nil, err
			}
			if parsed == nil {
				continue
			}
			return &clineOAuthResult{
				Code:             parsed.Code,
				State:            parsed.State,
				Error:            parsed.Error,
				ErrorDescription: parsed.ErrorDescription,
			}, nil
		}
	}
}
