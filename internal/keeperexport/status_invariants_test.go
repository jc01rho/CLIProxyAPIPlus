package keeperexport

import (
	"encoding/json"
	"testing"
)

func strPtr(s string) *string { return &s }
func i64Ptr(v int64) *int64   { return &v }

func baseStatus() StatusResponse {
	return StatusResponse{
		Enabled:         true,
		TokenConfigured: true,
		MetadataRevisions: map[string]int64{
			"auth_files":          0,
			"api_keys":            0,
			"provider_identities": 0,
		},
	}
}

func retryableErr() *StatusError {
	spec := protocolError("keeper_timeout")
	return &StatusError{Code: spec.Code, Message: spec.Message, Retryable: spec.Retryable, At: "2026-08-03T12:39:59.000Z"}
}

func nonRetryableErr() *StatusError {
	spec := protocolError("invalid_credential")
	return &StatusError{Code: spec.Code, Message: spec.Message, Retryable: spec.Retryable, At: "2026-08-03T12:39:59.000Z"}
}

func instanceRef() *InstanceRef {
	return &InstanceRef{InstanceID: "0198aa10-4d88-7a20-8f4e-8c8de4a9cb12", DisplayName: "test"}
}

func TestDecodeStatusResponseStateInvariantMatrix(t *testing.T) {
	stream := "0198aa11-1055-7f12-8a00-e843d1e17522"
	validWatermarks := func() *StatusResponse {
		s := baseStatus()
		s.StreamID = &stream
		s.NextSequence = i64Ptr(5)
		s.AcknowledgedThrough = i64Ptr(3)
		return &s
	}

	tests := []struct {
		name    string
		mutate  func(s *StatusResponse)
		wantErr bool
	}{
		// --- Disabled ---
		{"disabled valid", func(s *StatusResponse) {
			s.State = StateDisabled
			s.Enabled = false
			s.Instance = nil
			s.StreamID = nil
			s.NextSequence = nil
			s.AcknowledgedThrough = nil
			s.NextExpectedSequence = nil
			s.BacklogEvents = 0
			s.BacklogBytes = 0
			s.OldestBacklogAt = nil
			s.LastAttemptAt = nil
			s.LastSuccessAt = nil
			s.NextRetryAt = nil
			s.LastError = nil
		}, false},
		{"disabled with instance", func(s *StatusResponse) {
			s.State = StateDisabled
			s.Enabled = false
			s.Instance = instanceRef()
		}, true},
		{"disabled with streamId", func(s *StatusResponse) {
			s.State = StateDisabled
			s.Enabled = false
			s.StreamID = &stream
			s.NextSequence = i64Ptr(1)
			s.AcknowledgedThrough = i64Ptr(0)
		}, true},
		{"disabled enabled true", func(s *StatusResponse) {
			s.State = StateDisabled
			s.Enabled = true
		}, true},
		{"disabled with lastError", func(s *StatusResponse) {
			s.State = StateDisabled
			s.Enabled = false
			s.LastError = retryableErr()
		}, true},

		// --- Starting ---
		{"starting valid", func(s *StatusResponse) {
			s.State = StateStarting
			s.Instance = nil
			s.LastSuccessAt = nil
			s.LastError = nil
			s.NextRetryAt = nil
		}, false},
		{"starting with instance", func(s *StatusResponse) {
			s.State = StateStarting
			s.Instance = instanceRef()
		}, true},
		{"starting with lastSuccessAt", func(s *StatusResponse) {
			s.State = StateStarting
			s.LastSuccessAt = strPtr("2026-08-03T12:39:59.000Z")
		}, true},
		{"starting with retryable error", func(s *StatusResponse) {
			s.State = StateStarting
			s.LastError = retryableErr()
		}, true},
		{"starting with nonretryable error", func(s *StatusResponse) {
			s.State = StateStarting
			s.LastError = nonRetryableErr()
		}, true},
		{"starting with nextRetryAt", func(s *StatusResponse) {
			s.State = StateStarting
			s.NextRetryAt = strPtr("2026-08-03T12:40:05.000Z")
		}, true},

		// --- Connected ---
		{"connected valid", func(s *StatusResponse) {
			s.State = StateConnected
			s.Instance = instanceRef()
			s.LastSuccessAt = strPtr("2026-08-03T12:39:59.000Z")
			s.LastError = nil
			s.NextRetryAt = nil
			s.BacklogEvents = 0
			s.BacklogBytes = 0
			s.OldestBacklogAt = nil
			s.NextExpectedSequence = nil
		}, false},
		{"connected without instance", func(s *StatusResponse) {
			s.State = StateConnected
			s.Instance = nil
			s.LastSuccessAt = strPtr("2026-08-03T12:39:59.000Z")
		}, true},
		{"connected without lastSuccessAt", func(s *StatusResponse) {
			s.State = StateConnected
			s.Instance = instanceRef()
			s.LastSuccessAt = nil
		}, true},
		{"connected with lastError", func(s *StatusResponse) {
			s.State = StateConnected
			s.Instance = instanceRef()
			s.LastSuccessAt = strPtr("2026-08-03T12:39:59.000Z")
			s.LastError = retryableErr()
		}, true},
		{"connected with nextRetryAt", func(s *StatusResponse) {
			s.State = StateConnected
			s.Instance = instanceRef()
			s.LastSuccessAt = strPtr("2026-08-03T12:39:59.000Z")
			s.NextRetryAt = strPtr("2026-08-03T12:40:05.000Z")
		}, true},
		{"connected with pending backlog valid", func(s *StatusResponse) {
			s.State = StateConnected
			s.Instance = instanceRef()
			s.LastSuccessAt = strPtr("2026-08-03T12:39:59.000Z")
			s.BacklogEvents = 5
			s.BacklogBytes = 1024
			s.OldestBacklogAt = strPtr("2026-08-03T12:38:00.000Z")
		}, false},
		{"connected with backlog missing oldestBacklogAt", func(s *StatusResponse) {
			s.State = StateConnected
			s.Instance = instanceRef()
			s.LastSuccessAt = strPtr("2026-08-03T12:39:59.000Z")
			s.BacklogEvents = 5
			s.BacklogBytes = 1024
			s.OldestBacklogAt = nil
		}, true},

		// --- Retrying ---
		{"retrying valid", func(s *StatusResponse) {
			s.State = StateRetrying
			s.LastError = retryableErr()
			s.NextRetryAt = strPtr("2026-08-03T12:40:05.000Z")
		}, false},
		{"retrying without lastError", func(s *StatusResponse) {
			s.State = StateRetrying
			s.NextRetryAt = strPtr("2026-08-03T12:40:05.000Z")
		}, true},
		{"retrying with nonretryable error", func(s *StatusResponse) {
			s.State = StateRetrying
			s.LastError = nonRetryableErr()
			s.NextRetryAt = strPtr("2026-08-03T12:40:05.000Z")
		}, true},
		{"retrying without nextRetryAt", func(s *StatusResponse) {
			s.State = StateRetrying
			s.LastError = retryableErr()
		}, true},
		{"retrying without enabled", func(s *StatusResponse) {
			s.State = StateRetrying
			s.Enabled = false
			s.LastError = retryableErr()
			s.NextRetryAt = strPtr("2026-08-03T12:40:05.000Z")
		}, true},

		// --- Degraded (frozen invariant: non-nil instance + nonretryable lastError + no scheduled retry) ---
		{"degraded valid", func(s *StatusResponse) {
			s.State = StateDegraded
			s.Instance = instanceRef()
			s.LastSuccessAt = strPtr("2026-08-03T12:39:59.000Z")
			s.BacklogEvents = 3
			s.BacklogBytes = 2048
			s.OldestBacklogAt = strPtr("2026-08-03T12:38:00.000Z")
			s.NextRetryAt = nil
			s.LastError = nonRetryableErr()
		}, false},
		{"degraded without instance", func(s *StatusResponse) {
			s.State = StateDegraded
			s.Instance = nil
			s.LastSuccessAt = strPtr("2026-08-03T12:39:59.000Z")
			s.LastError = nonRetryableErr()
		}, true},
		{"degraded without lastSuccessAt", func(s *StatusResponse) {
			s.State = StateDegraded
			s.Instance = instanceRef()
			s.LastSuccessAt = nil
			s.LastError = nonRetryableErr()
		}, false},
		{"degraded with missing lastError", func(s *StatusResponse) {
			s.State = StateDegraded
			s.Instance = instanceRef()
			s.LastSuccessAt = strPtr("2026-08-03T12:39:59.000Z")
			s.LastError = nil
		}, true},
		{"degraded with retryable error", func(s *StatusResponse) {
			s.State = StateDegraded
			s.Instance = instanceRef()
			s.LastSuccessAt = strPtr("2026-08-03T12:39:59.000Z")
			s.LastError = retryableErr()
		}, true},
		{"degraded with nextRetryAt", func(s *StatusResponse) {
			s.State = StateDegraded
			s.Instance = instanceRef()
			s.LastSuccessAt = strPtr("2026-08-03T12:39:59.000Z")
			s.NextRetryAt = strPtr("2026-08-03T12:40:05.000Z")
			s.LastError = nonRetryableErr()
		}, true},
		{"degraded with backlog but no error", func(s *StatusResponse) {
			s.State = StateDegraded
			s.Instance = instanceRef()
			s.LastSuccessAt = strPtr("2026-08-03T12:39:59.000Z")
			s.BacklogEvents = 3
			s.BacklogBytes = 2048
			s.OldestBacklogAt = strPtr("2026-08-03T12:38:00.000Z")
			s.LastError = nil
		}, true},

		// --- Blocked ---
		{"blocked valid", func(s *StatusResponse) {
			s.State = StateBlocked
			s.Instance = nil
			s.StreamID = nil
			s.NextSequence = nil
			s.AcknowledgedThrough = nil
			s.NextExpectedSequence = nil
			s.BacklogEvents = 0
			s.BacklogBytes = 0
			s.OldestBacklogAt = nil
			s.LastSuccessAt = nil
			s.LastError = nonRetryableErr()
			s.NextRetryAt = nil
		}, false},
		{"blocked without instance valid", func(s *StatusResponse) {
			s.State = StateBlocked
			s.Instance = nil
			s.LastError = nonRetryableErr()
			s.NextRetryAt = nil
		}, false},
		{"blocked with instance", func(s *StatusResponse) {
			s.State = StateBlocked
			s.Instance = instanceRef()
			s.LastError = nonRetryableErr()
			s.NextRetryAt = nil
		}, false},
		{"blocked with retryable error", func(s *StatusResponse) {
			s.State = StateBlocked
			s.LastError = retryableErr()
		}, true},
		{"blocked without lastError", func(s *StatusResponse) {
			s.State = StateBlocked
		}, true},
		{"blocked with nextRetryAt", func(s *StatusResponse) {
			s.State = StateBlocked
			s.LastError = nonRetryableErr()
			s.NextRetryAt = strPtr("2026-08-03T12:40:05.000Z")
		}, true},
		{"blocked without enabled", func(s *StatusResponse) {
			s.State = StateBlocked
			s.Enabled = false
			s.LastError = nonRetryableErr()
		}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := validWatermarks()
			tc.mutate(s)
			encoded, err := MarshalStatusResponse(*s)
			if err != nil {
				t.Fatal(err)
			}
			_, perr := DecodeStatusResponse(encoded)
			if tc.wantErr && perr == nil {
				t.Fatalf("expected rejection for %s: %s", tc.name, string(encoded))
			}
			if !tc.wantErr && perr != nil {
				t.Fatalf("unexpected rejection for %s: %v encoded=%s", tc.name, perr, string(encoded))
			}
		})
	}
}

// Ensure JSON round-trip preserves all fields
func TestStatusResponseJSONRoundTrip(t *testing.T) {
	original := baseStatus()
	original.State = StateConnected
	original.Instance = instanceRef()
	original.LastSuccessAt = strPtr("2026-08-03T12:39:59.000Z")
	encoded, err := MarshalStatusResponse(original)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"protocolVersion", "state", "enabled", "tokenConfigured", "instance", "streamId", "nextSequence", "acknowledgedThrough", "nextExpectedSequence", "backlogEvents", "backlogBytes", "oldestBacklogAt", "lastAttemptAt", "lastSuccessAt", "nextRetryAt", "metadataRevisions", "lastError"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("missing key %s in encoded status", key)
		}
	}
}
