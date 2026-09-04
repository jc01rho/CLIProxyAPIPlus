package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestClaudeQuotaURLResolution(t *testing.T) {
	cases := []struct {
		name string
		auth *auth.Auth
		want string
	}{
		{"explicit quota_url wins", &auth.Auth{Attributes: map[string]string{"quota_url": "https://q.example.com/custom", "base_url": "https://b.example.com"}}, "https://q.example.com/custom"},
		{"base_url fallback", &auth.Auth{Attributes: map[string]string{"base_url": "https://claude.nekos.me/"}}, "https://claude.nekos.me/v1/usage/self"},
		{"default", &auth.Auth{}, "https://api.anthropic.com/v1/usage/self"},
		{"nil", nil, "https://api.anthropic.com/v1/usage/self"},
	}
	for _, tc := range cases {
		if got := claudeQuotaURL(tc.auth); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestClaudeQuotaThirdPartyFormat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	doc := `{"request_count":4684,"total_tokens":1405236445,"total_cost_usd":3716.025941,"limits":[{"limit_type":"cost_usd","limit_window":"3h","max_value":765625000,"current_value":1400516,"remaining_value":764224484,"used_percent":0.18,"model_filter":null,"reset_at":"2026-09-04T07:27:07.403949"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/usage/self" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(doc))
	}))
	defer srv.Close()

	target := &auth.Auth{Attributes: map[string]string{"quota_url": srv.URL + "/v1/usage/self", "api_key": "k"}}
	h := &Handler{}
	quota, err := h.fetchClaudeQuota(t.Context(), target, "k")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if quota.RequestCount != 4684 || quota.TotalTokens != 1405236445 {
		t.Fatalf("summary mismatch: %+v", quota)
	}
	if len(quota.Limits) != 1 {
		t.Fatalf("limits: %+v", quota.Limits)
	}
	if quota.Limits[0].LimitWindow != "3h" || quota.Limits[0].UsedPercent != 0.18 {
		t.Fatalf("limit: %+v", quota.Limits[0])
	}
	if quota.Limits[0].ModelFilter != nil {
		t.Fatalf("model_filter should be nil: %+v", quota.Limits[0])
	}
	var _ = json.Marshal
}
