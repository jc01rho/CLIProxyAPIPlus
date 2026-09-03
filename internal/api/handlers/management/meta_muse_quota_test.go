package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func newMetaMuseQuotaHandler(t *testing.T, quota coreauth.QuotaState) *Handler {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "meta-auth",
		Provider: "openai-compatible-meta",
		Quota:    quota,
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register Meta auth: %v", err)
	}
	return NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
}

func performGetMetaMuseQuota(t *testing.T, handler *Handler) (int, map[string]any) {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/meta-muse-quota", nil)
	handler.GetMetaMuseQuota(ctx)

	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response body %q: %v", recorder.Body.String(), err)
	}
	return recorder.Code, body
}

func TestGetMetaMuseQuota_when_PassiveSubscriptionSnapshotCached(t *testing.T) {
	// Given
	observedAt := time.Unix(1787279282, 0).UTC()
	var quota coreauth.QuotaState
	if !quota.ObserveResponseHeadersForProvider("openai-compatible-meta", http.Header{
		"X-Muse-Fivehour-Used-Percent": {"42.5"},
		"X-Muse-Fivehour-Reset-At":     {"1788431188"},
		"X-Muse-Weekly-Used-Percent":   {"63"},
		"X-Muse-Weekly-Reset-At":       {"1788739200"},
	}, observedAt) {
		t.Fatal("seed quota observation was not recorded")
	}
	handler := newMetaMuseQuotaHandler(t, quota)

	// When
	status, body := performGetMetaMuseQuota(t, handler)

	// Then
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", status, body)
	}
	if got := body["five_hour_used_percent"]; got != 42.5 {
		t.Fatalf("five_hour_used_percent = %v, want 42.5", got)
	}
	assertMetaMuseEpoch(t, body["five_hour_reset_at"], 1788431188)
	if got := body["weekly_used_percent"]; got != 63.0 {
		t.Fatalf("weekly_used_percent = %v, want 63", got)
	}
	assertMetaMuseEpoch(t, body["weekly_reset_at"], 1788739200)
	assertMetaMuseEpoch(t, body["observed_at"], observedAt.Unix())
}

func TestGetMetaMuseQuota_when_NoPassiveSnapshotExists(t *testing.T) {
	// Given
	handler := newMetaMuseQuotaHandler(t, coreauth.QuotaState{})

	// When
	status, body := performGetMetaMuseQuota(t, handler)

	// Then
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %v", status, body)
	}
}

func assertMetaMuseEpoch(t *testing.T, raw any, want int64) {
	t.Helper()
	value, ok := raw.(string)
	if !ok {
		t.Fatalf("time value = %T(%v), want RFC3339 string", raw, raw)
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse %q as RFC3339: %v", value, err)
	}
	if got := parsed.Unix(); got != want {
		t.Fatalf("epoch = %d, want %d", got, want)
	}
}
