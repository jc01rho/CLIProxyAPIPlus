package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/redisqueue"
)

func TestGetUsageQueueDrainsValidJSONPayloads(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	prevQueueEnabled := redisqueue.Enabled()
	prevUsageEnabled := redisqueue.UsageStatisticsEnabled()
	redisqueue.SetEnabled(false)
	redisqueue.SetEnabled(true)
	redisqueue.SetUsageStatisticsEnabled(true)
	t.Cleanup(func() {
		redisqueue.SetEnabled(false)
		redisqueue.SetEnabled(prevQueueEnabled)
		redisqueue.SetUsageStatisticsEnabled(prevUsageEnabled)
	})

	redisqueue.Enqueue([]byte(`{"request_id":"req-1","model":"gpt-5.4"}`))
	redisqueue.Enqueue([]byte(`not-json`))
	redisqueue.Enqueue([]byte(`{"request_id":"req-2","model":"claude"}`))

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage-queue?count=10", nil)

	h.GetUsageQueue(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload []json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload) != 2 {
		t.Fatalf("payload items = %d, want 2", len(payload))
	}
	if remaining := redisqueue.PopOldest(10); len(remaining) != 0 {
		t.Fatalf("remaining queue items = %d, want 0", len(remaining))
	}
}

func TestGetUsageQueueMasksAPIKey(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	prevQueueEnabled := redisqueue.Enabled()
	prevUsageEnabled := redisqueue.UsageStatisticsEnabled()
	redisqueue.SetEnabled(false)
	redisqueue.SetEnabled(true)
	redisqueue.SetUsageStatisticsEnabled(true)
	t.Cleanup(func() {
		redisqueue.SetEnabled(false)
		redisqueue.SetEnabled(prevQueueEnabled)
		redisqueue.SetUsageStatisticsEnabled(prevUsageEnabled)
	})

	redisqueue.Enqueue([]byte(`{"request_id":"req-mask","api_key":"sk-secret-key-12345678","model":"gpt-5.4"}`))

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage-queue?count=10", nil)

	h.GetUsageQueue(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if strings.Contains(body, "sk-secret-key-12345678") {
		t.Fatalf("response must not contain raw API key; got %s", body)
	}
	if !strings.Contains(body, "***") {
		t.Fatalf("response must contain masked API key (***); got %s", body)
	}
}

func TestMaskAPIKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"short", "sh***"},
		{"1234567890abcdef", "1234***cdef"},
	}
	for _, c := range cases {
		got := maskUsageQueueAPIKey(c.in)
		if got != c.want {
			t.Errorf("maskUsageQueueAPIKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestGetUsageQueueRejectsInvalidCount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage-queue?count=bad", nil)

	h.GetUsageQueue(ginCtx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
