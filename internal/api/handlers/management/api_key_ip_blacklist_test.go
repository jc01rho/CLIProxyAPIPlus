package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestAPIKeyIPBlacklistStoreWindowAndUnban(t *testing.T) {
	store := NewAPIKeyIPBlacklistStore(DefaultAPIKeyIPBlacklistPolicy())
	base := time.Date(2026, time.April, 22, 12, 0, 0, 0, time.UTC)
	store.nowFn = func() time.Time { return base }

	entry, blocked := store.RecordFailure("1.2.3.4")
	if blocked {
		t.Fatalf("first failure should not block: %+v", entry)
	}
	store.nowFn = func() time.Time { return base.Add(11 * time.Minute) }
	entry, blocked = store.RecordFailure("1.2.3.4")
	if blocked {
		t.Fatalf("expired window should not block: %+v", entry)
	}
	store.nowFn = func() time.Time { return base.Add(11*time.Minute + 30*time.Second) }
	_, blocked = store.RecordFailure("1.2.3.4")
	if blocked {
		t.Fatalf("second in fresh window should not block")
	}
	_, blocked = store.RecordFailure("1.2.3.4")
	if !blocked {
		t.Fatalf("third failure in fresh window should trigger blocked state")
	}
	if _, ok := store.IsBlocked("1.2.3.4"); !ok {
		t.Fatalf("expected IP to be blocked")
	}
	if !store.Unban("1.2.3.4") {
		t.Fatalf("expected unban to succeed")
	}
	if _, ok := store.IsBlocked("1.2.3.4"); ok {
		t.Fatalf("expected IP to be unblocked")
	}
}

func TestAPIKeyIPBlacklistStoreManualBan(t *testing.T) {
	store := NewAPIKeyIPBlacklistStore(DefaultAPIKeyIPBlacklistPolicy())
	base := time.Date(2026, time.April, 22, 12, 0, 0, 0, time.UTC)
	store.nowFn = func() time.Time { return base }

	entry, ok := store.ManualBan("1.2.3.4")
	if !ok {
		t.Fatalf("expected manual ban to succeed")
	}
	if entry.IP != "1.2.3.4" {
		t.Fatalf("unexpected entry IP: %+v", entry)
	}
	if entry.BlockedUntil == "" {
		t.Fatalf("expected blocked until to be set: %+v", entry)
	}
	if _, blocked := store.IsBlocked("1.2.3.4"); !blocked {
		t.Fatalf("expected IP to be blocked after manual ban")
	}
}

func TestHandlerAPIKeyIPBlacklistEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHandler(&config.Config{}, "", nil)
	base := time.Date(2026, time.April, 22, 12, 0, 0, 0, time.UTC)
	h.apiKeyIPBlacklist.nowFn = func() time.Time { return base }
	for i := 0; i < 3; i++ {
		h.apiKeyIPBlacklist.RecordFailure("10.0.0.7")
	}

	r := gin.New()
	r.GET("/api-key-ip-blacklist", h.GetAPIKeyIPBlacklist)
	r.POST("/api-key-ip-blacklist", h.PostAPIKeyIPBlacklist)
	r.DELETE("/api-key-ip-blacklist", h.DeleteAPIKeyIPBlacklist)

	getReq := httptest.NewRequest(http.MethodGet, "/api-key-ip-blacklist", nil)
	getRes := httptest.NewRecorder()
	r.ServeHTTP(getRes, getReq)
	if getRes.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", getRes.Code)
	}
	var payload struct {
		BlockedIPs []APIKeyIPBlacklistEntry `json:"blocked-ips"`
	}
	if err := json.Unmarshal(getRes.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if len(payload.BlockedIPs) != 1 || payload.BlockedIPs[0].IP != "10.0.0.7" {
		t.Fatalf("unexpected blocked IP payload: %+v", payload.BlockedIPs)
	}

	postReq := httptest.NewRequest(http.MethodPost, "/api-key-ip-blacklist", bytes.NewBufferString(`{"ip":"10.0.0.8"}`))
	postReq.Header.Set("Content-Type", "application/json")
	postRes := httptest.NewRecorder()
	r.ServeHTTP(postRes, postReq)
	if postRes.Code != http.StatusOK {
		t.Fatalf("expected 200 on manual ban, got %d", postRes.Code)
	}
	if _, ok := h.apiKeyIPBlacklist.IsBlocked("10.0.0.8"); !ok {
		t.Fatalf("expected manual ban endpoint to create blocked state")
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api-key-ip-blacklist?ip=10.0.0.7", nil)
	deleteRes := httptest.NewRecorder()
	r.ServeHTTP(deleteRes, deleteReq)
	if deleteRes.Code != http.StatusOK {
		t.Fatalf("expected 200 on unban, got %d", deleteRes.Code)
	}
	if _, ok := h.apiKeyIPBlacklist.IsBlocked("10.0.0.7"); ok {
		t.Fatalf("expected unban endpoint to clear blocked state")
	}
}

func TestHandlerAPIKeyIPBlacklistManualBanValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHandler(&config.Config{}, "", nil)
	r := gin.New()
	r.POST("/api-key-ip-blacklist", h.PostAPIKeyIPBlacklist)

	req := httptest.NewRequest(http.MethodPost, "/api-key-ip-blacklist", bytes.NewBufferString(`{"ip":"not-an-ip"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid ip, got %d", res.Code)
	}
}
