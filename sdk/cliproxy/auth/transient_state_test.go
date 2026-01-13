package auth

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestTransientState_Clone(t *testing.T) {
	t.Parallel()

	original := &TransientState{
		Unavailable:    true,
		NextRetryAfter: time.Now().Add(time.Hour),
		Quota: QuotaState{
			Exceeded: true,
			Reason:   "test quota",
		},
		ModelStates: map[string]*ModelState{
			"model-1": {
				Unavailable: true,
				Status:      StatusError,
			},
		},
		LastError: &Error{
			Code:    "test_error",
			Message: "test message",
		},
	}

	cloned := original.Clone()

	// Verify not nil
	if cloned == nil {
		t.Fatal("Clone() returned nil")
	}

	// Verify different pointers
	if cloned == original {
		t.Error("Clone() returned same pointer")
	}

	// Verify values copied
	if cloned.Unavailable != original.Unavailable {
		t.Error("Unavailable not copied")
	}

	if !cloned.NextRetryAfter.Equal(original.NextRetryAfter) {
		t.Error("NextRetryAfter not copied")
	}

	if cloned.Quota.Exceeded != original.Quota.Exceeded {
		t.Error("Quota.Exceeded not copied")
	}

	if cloned.Quota.Reason != original.Quota.Reason {
		t.Error("Quota.Reason not copied")
	}

	// Verify map is deep copied
	if cloned.ModelStates == nil {
		t.Fatal("ModelStates is nil")
	}

	if len(cloned.ModelStates) != len(original.ModelStates) {
		t.Errorf("ModelStates length mismatch: got %d, want %d", len(cloned.ModelStates), len(original.ModelStates))
	}

	// Modify original and verify clone is not affected
	original.ModelStates["model-1"].Unavailable = false
	if cloned.ModelStates["model-1"].Unavailable == false {
		t.Error("ModelStates not deep copied - modification affected clone")
	}

	// Verify LastError is copied
	if cloned.LastError == nil {
		t.Fatal("LastError is nil")
	}

	if cloned.LastError.Code != original.LastError.Code {
		t.Error("LastError.Code not copied")
	}
}

func TestTransientState_Clone_Nil(t *testing.T) {
	t.Parallel()

	var ts *TransientState
	cloned := ts.Clone()

	if cloned != nil {
		t.Error("Clone() of nil should return nil")
	}
}

func TestTransientState_Clone_EmptyModelStates(t *testing.T) {
	t.Parallel()

	original := &TransientState{
		Unavailable: true,
	}

	cloned := original.Clone()

	if cloned == nil {
		t.Fatal("Clone() returned nil")
	}

	if cloned.ModelStates != nil && len(cloned.ModelStates) > 0 {
		t.Error("Empty ModelStates should remain empty/nil")
	}
}

func TestTransientState_Clone_NilLastError(t *testing.T) {
	t.Parallel()

	original := &TransientState{
		Unavailable: true,
		LastError:   nil,
	}

	cloned := original.Clone()

	if cloned == nil {
		t.Fatal("Clone() returned nil")
	}

	if cloned.LastError != nil {
		t.Error("Nil LastError should remain nil")
	}
}

// ============================================
// TransientStateCache Tests
// ============================================

func TestTransientStateCache_GetSet(t *testing.T) {
	t.Parallel()

	cache := NewTransientStateCache("/tmp/test_cache.json")

	// Test Get on empty cache
	result := cache.Get("auth-1")
	if result != nil {
		t.Error("Get on empty cache should return nil")
	}

	// Test Set and Get
	state := &TransientState{
		Unavailable: true,
		Quota: QuotaState{
			Exceeded: true,
			Reason:   "test",
		},
	}

	cache.Set("auth-1", state)

	result = cache.Get("auth-1")
	if result == nil {
		t.Fatal("Get after Set should return state")
	}

	if !result.Unavailable {
		t.Error("Unavailable should be true")
	}

	if !result.Quota.Exceeded {
		t.Error("Quota.Exceeded should be true")
	}

	// Verify deep copy - modifying original shouldn't affect cached
	state.Unavailable = false
	result2 := cache.Get("auth-1")
	if !result2.Unavailable {
		t.Error("Cached state should not be affected by original modification")
	}

	// Verify deep copy - modifying returned shouldn't affect cached
	result.Quota.Reason = "modified"
	result3 := cache.Get("auth-1")
	if result3.Quota.Reason != "test" {
		t.Error("Cached state should not be affected by returned value modification")
	}
}

func TestTransientStateCache_Delete(t *testing.T) {
	t.Parallel()

	cache := NewTransientStateCache("/tmp/test_cache.json")

	// Set a state
	cache.Set("auth-1", &TransientState{Unavailable: true})

	// Verify it exists
	if cache.Get("auth-1") == nil {
		t.Fatal("State should exist after Set")
	}

	// Delete it
	cache.Delete("auth-1")

	// Verify it's gone
	if cache.Get("auth-1") != nil {
		t.Error("State should be nil after Delete")
	}

	// Delete non-existent should not panic
	cache.Delete("non-existent")
}

func TestTransientStateCache_SetNil(t *testing.T) {
	t.Parallel()

	cache := NewTransientStateCache("/tmp/test_cache.json")

	// Set a state
	cache.Set("auth-1", &TransientState{Unavailable: true})

	// Set nil should delete
	cache.Set("auth-1", nil)

	if cache.Get("auth-1") != nil {
		t.Error("Setting nil should delete the entry")
	}
}

func TestTransientStateCache_Len(t *testing.T) {
	t.Parallel()

	cache := NewTransientStateCache("/tmp/test_cache.json")

	if cache.Len() != 0 {
		t.Error("Empty cache should have length 0")
	}

	cache.Set("auth-1", &TransientState{})
	cache.Set("auth-2", &TransientState{})

	if cache.Len() != 2 {
		t.Errorf("Cache should have length 2, got %d", cache.Len())
	}

	cache.Delete("auth-1")

	if cache.Len() != 1 {
		t.Errorf("Cache should have length 1 after delete, got %d", cache.Len())
	}
}

func TestTransientStateCache_GetOrCreate(t *testing.T) {
	t.Parallel()

	cache := NewTransientStateCache("/tmp/test_cache.json")

	// GetOrCreate on non-existent should create
	state := cache.GetOrCreate("auth-1")
	if state == nil {
		t.Fatal("GetOrCreate should return non-nil state")
	}

	// Should be a new empty state
	if state.Unavailable {
		t.Error("New state should have Unavailable=false")
	}

	// Should now exist in cache
	if cache.Len() != 1 {
		t.Error("Cache should have 1 entry after GetOrCreate")
	}

	// GetOrCreate again should return existing
	cache.Set("auth-1", &TransientState{Unavailable: true})
	state2 := cache.GetOrCreate("auth-1")
	if !state2.Unavailable {
		t.Error("GetOrCreate should return existing state")
	}
}

func TestTransientStateCache_Concurrent(t *testing.T) {
	t.Parallel()

	cache := NewTransientStateCache("/tmp/test_cache.json")

	var wg sync.WaitGroup
	numGoroutines := 100

	// Concurrent writes
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			authID := "auth-" + string(rune('0'+id%10))
			cache.Set(authID, &TransientState{
				Unavailable: true,
				Quota: QuotaState{
					BackoffLevel: id,
				},
			})
		}(i)
	}

	// Concurrent reads
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			authID := "auth-" + string(rune('0'+id%10))
			_ = cache.Get(authID)
		}(i)
	}

	// Concurrent deletes
	for i := 0; i < numGoroutines/2; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			authID := "auth-" + string(rune('0'+id%10))
			cache.Delete(authID)
		}(i)
	}

	wg.Wait()

	// Should not panic and cache should be in consistent state
	_ = cache.Len()
}

func TestTransientStateCache_NilCache(t *testing.T) {
	t.Parallel()

	var cache *TransientStateCache

	// All operations on nil cache should not panic
	if cache.Get("auth-1") != nil {
		t.Error("Get on nil cache should return nil")
	}

	cache.Set("auth-1", &TransientState{}) // Should not panic
	cache.Delete("auth-1")                 // Should not panic

	if cache.Len() != 0 {
		t.Error("Len on nil cache should return 0")
	}

	if cache.GetOrCreate("auth-1") != nil {
		t.Error("GetOrCreate on nil cache should return nil")
	}
}

// ============================================
// TransientStateCache Load/Save Tests
// ============================================

func TestTransientStateCache_LoadSave(t *testing.T) {
	t.Parallel()

	// Create temp file
	tmpFile, err := os.CreateTemp("", "transient_cache_*.json")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	// Create cache and add data
	cache := NewTransientStateCache(tmpPath)
	cache.Set("auth-1", &TransientState{
		Unavailable: true,
		Quota: QuotaState{
			Exceeded: true,
			Reason:   "test quota",
		},
	})
	cache.Set("auth-2", &TransientState{
		NextRetryAfter: time.Now().Add(time.Hour),
	})

	// Save
	if err := cache.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Create new cache and load
	cache2 := NewTransientStateCache(tmpPath)
	if err := cache2.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Verify data
	if cache2.Len() != 2 {
		t.Errorf("Expected 2 entries, got %d", cache2.Len())
	}

	state1 := cache2.Get("auth-1")
	if state1 == nil {
		t.Fatal("auth-1 not found after load")
	}
	if !state1.Unavailable {
		t.Error("auth-1 Unavailable should be true")
	}
	if !state1.Quota.Exceeded {
		t.Error("auth-1 Quota.Exceeded should be true")
	}

	state2 := cache2.Get("auth-2")
	if state2 == nil {
		t.Fatal("auth-2 not found after load")
	}
}

func TestTransientStateCache_LoadMissingFile(t *testing.T) {
	t.Parallel()

	cache := NewTransientStateCache("/nonexistent/path/cache.json")

	// Load should not error, just start empty
	err := cache.Load()
	if err != nil {
		t.Errorf("Load of missing file should not error, got: %v", err)
	}

	if cache.Len() != 0 {
		t.Error("Cache should be empty after loading missing file")
	}
}

func TestTransientStateCache_LoadCorruptedFile(t *testing.T) {
	t.Parallel()

	// Create temp file with corrupted JSON
	tmpFile, err := os.CreateTemp("", "corrupted_cache_*.json")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.WriteString("{invalid json content")
	tmpFile.Close()
	defer os.Remove(tmpPath)

	cache := NewTransientStateCache(tmpPath)

	// Load should not error, just start empty
	err = cache.Load()
	if err != nil {
		t.Errorf("Load of corrupted file should not error, got: %v", err)
	}

	if cache.Len() != 0 {
		t.Error("Cache should be empty after loading corrupted file")
	}
}

func TestTransientStateCache_LoadEmptyFile(t *testing.T) {
	t.Parallel()

	// Create empty temp file
	tmpFile, err := os.CreateTemp("", "empty_cache_*.json")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	cache := NewTransientStateCache(tmpPath)

	// Load should not error
	err = cache.Load()
	if err != nil {
		t.Errorf("Load of empty file should not error, got: %v", err)
	}

	if cache.Len() != 0 {
		t.Error("Cache should be empty after loading empty file")
	}
}

func TestTransientStateCache_SaveCreatesDirectory(t *testing.T) {
	t.Parallel()

	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "cache_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Use a nested path that doesn't exist
	nestedPath := filepath.Join(tmpDir, "nested", "dir", "cache.json")

	cache := NewTransientStateCache(nestedPath)
	cache.Set("auth-1", &TransientState{Unavailable: true})

	// Save should create the directory
	if err := cache.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(nestedPath); os.IsNotExist(err) {
		t.Error("Cache file was not created")
	}
}

func TestTransientStateCache_FilePath(t *testing.T) {
	t.Parallel()

	cache := NewTransientStateCache("/some/path/cache.json")
	if cache.FilePath() != "/some/path/cache.json" {
		t.Errorf("FilePath() = %q, want %q", cache.FilePath(), "/some/path/cache.json")
	}

	var nilCache *TransientStateCache
	if nilCache.FilePath() != "" {
		t.Error("FilePath() on nil cache should return empty string")
	}
}

func TestTransientStateCache_NilOperations(t *testing.T) {
	t.Parallel()

	var cache *TransientStateCache

	// All operations on nil cache should not panic
	if err := cache.Load(); err != nil {
		t.Errorf("Load on nil cache should not error, got: %v", err)
	}

	if err := cache.Save(); err != nil {
		t.Errorf("Save on nil cache should not error, got: %v", err)
	}

	cache.SaveAsync() // Should not panic
}
