package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// SeatCall is one claude.ai web API request template. Placeholders {org}, {email}, {tier}
// are substituted in the path and body; {email} and {tier} are escaped in the path.
type SeatCall struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Body   string `json:"body,omitempty"`
	// OKStatuses lists extra statuses treated as success (for example 409 for "already assigned").
	OKStatuses []int `json:"ok_statuses,omitempty"`
}

// SeatEndpoints describes the claude.ai admin calls. The endpoint paths are data because
// they must be confirmed by capturing one real seat change in the admin console.
type SeatEndpoints struct {
	BaseURL    string            `json:"base_url"`
	OrgID      string            `json:"org_id"`
	CookieFile string            `json:"cookie_file"`
	UserAgent  string            `json:"user_agent,omitempty"`
	TierValues map[string]string `json:"tier_values"`
	Assign     SeatCall          `json:"assign"`
	Remove     SeatCall          `json:"remove"`
}

// LoadSeatEndpoints reads the endpoint description.
func LoadSeatEndpoints(path string) (SeatEndpoints, error) {
	var ep SeatEndpoints
	raw, err := os.ReadFile(path)
	if err != nil {
		return ep, err
	}
	if err := json.Unmarshal(raw, &ep); err != nil {
		return ep, err
	}
	if ep.BaseURL == "" || ep.OrgID == "" || ep.CookieFile == "" || ep.Assign.Path == "" || ep.Remove.Path == "" {
		return ep, errors.New("seat endpoints file must define base_url, org_id, cookie_file, assign.path and remove.path")
	}
	return ep, nil
}

// HTTPSeatManager calls the claude.ai admin web API with the administrator's session cookie.
type HTTPSeatManager struct {
	Endpoints SeatEndpoints
	Client    *http.Client
}

func (m *HTTPSeatManager) call(ctx context.Context, c SeatCall, email, tier string) error {
	cookieRaw, err := os.ReadFile(m.Endpoints.CookieFile)
	if err != nil {
		return fmt.Errorf("read admin cookie: %w", err)
	}
	cookie := strings.TrimSpace(string(cookieRaw))
	if cookie == "" {
		return errors.New("admin cookie file is empty")
	}
	tierValue := tier
	if v, ok := m.Endpoints.TierValues[tier]; ok {
		tierValue = v
	}
	path := strings.NewReplacer("{org}", url.PathEscape(m.Endpoints.OrgID), "{email}", url.PathEscape(email), "{tier}", url.PathEscape(tierValue)).Replace(c.Path)
	body := strings.NewReplacer("{org}", m.Endpoints.OrgID, "{email}", email, "{tier}", tierValue).Replace(c.Body)
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	req, err := http.NewRequestWithContext(ctx, c.Method, strings.TrimRight(m.Endpoints.BaseURL, "/")+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Accept", "application/json")
	if m.Endpoints.UserAgent != "" {
		req.Header.Set("User-Agent", m.Endpoints.UserAgent)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	client := m.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", c.Method, c.Path, redactTransportError(err, cookie))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return nil
	}
	for _, ok := range c.OKStatuses {
		if resp.StatusCode == ok {
			return nil
		}
	}
	return fmt.Errorf("%s %s: HTTP %d: %s", c.Method, c.Path, resp.StatusCode, truncate(string(raw), 200))
}

func redactTransportError(err error, secret string) error {
	if secret == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), secret, "[redacted]"))
}

// Assign gives email a seat of the requested tier.
func (m *HTTPSeatManager) Assign(ctx context.Context, email, tier string) error {
	return m.call(ctx, m.Endpoints.Assign, email, tier)
}

// Remove releases the seat and removes email from the organization.
func (m *HTTPSeatManager) Remove(ctx context.Context, email string) error {
	return m.call(ctx, m.Endpoints.Remove, email, "")
}

// DomainEmail derives member addresses at a domain whose mail is routed by a catch-all rule.
type DomainEmail struct {
	Domain string
}

// Ensure returns name@domain.
func (d DomainEmail) Ensure(_ context.Context, name string) (string, error) {
	domain := strings.TrimSpace(d.Domain)
	if domain == "" || name == "" {
		return "", errors.New("domain and name are required")
	}
	return name + "@" + domain, nil
}
