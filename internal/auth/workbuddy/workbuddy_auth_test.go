package workbuddy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestFetchAuthStateUsesGlobalWorkBuddyHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != statePath || r.URL.Query().Get("platform") != "CLI" {
			t.Fatalf("request = %s %s, want POST %s?platform=CLI", r.Method, r.URL.String(), statePath)
		}
		for header, want := range map[string]string{
			"Origin":              BaseURL,
			"Referer":             BaseURL + "/",
			"X-Domain":            DefaultDomain,
			"X-No-Authorization":  "true",
			"X-No-Enterprise-Id":  "true",
			"X-CodeBuddy-Request": "1",
			"User-Agent":          AuthUserAgent,
		} {
			if got := r.Header.Get(header); got != want {
				t.Errorf("%s = %q, want %q", header, got, want)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"state": "state-1", "authUrl": "https://www.workbuddy.ai/authorize?state=state-1"},
		})
	}))
	t.Cleanup(server.Close)

	auth := &WorkBuddyAuth{httpClient: server.Client(), baseURL: server.URL}
	state, err := auth.FetchAuthState(context.Background())
	if err != nil {
		t.Fatalf("FetchAuthState() error = %v", err)
	}
	if state.State != "state-1" {
		t.Fatalf("state = %q, want state-1", state.State)
	}
}

func TestTokenStoragePersistsIndependentWorkBuddyType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workbuddy-user.json")
	storage := &TokenStorage{AccessToken: "at", RefreshToken: "rt", UserID: "uid"}
	storage.SetMetadata(map[string]any{"disabled": false, "note": "keep"})
	if err := storage.SaveTokenToFile(path); err != nil {
		t.Fatalf("SaveTokenToFile() error = %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err = json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["type"] != "workbuddy" || saved["realm"] != "global" || saved["domain"] != DefaultDomain {
		t.Fatalf("saved identity = %#v", saved)
	}
	if saved["note"] != "keep" {
		t.Fatalf("metadata note = %#v, want keep", saved["note"])
	}
}
