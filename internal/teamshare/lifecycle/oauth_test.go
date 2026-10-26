package lifecycle

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/teamshare/oauthlogin"
)

func writeCredential(t *testing.T, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "passkey.json")
	raw, _ := json.Marshal(storedCredential{CredentialID: "Y3JlZA", PrivateKeyPKCS8: "a2V5", RPID: "sso.example.test", SignCount: 4})
	if err := os.WriteFile(path, raw, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBrowserOAuthRegistersCredentialAndPersistsPasskeyCounter(t *testing.T) {
	var polls int32
	var callbackBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0/management/anthropic-auth-url":
			_, _ = io.WriteString(w, `{"status":"ok","url":"https://login.example.test/authorize?x=1","state":"st-1"}`)
		case "/v0/management/oauth-callback":
			raw, _ := io.ReadAll(r.Body)
			callbackBody = string(raw)
			_, _ = io.WriteString(w, `{"status":"ok"}`)
		case "/v0/management/get-auth-status":
			if atomic.AddInt32(&polls, 1) < 2 {
				_, _ = io.WriteString(w, `{"status":"wait"}`)
				return
			}
			_, _ = io.WriteString(w, `{"status":"ok"}`)
		case "/v0/management/auth-files":
			_, _ = io.WriteString(w, `{"files":[{"name":"dana@junghap.top.json","email":"dana@junghap.top"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	credPath := writeCredential(t, 0o600)
	var gotOpts oauthlogin.Options
	var gotURL string
	reg := &BrowserOAuth{
		Proxy: &HTTPProxy{BaseURL: srv.URL, ManagementKey: "k"},
		Config: OAuthConfig{
			CredentialFile: credPath,
			Steps:          []OAuthStep{{Action: "fill", Selector: "#email", Value: "{email}"}, {Action: "click", Selector: "#sso"}},
		},
		Run: func(_ context.Context, authURL string, opts oauthlogin.Options) (oauthlogin.Result, error) {
			gotURL, gotOpts = authURL, opts
			return oauthlogin.Result{FinalURL: "http://localhost:54545/callback?code=C&state=st-1", SignCount: 9}, nil
		},
		PollInterval: time.Millisecond,
	}
	file, err := reg.Register(context.Background(), "dana@junghap.top")
	if err != nil {
		t.Fatal(err)
	}
	if file != "dana@junghap.top.json" || gotURL != "https://login.example.test/authorize?x=1" {
		t.Fatalf("file %q url %q", file, gotURL)
	}
	if gotOpts.Steps[0].Value != "dana@junghap.top" || gotOpts.CallbackURLPrefix != defaultCallbackPrefix || !gotOpts.Headless {
		t.Fatalf("options = %+v", gotOpts)
	}
	if gotOpts.Credential == nil || gotOpts.Credential.SignCount != 4 || gotOpts.Credential.RPID != "sso.example.test" {
		t.Fatalf("credential = %+v", gotOpts.Credential)
	}
	if !strings.Contains(callbackBody, `"provider":"anthropic"`) || !strings.Contains(callbackBody, "code=C") {
		t.Fatalf("callback body = %s", callbackBody)
	}
	saved, err := loadCredential(credPath)
	if err != nil || saved.SignCount != 9 {
		t.Fatalf("persisted counter = %d err=%v", saved.SignCount, err)
	}
}

func TestBrowserOAuthRefusesAWorldReadablePasskeyFile(t *testing.T) {
	credPath := writeCredential(t, 0o644)
	reg := &BrowserOAuth{
		Proxy:  &HTTPProxy{BaseURL: "http://127.0.0.1:1", ManagementKey: "k"},
		Config: OAuthConfig{CredentialFile: credPath, Steps: []OAuthStep{{Action: "click", Selector: "#x"}}},
		Run: func(context.Context, string, oauthlogin.Options) (oauthlogin.Result, error) {
			t.Fatal("the browser must not start with an unsafe credential file")
			return oauthlogin.Result{}, nil
		},
	}
	if _, err := reg.Register(context.Background(), "dana@junghap.top"); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("err = %v", err)
	}
}
