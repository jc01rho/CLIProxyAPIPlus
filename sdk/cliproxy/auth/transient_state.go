package auth

import (
	"time"
)

// TransientState holds runtime state that should NOT be persisted to Auth files.
// This includes block status, quota information, and per-model states that are
// managed in a separate cache file for server restart recovery.
type TransientState struct {
	// Unavailable flags transient provider unavailability (e.g. quota exceeded).
	Unavailable bool `json:"unavailable"`
	// NextRetryAfter is the earliest time a retry should retrigger.
	NextRetryAfter time.Time `json:"next_retry_after"`
	// ModelStates tracks per-model runtime availability data.
	ModelStates map[string]*ModelState `json:"model_states,omitempty"`
	// Quota captures recent quota information for load balancers.
	Quota QuotaState `json:"quota"`
	// LastError stores the last failure encountered while executing or refreshing.
	LastError *Error `json:"last_error,omitempty"`
}

// Clone creates a deep copy of the TransientState to avoid accidental mutation.
func (t *TransientState) Clone() *TransientState {
	if t == nil {
		return nil
	}

	copyState := *t

	// Deep copy ModelStates map
	if len(t.ModelStates) > 0 {
		copyState.ModelStates = make(map[string]*ModelState, len(t.ModelStates))
		for key, state := range t.ModelStates {
			copyState.ModelStates[key] = state.Clone()
		}
	}

	// Deep copy LastError if present
	if t.LastError != nil {
		copyError := *t.LastError
		copyState.LastError = &copyError
	}

	return &copyState
}
