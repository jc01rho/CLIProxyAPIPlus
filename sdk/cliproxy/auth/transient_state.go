package auth

import (
	"sync"
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

// TransientStateCache manages in-memory transient states with thread-safe operations.
// States are keyed by auth ID and can be persisted to a file for restart recovery.
type TransientStateCache struct {
	mu       sync.RWMutex
	states   map[string]*TransientState
	filePath string
}

// NewTransientStateCache creates a new cache with the specified file path for persistence.
func NewTransientStateCache(filePath string) *TransientStateCache {
	return &TransientStateCache{
		states:   make(map[string]*TransientState),
		filePath: filePath,
	}
}

// Get retrieves the transient state for an auth ID. Returns nil if not found.
func (c *TransientStateCache) Get(authID string) *TransientState {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()

	if state, ok := c.states[authID]; ok {
		return state.Clone()
	}
	return nil
}

// Set stores the transient state for an auth ID.
func (c *TransientStateCache) Set(authID string, state *TransientState) {
	if c == nil || authID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if state == nil {
		delete(c.states, authID)
		return
	}
	c.states[authID] = state.Clone()
}

// Delete removes the transient state for an auth ID.
func (c *TransientStateCache) Delete(authID string) {
	if c == nil || authID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.states, authID)
}

// GetOrCreate retrieves the transient state for an auth ID, creating a new one if not found.
func (c *TransientStateCache) GetOrCreate(authID string) *TransientState {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if state, ok := c.states[authID]; ok {
		return state.Clone()
	}

	// Create new state
	newState := &TransientState{}
	c.states[authID] = newState
	return newState.Clone()
}

// Len returns the number of cached states.
func (c *TransientStateCache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.states)
}
