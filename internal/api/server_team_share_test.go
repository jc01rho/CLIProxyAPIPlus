package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/teamshare"
)

func teamShareRouter(apiKey string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		teamShareGuard(c, apiKey)
	})
	router.Any("/v1/messages", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	return router
}

func TestTeamShareGuardRejectsExhaustedMemberWith429AndRemainingShare(t *testing.T) {
	engine := teamshare.Default()
	engine.SetAccountLister(func() []teamshare.AccountInfo {
		return []teamshare.AccountInfo{{ID: "guard-acc", Email: "guard@junghap.top", Provider: "claude"}}
	})
	cfg := config.TeamShareConfig{Enabled: true, Pools: []config.TeamSharePool{{
		Name:     "guard-pool",
		Accounts: []config.TeamShareAccount{{Auth: "guard@junghap.top", Tier: "standard"}},
		Members: []config.TeamShareMember{
			{Name: "heavy", APIKey: "sk-guard-heavy", Percent: 50},
			{Name: "light", APIKey: "sk-guard-light", Percent: 50},
		},
	}}}
	if err := engine.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Apply(config.TeamShareConfig{}) })

	post := func(key string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		teamShareRouter(key).ServeHTTP(rec, req)
		return rec
	}
	if rec := post("sk-guard-heavy"); rec.Code != http.StatusOK {
		t.Fatalf("fresh member must pass, got %d", rec.Code)
	}

	// standard seat = 6 USD per 5h, 50% = 3 USD; 250k sonnet output tokens = 3.75 USD.
	engine.Record(teamshare.Sample{AuthID: "guard-acc", APIKey: "sk-guard-heavy", Model: "claude-sonnet-5-5", Usage: teamshare.Usage{Output: 250_000}})

	rec := post("sk-guard-heavy")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("exhausted member must get 429, got %d: %s", rec.Code, rec.Body.String())
	}
	if secs, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || secs < 1 {
		t.Fatalf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	var body struct {
		Error struct {
			Code             string  `json:"code"`
			Window           string  `json:"window"`
			AllocatedUSD     float64 `json:"allocated_usd"`
			RemainingUSD     float64 `json:"remaining_usd"`
			RemainingPercent float64 `json:"remaining_percent"`
			ResetAt          string  `json:"reset_at"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "team_share_exceeded" || body.Error.Window != teamshare.WindowFiveHour || body.Error.AllocatedUSD != 3 || body.Error.RemainingUSD != 0 || body.Error.ResetAt == "" {
		t.Fatalf("error body = %+v", body.Error)
	}

	if rec := post("sk-guard-light"); rec.Code != http.StatusOK {
		t.Fatalf("the other member must still pass, got %d", rec.Code)
	}
	getRec := httptest.NewRecorder()
	teamShareRouter("sk-guard-heavy").ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/v1/messages", nil))
	if getRec.Code != http.StatusOK {
		t.Fatalf("read-only requests must pass, got %d", getRec.Code)
	}
}

func TestTeamSharePanelIsServedAsSingleHTMLPage(t *testing.T) {
	server := newTestServer(t)
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/team-share.html", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("status %d content-type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), "/v0/management/team-share") {
		t.Fatal("panel must load /v0/management/team-share")
	}
}
