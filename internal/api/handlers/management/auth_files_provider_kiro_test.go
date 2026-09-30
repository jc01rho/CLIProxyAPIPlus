package management

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	kiro "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type kiroProbeTransport func(*http.Request) (*http.Response, error)

func (f kiroProbeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestKiroSocialLoginProbesOtherCredentialsAndReportsSignedOut(t *testing.T) {
	// No parallelism: replace the default transport only for this synchronous test.
	originalTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	calls := map[string]int{}
	http.DefaultTransport = kiroProbeTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/refreshToken" {
			return nil, errors.New("unexpected request")
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		id := body["refreshToken"]
		calls[id]++
		status := http.StatusOK
		switch id {
		case "bad-request":
			status = http.StatusBadRequest
		case "unauthorized":
			status = http.StatusUnauthorized
		case "forbidden":
			status = http.StatusForbidden
		case "server-error":
			status = http.StatusInternalServerError
		case "rate-limit":
			status = http.StatusTooManyRequests
		case "transport":
			return nil, errors.New("connection reset")
		}
		response := `{"accessToken":"fresh-access","expiresIn":3600}`
		if id == "rotated" {
			response = `{"accessToken":"fresh-access","refreshToken":"rotated-token","expiresIn":3600}`
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
	})
	store := sdkauth.NewFileTokenStore()
	dir := t.TempDir()
	store.SetBaseDir(dir)
	manager := coreauth.NewManager(nil, nil, nil)
	h := &Handler{cfg: &config.Config{AuthDir: dir}, authManager: manager, tokenStore: store}
	for _, id := range []string{"new-login", "bad-request", "unauthorized", "forbidden", "server-error", "rate-limit", "transport", "valid", "rotated", "builder", "enterprise", "oidc-without-method", "disabled", "other-provider", "no-refresh"} {
		a := &coreauth.Auth{ID: id + ".json", FileName: id + ".json", Label: id + " label", Provider: "kiro", Status: coreauth.StatusActive, Metadata: map[string]any{"access_token": "old-access", "refresh_token": id, "expires_at": "2099-01-01T00:00:00Z", "auth_method": "social"}}
		switch id {
		case "builder":
			a.Metadata["auth_method"] = "builder-id"
		case "enterprise":
			a.Metadata["auth_method"] = "idc"
		case "oidc-without-method":
			a.Metadata["auth_method"] = ""
			a.Metadata["client_id"] = "client"
			a.Metadata["client_secret"] = "secret"
		case "disabled":
			a.Disabled = true
			a.Status = coreauth.StatusDisabled
		case "other-provider":
			a.Provider = "claude"
		case "no-refresh":
			delete(a.Metadata, "refresh_token")
		}
		if id == "forbidden" {
			a.Storage = &kiro.KiroTokenStorage{Type: "kiro", AccessToken: "stale-storage-token", RefreshToken: id}
		}
		if _, err := store.Save(coreauth.WithAuthCreationIntent(context.Background()), a); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Register(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := manager.GetByID("transport.json")
	labels := h.probeKiroSocialCredentials(context.Background(), "new-login.json")
	sort.Strings(labels)
	if want := []string{"bad-request label", "forbidden label", "unauthorized label"}; !reflect.DeepEqual(labels, want) {
		t.Fatalf("signedOut=%v want=%v", labels, want)
	}
	for _, id := range []string{"new-login", "builder", "enterprise", "oidc-without-method", "disabled", "other-provider", "no-refresh"} {
		if calls[id] != 0 {
			t.Errorf("unexpected probe of %s", id)
		}
	}
	for _, id := range []string{"bad-request", "unauthorized", "forbidden", "server-error", "rate-limit", "transport", "valid", "rotated"} {
		if calls[id] != 1 {
			t.Errorf("probe count %s=%d", id, calls[id])
		}
	}
	for _, id := range []string{"bad-request", "unauthorized", "forbidden"} {
		a, _ := manager.GetByID(id + ".json")
		if !a.Disabled || a.Status != coreauth.StatusDisabled || a.StatusMessage == "" {
			t.Errorf("%s not disabled: %+v", id, a)
		}
		data, err := os.ReadFile(filepath.Join(dir, id+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var persisted map[string]any
		if err := json.Unmarshal(data, &persisted); err != nil {
			t.Fatal(err)
		}
		if persisted["disabled"] != true || persisted["status_message"] == "" {
			t.Errorf("%s disable not persisted: %s", id, data)
		}
	}
	after, _ := manager.GetByID("transport.json")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("transport failure changed credential state")
	}
	for _, id := range []string{"server-error", "rate-limit"} {
		a, _ := manager.GetByID(id + ".json")
		if a.Disabled || a.Metadata["access_token"] != "old-access" {
			t.Errorf("transient %s changed credential", id)
		}
	}
	for _, id := range []string{"valid", "rotated"} {
		a, _ := manager.GetByID(id + ".json")
		want := id
		if id == "rotated" {
			want = "rotated-token"
		}
		if a.Metadata["access_token"] != "fresh-access" || a.Metadata["refresh_token"] != want {
			t.Errorf("%s not refreshed correctly: %v", id, a.Metadata)
		}
	}

	sessions := newOAuthSessionStore(time.Minute)
	replaceOAuthSessionStoreForTest(t, sessions)
	RegisterOAuthSessionWithMetadata("kiro-completed", "kiro", map[string]any{"secret": "not-public"})
	completeKiroOAuthSession("kiro-completed", labels)
	router := gin.New()
	router.GET("/status", h.GetAuthStatus)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/status?state=kiro-completed", nil))
	var response struct {
		Status    string   `json:"status"`
		SignedOut []string `json:"signedOut"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || response.Status != "ok" || !reflect.DeepEqual(response.SignedOut, labels) {
		t.Fatalf("status response=%s", w.Body.String())
	}
	_, _, _, metadata, completed, _ := GetOAuthSessionDetails("kiro-completed")
	if !completed || len(metadata) != 1 || metadata["secret"] != nil {
		t.Fatalf("completed metadata=%v", metadata)
	}
}

func TestKiroCompletionDoesNotReviveCancelledSession(t *testing.T) {
	replaceOAuthSessionStoreForTest(t, newOAuthSessionStore(time.Minute))
	RegisterOAuthSession("kiro-cancelled", "kiro")
	if !CancelOAuthSession("kiro-cancelled") {
		t.Fatal("cancel failed")
	}
	completeKiroOAuthSession("kiro-cancelled", []string{"old account"})
	if _, _, _, _, _, ok := GetOAuthSessionDetails("kiro-cancelled"); ok {
		t.Fatal("cancelled session revived")
	}
}
