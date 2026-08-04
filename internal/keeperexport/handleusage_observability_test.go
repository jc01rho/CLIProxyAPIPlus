package keeperexport

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestHandleUsageProjectionFailureIsObservable is the failing-first gate for
// the silent-drop observation bug. When ProjectUsage returns an error (e.g.,
// missing request ID), HandleUsage must surface the failure through the
// sanitized runtime status (LastError) instead of dropping the event silently.
// The status shape must also be valid under the frozen strict decoder so the
// management handler returns HTTP 200, not 500.
func TestHandleUsageProjectionFailureIsObservable(t *testing.T) {
	runtime, path := newBoundProbeRuntime(t)

	// Missing RequestID forces ProjectUsage to fail (see project.go:
	// "keeper usage projection requires request ID").
	record := probeRecord()
	ctx := contextWithRequestID(t, "")
	runtime.HandleUsage(ctx, record)

	status, err := runtime.ManagementStatus(context.Background())
	if err != nil {
		t.Fatalf("ManagementStatus() error = %v", err)
	}
	if status.LastError == nil {
		t.Fatalf("expected LastError to be set after projection failure; status = %+v", status)
	}
	if status.LastError.Code == "" {
		t.Fatalf("LastError.Code must be a non-empty stable code; got %+v", status.LastError)
	}
	if !IsStableCode(status.LastError.Code) {
		t.Fatalf("LastError.Code %q is not part of the frozen section-9 error set", status.LastError.Code)
	}
	// Local usage-side failures have no scheduled retry, so the frozen state
	// precedence must derive a valid blocked/degraded state (non-retryable,
	// no nextRetryAt) that passes the strict status decoder.
	if status.LastError.Retryable {
		t.Fatalf("local projection failure must be non-retryable (no scheduled retry); got %+v", status.LastError)
	}
	if perr := ValidateStatusResponse(status); perr != nil {
		t.Fatalf("projection-failure status rejected by strict decoder: %v\nstatus=%+v", perr, status)
	}
	if status.State != StateBlocked && status.State != StateDegraded {
		t.Fatalf("projection-failure state = %q, want blocked or degraded", status.State)
	}
	// LastError must never leak raw secret material into the JSON serializer.
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("json.Marshal(status) error = %v", err)
	}
	for _, secret := range []string{"sk-task10-shared-provider-key", "sk-task10-shared-client-key", "Authorization", "Bearer"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("encoded status leaks %q: %s", secret, encoded)
		}
	}
	// The failed projection must not have appended a payload to the durable
	// outbox.
	outbox, err := OpenOutbox(context.Background(), path, 16<<20)
	if err != nil {
		t.Fatalf("OpenOutbox() error = %v", err)
	}
	defer outbox.Close()
	obStatus, err := outbox.Status(context.Background())
	if err != nil {
		t.Fatalf("outbox.Status() error = %v", err)
	}
	if obStatus.BacklogEvents != 0 {
		t.Fatalf("BacklogEvents = %d, want 0 (failed projection must not enqueue)", obStatus.BacklogEvents)
	}
}

// TestHandleUsageAppendFailureIsObservable is the failing-first gate for
// silent drops on outbox commit failure. When Outbox.Append fails (e.g.,
// outbox full, sequence exhausted, storage error), the runtime must surface
// the failure through the sanitized runtime status with a shape that is valid
// under the frozen strict decoder so the management handler returns HTTP 200,
// not 500.
func TestHandleUsageAppendFailureIsObservable(t *testing.T) {
	var runtime Runtime
	dir := t.TempDir()
	path := filepath.Join(dir, "outbox.db")
	cfg := configForAppendFailureProbe(t, dir, path)

	if err := runtime.Apply(context.Background(), cfg); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	outbox, err := OpenOutbox(context.Background(), path, 16<<20)
	if err != nil {
		t.Fatalf("OpenOutbox() error = %v", err)
	}
	if _, err := outbox.BindInstance(context.Background(), "0198aa10-4d88-7a20-8f4e-8c8de4a9cb11"); err != nil {
		t.Fatalf("BindInstance() error = %v", err)
	}
	_ = outbox.Close()

	// Inject a deterministic append-time failure via the test hook seam.
	// The raw cause contains secret markers to prove the sanitized status never
	// leaks the inner error string.
	released := make(chan struct{})
	failures := 0
	runtime.mu.RLock()
	runtime.outbox.testHooks.beforeAppendCommit = func(context.Context) error {
		failures++
		close(released)
		return fmt.Errorf("%w: raw cause Authorization Bearer marker", context.DeadlineExceeded)
	}
	runtime.mu.RUnlock()

	ctx := contextWithRequestID(t, "obs-append-fail")
	runtime.HandleUsage(ctx, probeRecord())

	<-released

	status, err := runtime.ManagementStatus(context.Background())
	if err != nil {
		t.Fatalf("ManagementStatus() error = %v", err)
	}
	if status.LastError == nil {
		t.Fatalf("expected LastError to be set after append failure; status = %+v", status)
	}
	if status.LastError.Code == "" {
		t.Fatalf("LastError.Code must be a non-empty stable code; got %+v", status.LastError)
	}
	if !IsStableCode(status.LastError.Code) {
		t.Fatalf("LastError.Code %q is not part of the frozen section-9 error set", status.LastError.Code)
	}
	// Local append failures have no scheduled retry, so the frozen state
	// precedence must derive a valid blocked/degraded state (non-retryable,
	// no nextRetryAt) that passes the strict status decoder.
	if status.LastError.Retryable {
		t.Fatalf("local append failure must be non-retryable (no scheduled retry); got %+v", status.LastError)
	}
	if status.NextRetryAt != nil {
		t.Fatalf("local append failure must not schedule a retry; got NextRetryAt=%v", status.NextRetryAt)
	}
	if perr := ValidateStatusResponse(status); perr != nil {
		t.Fatalf("append-failure status rejected by strict decoder: %v\nstatus=%+v", perr, status)
	}
	if status.State != StateBlocked && status.State != StateDegraded {
		t.Fatalf("append-failure state = %q, want blocked or degraded", status.State)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("json.Marshal(status) error = %v", err)
	}
	for _, secret := range []string{"sk-task10-shared-provider-key", "sk-task10-shared-client-key", "Authorization", "Bearer", "DeadlineExceeded", "raw cause"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("encoded status leaks %q: %s", secret, encoded)
		}
	}
	// No backlog should exist for the failed append.
	outbox2, err := OpenOutbox(context.Background(), path, 16<<20)
	if err != nil {
		t.Fatalf("OpenOutbox() error = %v", err)
	}
	defer outbox2.Close()
	obStatus, err := outbox2.Status(context.Background())
	if err != nil {
		t.Fatalf("outbox.Status() error = %v", err)
	}
	if obStatus.BacklogEvents != 0 {
		t.Fatalf("BacklogEvents = %d, want 0 (failed append must not enqueue)", obStatus.BacklogEvents)
	}
	if failures != 1 {
		t.Fatalf("beforeAppendCommit hook must be called exactly once on the failed append path; got %d", failures)
	}
}

// TestHandleUsageAppendFailureClearsOnSuccess proves the recorded usage-side
// failure is cleared after a subsequent successful append. Since the runtime's
// worker then attempts to export to the 503 probe server and records a
// delivery-side error, we verify the usage-side failure was specifically
// cleared by checking the outbox received the event and the status shape
// remains valid under the strict decoder.
func TestHandleUsageAppendFailureClearsOnSuccess(t *testing.T) {
	runtime, path := newBoundProbeRuntime(t)

	var failures int32 = 1
	failSignal := make(chan struct{}, 1)
	successSignal := make(chan struct{}, 1)
	runtime.mu.RLock()
	runtime.outbox.testHooks.beforeAppendCommit = func(context.Context) error {
		if atomic.AddInt32(&failures, -1) >= 0 {
			select {
			case failSignal <- struct{}{}:
			default:
			}
			return context.DeadlineExceeded
		}
		select {
		case successSignal <- struct{}{}:
		default:
		}
		return nil
	}
	runtime.mu.RUnlock()

	ctx := contextWithRequestID(t, "append-fail-then-clear")
	runtime.HandleUsage(ctx, probeRecord())
	select {
	case <-failSignal:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for append failure hook")
	}

	status, err := runtime.ManagementStatus(context.Background())
	if err != nil {
		t.Fatalf("ManagementStatus after failure: %v", err)
	}
	if status.LastError == nil {
		t.Fatalf("expected LastError after append failure")
	}
	// The usage-side failure must be observable with a valid state shape.
	if perr := ValidateStatusResponse(status); perr != nil {
		t.Fatalf("append-failure status rejected by strict decoder: %v\n%+v", perr, status)
	}
	// Usage-side errors take precedence over delivery-side errors, so the state
	// must be blocked or degraded (non-retryable usage error, no nextRetryAt).
	if status.State != StateBlocked && status.State != StateDegraded {
		t.Fatalf("append-failure state = %q, want blocked or degraded", status.State)
	}

	// Second call: hook returns nil (no more failures) → append succeeds.
	runtime.HandleUsage(contextWithRequestID(t, "append-clear-success"), probeRecord())
	select {
	case <-successSignal:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for append success hook")
	}

	// Verify the successful append landed in the outbox (the usage-side failure
	// was cleared and the event was durably recorded).
	ob, err := OpenOutbox(context.Background(), path, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer ob.Close()
	obStatus, err := ob.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if obStatus.BacklogEvents < 1 {
		t.Fatalf("expected at least one backlog event after successful append; got %d", obStatus.BacklogEvents)
	}

	// After the successful append, the usage-side failure is cleared. The worker
	// may still report delivery-side errors from the 503 probe server, but the
	// status must remain valid under the strict decoder.
	clearStatus, err := runtime.ManagementStatus(context.Background())
	if err != nil {
		t.Fatalf("ManagementStatus after success: %v", err)
	}
	if perr := ValidateStatusResponse(clearStatus); perr != nil {
		t.Fatalf("cleared status rejected by strict decoder: %v\n%+v", perr, clearStatus)
	}
}
