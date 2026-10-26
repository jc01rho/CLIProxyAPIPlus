package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/teamshare"
)

// HTTPProxy drives a CLIProxyAPIPlus instance through its management API.
type HTTPProxy struct {
	BaseURL       string
	ManagementKey string
	Client        *http.Client
}

func (p *HTTPProxy) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (p *HTTPProxy) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(p.BaseURL, "/")+"/v0/management"+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("X-Management-Key", p.ManagementKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, truncate(string(raw), 300))
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// AddAPIKey appends a client API key to the proxy configuration.
func (p *HTTPProxy) AddAPIKey(ctx context.Context, key string) error {
	return p.do(ctx, http.MethodPatch, "/api-keys", map[string]string{"old": "", "new": key}, nil)
}

// RemoveAPIKeyByKeyID revokes the configured API key whose team-share key-id matches.
func (p *HTTPProxy) RemoveAPIKeyByKeyID(ctx context.Context, keyID string) error {
	var listing struct {
		APIKeys []string `json:"api-keys"`
	}
	if err := p.do(ctx, http.MethodGet, "/api-keys", nil, &listing); err != nil {
		return err
	}
	for _, key := range listing.APIKeys {
		if teamshare.KeyID(key) == keyID {
			return p.do(ctx, http.MethodDelete, "/api-keys?value="+url.QueryEscape(key), nil, nil)
		}
	}
	return nil
}

// PutPoolAccount adds a credential to a pool.
func (p *HTTPProxy) PutPoolAccount(ctx context.Context, pool, auth, tier string) error {
	return p.do(ctx, http.MethodPut, "/team-share/pools/"+url.PathEscape(pool)+"/accounts", map[string]string{"auth": auth, "tier": tier}, nil)
}

// RemovePoolAccount removes a credential from a pool.
func (p *HTTPProxy) RemovePoolAccount(ctx context.Context, pool, auth string) error {
	return p.do(ctx, http.MethodDelete, "/team-share/pools/"+url.PathEscape(pool)+"/accounts?auth="+url.QueryEscape(auth), nil, nil)
}

// PutMember assigns a percentage of the pool to an API key.
func (p *HTTPProxy) PutMember(ctx context.Context, pool, name, apiKey string, percent float64) error {
	return p.do(ctx, http.MethodPut, "/team-share/pools/"+url.PathEscape(pool)+"/members/"+url.PathEscape(name), map[string]any{"api_key": apiKey, "percent": percent}, nil)
}

// RemoveMember drops a member from a pool.
func (p *HTTPProxy) RemoveMember(ctx context.Context, pool, name string) error {
	return p.do(ctx, http.MethodDelete, "/team-share/pools/"+url.PathEscape(pool)+"/members/"+url.PathEscape(name), nil, nil)
}

// FindAuthFile returns the stored credential file registered for an email, or "".
func (p *HTTPProxy) FindAuthFile(ctx context.Context, email string) (string, error) {
	var listing struct {
		Files []struct {
			Name  string `json:"name"`
			Email string `json:"email"`
		} `json:"files"`
	}
	if err := p.do(ctx, http.MethodGet, "/auth-files", nil, &listing); err != nil {
		return "", err
	}
	for _, f := range listing.Files {
		if strings.EqualFold(f.Email, email) {
			return f.Name, nil
		}
	}
	return "", nil
}

// DeleteAuthFile deletes a stored credential file.
func (p *HTTPProxy) DeleteAuthFile(ctx context.Context, name string) error {
	return p.do(ctx, http.MethodDelete, "/auth-files?name="+url.QueryEscape(name), nil, nil)
}

// Snapshot reads the team-share state.
func (p *HTTPProxy) Snapshot(ctx context.Context) (teamshare.Snapshot, error) {
	var snap teamshare.Snapshot
	err := p.do(ctx, http.MethodGet, "/team-share", nil, &snap)
	return snap, err
}

// AnthropicAuthURL starts a Claude OAuth session and returns the login URL and state.
func (p *HTTPProxy) AnthropicAuthURL(ctx context.Context) (authURL, state string, err error) {
	var out struct {
		URL   string `json:"url"`
		State string `json:"state"`
	}
	if err = p.do(ctx, http.MethodGet, "/anthropic-auth-url?is_webui=true", nil, &out); err != nil {
		return "", "", err
	}
	return out.URL, out.State, nil
}

// SubmitOAuthCallback hands the final redirect URL of a browser login to the proxy.
func (p *HTTPProxy) SubmitOAuthCallback(ctx context.Context, redirectURL string) error {
	return p.do(ctx, http.MethodPost, "/oauth-callback", map[string]string{"provider": "anthropic", "redirect_url": redirectURL}, nil)
}

// OAuthStatus reports whether the OAuth session identified by state has completed.
func (p *HTTPProxy) OAuthStatus(ctx context.Context, state string) (string, error) {
	var out struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := p.do(ctx, http.MethodGet, "/get-auth-status?state="+url.QueryEscape(state), nil, &out); err != nil {
		return "", err
	}
	if out.Status == "error" {
		return "error", fmt.Errorf("oauth: %s", out.Error)
	}
	return out.Status, nil
}
