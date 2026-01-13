package auth

import (
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
