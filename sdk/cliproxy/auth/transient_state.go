package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
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

// Load reads the cache from the file. If the file doesn't exist or is corrupted,
// it starts with an empty cache and logs a warning.
func (c *TransientStateCache) Load() error {
	if c == nil || c.filePath == "" {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := os.ReadFile(c.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Debugf("transient cache file not found: %s, starting with empty cache", c.filePath)
			return nil
		}
		log.Warnf("failed to read transient cache file: %v, starting with empty cache", err)
		return nil
	}

	if len(data) == 0 {
		log.Debugf("transient cache file is empty: %s", c.filePath)
		return nil
	}

	var loaded map[string]*TransientState
	if err := json.Unmarshal(data, &loaded); err != nil {
		log.Warnf("failed to parse transient cache file: %v, starting with empty cache", err)
		c.states = make(map[string]*TransientState)
		return nil
	}

	if loaded == nil {
		loaded = make(map[string]*TransientState)
	}
	c.states = loaded

	log.Infof("loaded %d transient state(s) from cache", len(c.states))
	return nil
}

// Save persists the cache to the file using atomic write (temp file + rename).
// Returns error if write fails, but the caller may choose to ignore it.
func (c *TransientStateCache) Save() error {
	if c == nil || c.filePath == "" {
		return nil
	}

	c.mu.RLock()
	statesToSave := make(map[string]*TransientState, len(c.states))
	for k, v := range c.states {
		statesToSave[k] = v.Clone()
	}
	c.mu.RUnlock()

	data, err := json.MarshalIndent(statesToSave, "", "  ")
	if err != nil {
		log.Warnf("failed to marshal transient cache: %v", err)
		return err
	}

	// Ensure parent directory exists
	dir := filepath.Dir(c.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Warnf("failed to create cache directory: %v", err)
		return err
	}

	// Atomic write: write to temp file, then rename
	tempFile := c.filePath + ".tmp"
	if err := os.WriteFile(tempFile, data, 0644); err != nil {
		log.Warnf("failed to write transient cache temp file: %v", err)
		return err
	}

	if err := os.Rename(tempFile, c.filePath); err != nil {
		log.Warnf("failed to rename transient cache temp file: %v", err)
		// Clean up temp file
		_ = os.Remove(tempFile)
		return err
	}

	log.Debugf("saved %d transient state(s) to cache", len(statesToSave))
	return nil
}

// SaveAsync saves the cache in a background goroutine.
// Errors are logged but not returned.
func (c *TransientStateCache) SaveAsync() {
	if c == nil {
		return
	}
	go func() {
		if err := c.Save(); err != nil {
			log.Warnf("async save of transient cache failed: %v", err)
		}
	}()
}

// FilePath returns the configured file path for the cache.
func (c *TransientStateCache) FilePath() string {
	if c == nil {
		return ""
	}
	return c.filePath
}
