// Package alysis provides authentication and token management for the
// Alysis Code Pro hosted service (`alysiscode.com`).
//
// Login follows an RFC 8628-style device flow against Supabase Edge Functions:
// the CLI requests a short user code, the user approves it on the website's
// /activate page, and the CLI polls until it receives a long-lived gateway key
// (“slk_...“). The gateway is an OpenAI-compatible proxy that meters the
// subscription's credits server-side.
package alysis

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Default endpoints for the Alysis Code hosted backend. They mirror the
// constants in the reference CLI (pypi alysis-code, src/alysis_code/
// alysis_cloud.py) so both clients talk to the same deployment.
const (
	// ProductSiteURL is the product site serving the /activate approval page.
	ProductSiteURL = "https://alysiscode.com"

	// DeviceCodePath starts a device login (returns user_code + device_code).
	DeviceCodePath = "/functions/v1/device-code"

	// DeviceTokenPath exchanges a device_code for the gateway key once the
	// user approves it on /activate.
	DeviceTokenPath = "/functions/v1/device-token"

	// GatewayPathPrefix is the OpenAI-compatible gateway base path.
	GatewayPathPrefix = "/functions/v1/llm/v1"

	// AnonKey is the Supabase public anon key shipped in the reference CLI.
	// It is a public client identifier: the device endpoints are public by
	// design and verify_jwt is disabled server-side for them.
	AnonKey = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." +
		"eyJpc3MiOiJzdXBhYmFzZSIsInJlZiI6InZ6aWd1amJjamptcG50eGhteXZyIiwicm9sZSI6ImFub24iLCJpYXQiOjE3ODA5Mzc0NTIsImV4cCI6MjA5NjUxMzQ1Mn0." +
		"vLH9q-BNO8IWIZrVlvCw8pZWXdLgmKG4Tl9toTTD3pg"
)

// SupabaseURL is the Supabase project hosting the device-login edge
// functions. It is a variable so tests can point it at a local stub.
var SupabaseURL = "https://vzigujbcjjmpntxhmyvr.supabase.co"

// DeviceCodeResponse is the payload returned by POST device-code.
type DeviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURL         string `json:"verification_url"`
	VerificationURLComplete string `json:"verification_url_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// DeviceTokenResponse is the payload returned by POST device-token.
type DeviceTokenResponse struct {
	Status string `json:"status"`
	Key    string `json:"key"`
}

// Auth drives the Alysis Code device login flow.
type Auth struct {
	client *http.Client
}

// NewAuth creates an Auth instance with the default HTTP client.
func NewAuth() *Auth {
	return &Auth{client: &http.Client{Timeout: 30 * time.Second}}
}

// InitiateDeviceFlow starts the device flow and returns the grant.
func (a *Auth) InitiateDeviceFlow(ctx context.Context) (*DeviceCodeResponse, error) {
	body, err := a.postJSON(ctx, DeviceCodePath, strings.NewReader(`{"client_name":"cli-proxy"}`))
	if err != nil {
		return nil, err
	}
	var grant DeviceCodeResponse
	if err = json.Unmarshal(body, &grant); err != nil {
		return nil, fmt.Errorf("alysis device flow: invalid device-code response: %w", err)
	}
	if strings.TrimSpace(grant.DeviceCode) == "" || strings.TrimSpace(grant.UserCode) == "" {
		return nil, fmt.Errorf("alysis device flow: device-code response missing codes")
	}
	if grant.ExpiresIn <= 0 {
		grant.ExpiresIn = 900
	}
	if grant.Interval <= 0 {
		grant.Interval = 5
	}
	return &grant, nil
}

// PollForToken polls device-token until the user approves the code. HTTP and
// status semantics mirror the reference CLI: status "approved" carries the
// gateway key, "denied" and "expired"/"not_found"/"already_claimed" abort,
// anything else keeps polling. Polling always respects ctx cancellation.
func (a *Auth) PollForToken(ctx context.Context, deviceCode string) (*DeviceTokenResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if deviceCode == "" {
		return nil, fmt.Errorf("alysis device flow: device code is required")
	}

	// Probe immediately so a just-approved code does not wait a full interval.
	if status, done, err := a.pollOnce(ctx, deviceCode); done || err != nil {
		return status, err
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			status, done, err := a.pollOnce(ctx, deviceCode)
			if err != nil || done {
				return status, err
			}
		}
	}
}

func (a *Auth) pollOnce(ctx context.Context, deviceCode string) (*DeviceTokenResponse, bool, error) {
	payload := fmt.Sprintf(`{"device_code":%q}`, deviceCode)
	body, err := a.postJSON(ctx, DeviceTokenPath, strings.NewReader(payload))
	if err != nil {
		return nil, true, err
	}
	var resp DeviceTokenResponse
	if err = json.Unmarshal(body, &resp); err != nil {
		return nil, true, fmt.Errorf("alysis device flow: invalid device-token response: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(resp.Status)) {
	case "approved":
		if strings.TrimSpace(resp.Key) == "" {
			return nil, true, fmt.Errorf("alysis device flow: approved response missing key")
		}
		return &resp, true, nil
	case "denied":
		return nil, true, fmt.Errorf("alysis device flow: login was rejected on the website")
	case "expired", "not_found", "already_claimed":
		return nil, true, fmt.Errorf("alysis device flow: login code expired")
	default: // "pending" or unknown: keep waiting.
		return nil, false, nil
	}
}

// postJSON POSTs a JSON payload to a Supabase edge function with the public
// anon key headers. The device endpoints are public by design; the gateway
// (chat/models) calls use per-user slk_ keys instead.
func (a *Auth) postJSON(ctx context.Context, path string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, SupabaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", AnonKey)
	req.Header.Set("Authorization", "Bearer "+AnonKey)

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("alysis device flow: request failed: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("alysis device flow: failed to read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("alysis device flow: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}
