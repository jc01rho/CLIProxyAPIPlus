package management

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/teamshare"
)

func teamShareHandlerFixture(t *testing.T) (*Handler, http.Handler, string) {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("team-share:\n  enabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(cfg, configPath, nil)
	router := setupTestRouter(handler)
	router.PUT("/team-share/pools/:pool/members/:name", handler.PutTeamShareMember)
	router.DELETE("/team-share/pools/:pool/members/:name", handler.DeleteTeamShareMember)
	router.PUT("/team-share/pools/:pool/accounts", handler.PutTeamShareAccount)
	router.DELETE("/team-share/pools/:pool/accounts", handler.DeleteTeamShareAccount)
	return handler, router, configPath
}

func doJSON(router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestTeamSharePoolEditsPersistAndEnforcePercentCap(t *testing.T) {
	_, router, configPath := teamShareHandlerFixture(t)

	if rec := doJSON(router, http.MethodPut, "/team-share/pools/team/accounts", `{"auth":"one@junghap.top","tier":"premium"}`); rec.Code != http.StatusOK {
		t.Fatalf("add account: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(router, http.MethodPut, "/team-share/pools/team/members/alice", `{"api_key":"sk-alice","percent":60}`); rec.Code != http.StatusOK {
		t.Fatalf("add alice: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(router, http.MethodPut, "/team-share/pools/team/members/bob", `{"api_key":"sk-bob","percent":40}`); rec.Code != http.StatusOK {
		t.Fatalf("add bob: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(router, http.MethodPut, "/team-share/pools/team/members/carol", `{"api_key":"sk-carol","percent":10}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a 110%% pool must be rejected, got %d %s", rec.Code, rec.Body.String())
	}

	reloaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	pool := reloaded.TeamShare.Pools[0]
	if len(pool.Members) != 2 || pool.Members[0].KeyID != teamshare.KeyID("sk-alice") || pool.Members[0].APIKey != "" {
		t.Fatalf("members must be stored by key-id only: %+v", pool.Members)
	}
	if pool.Accounts[0].Tier != "premium" {
		t.Fatalf("accounts = %+v", pool.Accounts)
	}

	if rec := doJSON(router, http.MethodDelete, "/team-share/pools/team/members/bob", ""); rec.Code != http.StatusOK {
		t.Fatalf("remove bob: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(router, http.MethodPut, "/team-share/pools/team/members/carol", `{"api_key":"sk-carol","percent":40}`); rec.Code != http.StatusOK {
		t.Fatalf("the freed share must be assignable again: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(router, http.MethodDelete, "/team-share/pools/team/members/ghost", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown member must be 404, got %d", rec.Code)
	}
	if rec := doJSON(router, http.MethodDelete, "/team-share/pools/team/accounts?auth=one@junghap.top", ""); rec.Code != http.StatusOK {
		t.Fatalf("remove account: %d %s", rec.Code, rec.Body.String())
	}
	final, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := final.TeamShare.Pools[0]; len(got.Accounts) != 0 || len(got.Members) != 2 {
		t.Fatalf("final pool = %+v", got)
	}
}
