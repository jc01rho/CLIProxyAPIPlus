package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type kiroRefreshTransport func(*http.Request) (*http.Response, error)

func (f kiroRefreshTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestKiroAuthenticatorRefreshRetainsOmittedRefreshToken(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, replacement := range []string{"", " ", "rotated-refresh"} {
		t.Run("replacement="+replacement, func(t *testing.T) {
			calls := 0
			http.DefaultTransport = kiroRefreshTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Path != "/refreshToken" || r.Method != http.MethodPost {
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
				}
				var payload map[string]string
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					return nil, err
				}
				if payload["refreshToken"] != "original-refresh" {
					t.Errorf("refresh payload = %v", payload)
				}
				body := map[string]any{"accessToken": "new-access", "expiresIn": 3600}
				if replacement != "" {
					body["refreshToken"] = replacement
				}
				data, err := json.Marshal(body)
				if err != nil {
					return nil, err
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(data))), Header: make(http.Header)}, nil
			})
			record := &coreauth.Auth{ID: "kiro-test", Provider: "kiro", Metadata: map[string]any{"access_token": "old-access", "refresh_token": "original-refresh", "auth_method": "social"}}
			updated, err := NewKiroAuthenticator().Refresh(context.Background(), &config.Config{}, record)
			if err != nil {
				t.Fatal(err)
			}
			want := replacement
			if strings.TrimSpace(want) == "" {
				want = "original-refresh"
			}
			if calls != 1 || updated.Metadata["refresh_token"] != want || updated.Metadata["access_token"] != "new-access" {
				t.Fatalf("updated=%v calls=%d", updated.Metadata, calls)
			}
			if record.Metadata["access_token"] != "old-access" || record.Metadata["refresh_token"] != "original-refresh" {
				t.Fatal("input record was mutated")
			}
		})
	}
}

func TestKiroAuthenticatorImportRefreshOnlyDueImmediately(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, "import.json")
	t.Setenv("KIRO_CREDS_FILE", path)
	if err := os.WriteFile(path, []byte(`{"refreshToken":"refresh","expiresAt":"2099-01-01T00:00:00Z","authMethod":"social"}`), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := NewKiroAuthenticator().ImportFromKiroIDE(context.Background(), &config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Metadata["access_token"] != "" || got.Metadata["refresh_token"] != "refresh" || got.Metadata["expires_at"] != "1970-01-01T00:00:00Z" {
		t.Fatalf("metadata=%v", got.Metadata)
	}
	if want := time.Unix(0, 0).Add(-20 * time.Minute); !got.NextRefreshAfter.Equal(want) {
		t.Fatalf("next refresh=%v want=%v", got.NextRefreshAfter, want)
	}
}

func TestKiroAuthenticatorImportDoesNotHideInvalidCredential(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, "import.json")
	t.Setenv("KIRO_CREDS_FILE", path)
	if err := os.WriteFile(path, []byte(`{"accessToken":"access","region":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	idePath := filepath.Join(home, kiroauth.KiroIDETokenFile)
	if err := os.MkdirAll(filepath.Dir(idePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(idePath, []byte(`{"accessToken":"fallback-access"}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := NewKiroAuthenticator().ImportFromKiroIDE(context.Background(), &config.Config{})
	var fieldErr *kiroauth.CredentialFieldError
	if !errors.As(err, &fieldErr) || fieldErr.Field != "region" {
		t.Fatalf("error=%v want invalid region", err)
	}
}
