package kiro

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestParseDeviceLoginKind(t *testing.T) {
	t.Parallel()
	cases := map[string]DeviceLoginKind{
		"":           DeviceLoginBuilderID,
		"builder-id": DeviceLoginBuilderID,
		"BuilderId":  DeviceLoginBuilderID,
		"aws":        DeviceLoginBuilderID,
		"google":     DeviceLoginGoogle,
		"GitHub":     DeviceLoginGitHub,
		"github.com": DeviceLoginGitHub,
	}
	for raw, want := range cases {
		got, err := ParseDeviceLoginKind(raw)
		if err != nil {
			t.Fatalf("ParseDeviceLoginKind(%q): %v", raw, err)
		}
		if got != want {
			t.Fatalf("ParseDeviceLoginKind(%q) = %q, want %q", raw, got, want)
		}
	}
	if _, err := ParseDeviceLoginKind("cognito"); err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestStartAndPollSocialDeviceLogin(t *testing.T) {
	var pollCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/device/authorization":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode authorization: %v", err)
			}
			if body["clientId"] != kiroDeviceClientID || body["loginProvider"] != "Google" {
				t.Errorf("authorization body = %#v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{
				"deviceCode":"dev-1",
				"userCode":"ABCD-1234",
				"verificationUri":"https://kiro.dev/activate",
				"verificationUriComplete":"https://kiro.dev/activate?user_code=ABCD-1234",
				"expiresInMilliseconds":300000,
				"intervalInMilliseconds":1000
			}`)
		case "/oauth/device/poll":
			pollCount++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode poll: %v", err)
			}
			if body["deviceCode"] != "dev-1" {
				t.Errorf("poll body = %#v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			if pollCount == 1 {
				_, _ = io.WriteString(w, `{"status":"authorization_pending"}`)
				return
			}
			_, _ = io.WriteString(w, `{
				"accessToken":"at-social",
				"refreshToken":"rt-social",
				"profileArn":"arn:aws:codewhisperer:us-east-1:1:profile/ABC",
				"identityProvider":"Google",
				"expiresIn":3600
			}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	client := NewDeviceLoginClient(nil)
	client.authHost = server.URL

	flow, err := client.Start(context.Background(), DeviceLoginGoogle)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if flow.UserCode != "ABCD-1234" || !strings.Contains(flow.VerificationURIComplete, "ABCD-1234") {
		t.Fatalf("flow = %+v", flow)
	}
	if flow.ExpiresIn != 5*time.Minute || flow.Interval != time.Second {
		t.Fatalf("timings = expires %s interval %s", flow.ExpiresIn, flow.Interval)
	}

	if _, err := client.Poll(context.Background(), flow); !errors.Is(err, ErrDeviceAuthorizationPending) {
		t.Fatalf("first poll err = %v, want pending", err)
	}
	token, err := client.Poll(context.Background(), flow)
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if token.AccessToken != "at-social" || token.ProfileArn == "" || token.AuthMethod != "social" {
		t.Fatalf("token = %+v", token)
	}
	data := token.ToTokenData()
	if data.ProfileArn != token.ProfileArn || data.Region != kiroDeviceSocialRegion {
		t.Fatalf("token data = %+v", data)
	}
}

func TestStartAndPollBuilderIDDeviceLogin(t *testing.T) {
	var tokenCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/client/register":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"clientId":"cid","clientSecret":"csec"}`)
		case "/device_authorization":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{
				"deviceCode":"oidc-dev",
				"userCode":"WXYZ-9999",
				"verificationUri":"https://view.awsapps.com/start/#/device",
				"verificationUriComplete":"https://view.awsapps.com/start/#/device?user_code=WXYZ-9999",
				"expiresIn":600,
				"interval":5
			}`)
		case "/token":
			tokenCalls++
			if tokenCalls == 1 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"authorization_pending"}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"accessToken":"at-builder","refreshToken":"rt-builder","expiresIn":3600}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	client := NewDeviceLoginClient(&config.Config{
		OAuthEndpointOverrides: map[string]config.OAuthEndpointConfig{
			"kiro": {ApiBaseURL: server.URL},
		},
	})

	flow, err := client.Start(context.Background(), DeviceLoginBuilderID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if flow.ClientID != "cid" || flow.ClientSecret != "csec" || flow.UserCode != "WXYZ-9999" {
		t.Fatalf("flow = %+v", flow)
	}
	if _, err := client.Poll(context.Background(), flow); !errors.Is(err, ErrDeviceAuthorizationPending) {
		t.Fatalf("first poll err = %v, want pending", err)
	}
	token, err := client.Poll(context.Background(), flow)
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if token.ProfileArn != "" || token.AuthMethod != "builder-id" || token.ClientID != "cid" || token.StartURL == "" {
		t.Fatalf("token = %+v", token)
	}
}

func TestPollSocialDeviceLoginExpired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"expired_token"}`)
	}))
	t.Cleanup(server.Close)

	client := NewDeviceLoginClient(nil)
	client.authHost = server.URL
	_, err := client.Poll(context.Background(), &DeviceLoginFlow{
		Kind:       DeviceLoginGitHub,
		DeviceCode: "gone",
	})
	if !errors.Is(err, ErrDeviceExpired) {
		t.Fatalf("err = %v, want expired", err)
	}
}
