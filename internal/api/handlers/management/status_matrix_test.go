package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/keeperexport"
)

func strPtr(s string) *string { return &s }
func i64Ptr(v int64) *int64   { return &v }
func instPtr() *keeperexport.InstanceRef {
	return &keeperexport.InstanceRef{InstanceID: "0198aa10-4d88-7a20-8f4e-8c8de4a9cb12", DisplayName: "test"}
}

func retryableErr() *keeperexport.StatusError {
	spec := keeperexport.StableError("keeper_timeout")
	return &keeperexport.StatusError{Code: spec.Code, Message: spec.Message, Retryable: spec.Retryable, At: "2026-08-03T12:39:59.000Z"}
}

func nonRetryableErr() *keeperexport.StatusError {
	spec := keeperexport.StableError("invalid_credential")
	return &keeperexport.StatusError{Code: spec.Code, Message: spec.Message, Retryable: spec.Retryable, At: "2026-08-03T12:39:59.000Z"}
}

func baseMetadata() map[string]int64 {
	return map[string]int64{"auth_files": 0, "api_keys": 0, "provider_identities": 0}
}

func baseStatus(stream string) keeperexport.StatusResponse {
	next, ack := int64(5), int64(3)
	return keeperexport.StatusResponse{
		Enabled:             true,
		TokenConfigured:     true,
		StreamID:            &stream,
		NextSequence:        &next,
		AcknowledgedThrough: &ack,
		MetadataRevisions:   baseMetadata(),
	}
}

func runStatus(t *testing.T, h *Handler, status keeperexport.StatusResponse) (int, string) {
	t.Helper()
	h.SetUsageExportRuntime(&fakeUsageExportRuntime{response: status})
	r := setupTestRouter(h)
	r.GET("/status", h.GetUsageExportStatus)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/status", nil))
	return rr.Code, rr.Body.String()
}

func assertInternalError(t *testing.T, status keeperexport.StatusResponse, label string) {
	t.Helper()
	h := NewHandler(&config.Config{}, "", nil)
	code, body := runStatus(t, h, status)
	if code != http.StatusInternalServerError || !strings.Contains(body, `"code":"internal_error"`) {
		t.Fatalf("%s expected 500 internal_error; code=%d body=%s", label, code, body)
	}
}

func TestUsageExportStatusHandlerRejectsInvalidStateMatrix(t *testing.T) {
	stream := "0198aa11-1055-7f12-8a00-e843d1e17522"
	cases := []struct {
		label  string
		status keeperexport.StatusResponse
	}{
		{
			"disabled with instance",
			func() keeperexport.StatusResponse {
				s := baseStatus(stream)
				s.State, s.Enabled, s.Instance = keeperexport.StateDisabled, false, instPtr()
				s.StreamID, s.NextSequence, s.AcknowledgedThrough = nil, nil, nil
				return s
			}(),
		},
		{
			"disabled with lastError",
			func() keeperexport.StatusResponse {
				s := baseStatus(stream)
				s.State, s.Enabled, s.LastError = keeperexport.StateDisabled, false, nonRetryableErr()
				s.StreamID, s.NextSequence, s.AcknowledgedThrough = nil, nil, nil
				return s
			}(),
		},
		{
			"starting with instance",
			func() keeperexport.StatusResponse {
				s := baseStatus(stream)
				s.State, s.Instance = keeperexport.StateStarting, instPtr()
				return s
			}(),
		},
		{
			"starting with lastSuccessAt",
			func() keeperexport.StatusResponse {
				s := baseStatus(stream)
				s.State, s.LastSuccessAt = keeperexport.StateStarting, strPtr("2026-08-03T12:39:59.000Z")
				return s
			}(),
		},
		{
			"starting with retryable error",
			func() keeperexport.StatusResponse {
				s := baseStatus(stream)
				s.State, s.LastError = keeperexport.StateStarting, retryableErr()
				return s
			}(),
		},
		{
			"connected without instance",
			func() keeperexport.StatusResponse {
				s := baseStatus(stream)
				s.State, s.Instance = keeperexport.StateConnected, nil
				s.LastSuccessAt = strPtr("2026-08-03T12:39:59.000Z")
				return s
			}(),
		},
		{
			"connected with lastError",
			func() keeperexport.StatusResponse {
				s := baseStatus(stream)
				s.State, s.Instance, s.LastSuccessAt = keeperexport.StateConnected, instPtr(), strPtr("2026-08-03T12:39:59.000Z")
				s.LastError = retryableErr()
				return s
			}(),
		},
		{
			"connected with nextRetryAt",
			func() keeperexport.StatusResponse {
				s := baseStatus(stream)
				s.State, s.Instance, s.LastSuccessAt = keeperexport.StateConnected, instPtr(), strPtr("2026-08-03T12:39:59.000Z")
				s.NextRetryAt = strPtr("2026-08-03T12:40:05.000Z")
				return s
			}(),
		},
		{
			"connected with backlog missing oldestBacklogAt",
			func() keeperexport.StatusResponse {
				s := baseStatus(stream)
				s.State, s.Instance, s.LastSuccessAt = keeperexport.StateConnected, instPtr(), strPtr("2026-08-03T12:39:59.000Z")
				s.BacklogEvents, s.BacklogBytes = 3, 2048
				s.OldestBacklogAt = nil
				return s
			}(),
		},
		{
			"retrying without lastError",
			func() keeperexport.StatusResponse {
				s := baseStatus(stream)
				s.State, s.LastError = keeperexport.StateRetrying, nil
				s.NextRetryAt = strPtr("2026-08-03T12:40:05.000Z")
				return s
			}(),
		},
		{
			"retrying with nonretryable error",
			func() keeperexport.StatusResponse {
				s := baseStatus(stream)
				s.State, s.LastError = keeperexport.StateRetrying, nonRetryableErr()
				s.NextRetryAt = strPtr("2026-08-03T12:40:05.000Z")
				return s
			}(),
		},
		{
			"retrying without nextRetryAt",
			func() keeperexport.StatusResponse {
				s := baseStatus(stream)
				s.State, s.LastError = keeperexport.StateRetrying, retryableErr()
				return s
			}(),
		},
		{
			"degraded without instance",
			func() keeperexport.StatusResponse {
				s := baseStatus(stream)
				s.State, s.Instance = keeperexport.StateDegraded, nil
				s.LastSuccessAt = strPtr("2026-08-03T12:39:59.000Z")
				s.LastError = nonRetryableErr()
				return s
			}(),
		},
		{
			"degraded with missing lastError",
			func() keeperexport.StatusResponse {
				s := baseStatus(stream)
				s.State, s.Instance, s.LastSuccessAt = keeperexport.StateDegraded, instPtr(), strPtr("2026-08-03T12:39:59.000Z")
				s.BacklogEvents, s.BacklogBytes, s.OldestBacklogAt = 3, 2048, strPtr("2026-08-03T12:38:00.000Z")
				s.LastError = nil
				return s
			}(),
		},
		{
			"degraded with retryable error",
			func() keeperexport.StatusResponse {
				s := baseStatus(stream)
				s.State, s.Instance, s.LastSuccessAt = keeperexport.StateDegraded, instPtr(), strPtr("2026-08-03T12:39:59.000Z")
				s.LastError = retryableErr()
				return s
			}(),
		},
		{
			"degraded with nextRetryAt",
			func() keeperexport.StatusResponse {
				s := baseStatus(stream)
				s.State, s.Instance, s.LastSuccessAt = keeperexport.StateDegraded, instPtr(), strPtr("2026-08-03T12:39:59.000Z")
				s.LastError = nonRetryableErr()
				s.NextRetryAt = strPtr("2026-08-03T12:40:05.000Z")
				return s
			}(),
		},
		{
			"degraded with backlog but no error",
			func() keeperexport.StatusResponse {
				s := baseStatus(stream)
				s.State, s.Instance, s.LastSuccessAt = keeperexport.StateDegraded, instPtr(), strPtr("2026-08-03T12:39:59.000Z")
				s.BacklogEvents, s.BacklogBytes, s.OldestBacklogAt = 3, 2048, strPtr("2026-08-03T12:38:00.000Z")
				return s
			}(),
		},
		{
			"blocked with retryable error",
			func() keeperexport.StatusResponse {
				s := baseStatus(stream)
				s.State, s.LastError = keeperexport.StateBlocked, retryableErr()
				s.StreamID, s.NextSequence, s.AcknowledgedThrough = nil, nil, nil
				return s
			}(),
		},
		{
			"blocked without lastError",
			func() keeperexport.StatusResponse {
				s := baseStatus(stream)
				s.State, s.LastError = keeperexport.StateBlocked, nil
				s.StreamID, s.NextSequence, s.AcknowledgedThrough = nil, nil, nil
				return s
			}(),
		},
		{
			"blocked with nextRetryAt",
			func() keeperexport.StatusResponse {
				s := baseStatus(stream)
				s.State, s.LastError, s.NextRetryAt = keeperexport.StateBlocked, nonRetryableErr(), strPtr("2026-08-03T12:40:05.000Z")
				s.StreamID, s.NextSequence, s.AcknowledgedThrough = nil, nil, nil
				return s
			}(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			assertInternalError(t, tc.status, tc.label)
		})
	}
}

func TestUsageExportStatusHandlerAllowsValidShapeMatrix(t *testing.T) {
	stream := "0198aa11-1055-7f12-8a00-e843d1e17522"
	cases := []struct {
		label  string
		status keeperexport.StatusResponse
	}{
		{
			"disabled valid",
			func() keeperexport.StatusResponse {
				s := baseStatus(stream)
				s.State, s.Enabled = keeperexport.StateDisabled, false
				s.StreamID, s.NextSequence, s.AcknowledgedThrough = nil, nil, nil
				s.NextExpectedSequence = nil
				s.LastAttemptAt = nil
				return s
			}(),
		},
		{
			"starting valid",
			func() keeperexport.StatusResponse {
				s := baseStatus(stream)
				s.State = keeperexport.StateStarting
				return s
			}(),
		},
		{
			"connected valid",
			func() keeperexport.StatusResponse {
				s := baseStatus(stream)
				s.State, s.Instance, s.LastSuccessAt = keeperexport.StateConnected, instPtr(), strPtr("2026-08-03T12:39:59.000Z")
				s.NextExpectedSequence = nil
				return s
			}(),
		},
		{
			"connected valid with pending backlog",
			func() keeperexport.StatusResponse {
				s := baseStatus(stream)
				s.State, s.Instance, s.LastSuccessAt = keeperexport.StateConnected, instPtr(), strPtr("2026-08-03T12:39:59.000Z")
				s.NextExpectedSequence = nil
				s.BacklogEvents, s.BacklogBytes = 3, 2048
				s.OldestBacklogAt = strPtr("2026-08-03T12:38:00.000Z")
				return s
			}(),
		},
		{
			"retrying valid",
			func() keeperexport.StatusResponse {
				s := baseStatus(stream)
				s.State, s.LastError, s.NextRetryAt = keeperexport.StateRetrying, retryableErr(), strPtr("2026-08-03T12:40:05.000Z")
				return s
			}(),
		},
		{
			"degraded valid",
			func() keeperexport.StatusResponse {
				s := baseStatus(stream)
				s.State, s.Instance, s.LastSuccessAt = keeperexport.StateDegraded, instPtr(), strPtr("2026-08-03T12:39:59.000Z")
				s.LastError = nonRetryableErr()
				return s
			}(),
		},
		{
			"blocked valid",
			func() keeperexport.StatusResponse {
				s := baseStatus(stream)
				s.State, s.LastError = keeperexport.StateBlocked, nonRetryableErr()
				s.StreamID, s.NextSequence, s.AcknowledgedThrough = nil, nil, nil
				return s
			}(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			h := NewHandler(&config.Config{}, "", nil)
			code, body := runStatus(t, h, tc.status)
			if code != http.StatusOK {
				t.Fatalf("%s expected 200, got %d body=%s", tc.label, code, body)
			}
		})
	}
}
