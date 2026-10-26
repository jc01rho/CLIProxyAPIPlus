package lifecycle

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/teamshare"
)

func TestHTTPProxyRevokesTheKeyWhoseKeyIDMatches(t *testing.T) {
	var deleted string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Management-Key") != "secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/api-keys":
			_, _ = io.WriteString(w, `{"api-keys":["sk-other","sk-bob"]}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/v0/management/api-keys":
			deleted = r.URL.Query().Get("value")
			_, _ = io.WriteString(w, `{"status":"ok"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	proxy := &HTTPProxy{BaseURL: srv.URL, ManagementKey: "secret"}
	if err := proxy.RemoveAPIKeyByKeyID(context.Background(), teamshare.KeyID("sk-bob")); err != nil {
		t.Fatal(err)
	}
	if deleted != "sk-bob" {
		t.Fatalf("deleted %q, want sk-bob", deleted)
	}
	bad := &HTTPProxy{BaseURL: srv.URL, ManagementKey: "wrong"}
	if err := bad.RemoveAPIKeyByKeyID(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
}

func TestHTTPSeatManagerSendsCookieAndSubstitutesTemplates(t *testing.T) {
	var gotCookie, gotPath, gotBody, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCookie, gotPath, gotMethod = r.Header.Get("Cookie"), r.URL.EscapedPath(), r.Method
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	dir := t.TempDir()
	cookie := filepath.Join(dir, "cookie")
	if err := os.WriteFile(cookie, []byte("sessionKey=SECRET-COOKIE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seats := &HTTPSeatManager{Endpoints: SeatEndpoints{
		BaseURL: srv.URL, OrgID: "org-1", CookieFile: cookie,
		TierValues: map[string]string{"premium": "tier_premium"},
		Assign:     SeatCall{Method: http.MethodPut, Path: "/api/organizations/{org}/members/{email}/seat", Body: `{"tier":"{tier}"}`},
		Remove:     SeatCall{Method: http.MethodDelete, Path: "/api/organizations/{org}/members/{email}", OKStatuses: []int{http.StatusConflict}},
	}}
	if err := seats.Assign(context.Background(), "dana@junghap.top", "premium"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPut || gotPath != "/api/organizations/org-1/members/dana@junghap.top/seat" && gotPath != "/api/organizations/org-1/members/dana%40junghap.top/seat" {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
	if gotBody != `{"tier":"tier_premium"}` || gotCookie != "sessionKey=SECRET-COOKIE" {
		t.Fatalf("body %q cookie %q", gotBody, gotCookie)
	}
	if err := seats.Remove(context.Background(), "dana@junghap.top"); err != nil {
		t.Fatalf("an OKStatuses code must count as success: %v", err)
	}
}

func TestHTTPSeatManagerErrorsDoNotLeakTheCookie(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()
	cookie := filepath.Join(t.TempDir(), "cookie")
	if err := os.WriteFile(cookie, []byte("sessionKey=SECRET-COOKIE"), 0o600); err != nil {
		t.Fatal(err)
	}
	seats := &HTTPSeatManager{Endpoints: SeatEndpoints{BaseURL: srv.URL, OrgID: "o", CookieFile: cookie, Assign: SeatCall{Method: http.MethodPut, Path: "/x"}, Remove: SeatCall{Method: http.MethodDelete, Path: "/x"}}}
	err := seats.Assign(context.Background(), "a@b.c", "standard")
	if err == nil || strings.Contains(err.Error(), "SECRET-COOKIE") || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckReportsRefreshOutcomeAndPoolStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"files":[{"name":"dana.json","email":"dana@junghap.top"}]}`)
		case r.URL.Path == "/v0/management/auth-files/refresh" && r.Method == http.MethodPost:
			_, _ = io.WriteString(w, `{"ok":true,"results":[{"id":"dana.json","success":true}]}`)
		case r.URL.Path == "/v0/management/team-share":
			_, _ = io.WriteString(w, `{"pools":[{"name":"team","accounts":[{"ref":"dana@junghap.top","email":"dana@junghap.top","tier":"premium","status":"active","included_in_total":true}]}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	rep, err := Check(context.Background(), &HTTPProxy{BaseURL: srv.URL, ManagementKey: "k"}, "team", "dana@junghap.top")
	if err != nil {
		t.Fatal(err)
	}
	if !rep.RefreshOK || rep.AuthFile != "dana.json" || rep.Account == nil || rep.Account.Tier != "premium" || rep.Account.Status != "active" {
		t.Fatalf("report = %+v", rep)
	}
}
