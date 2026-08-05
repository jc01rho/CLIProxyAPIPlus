package keeperexport

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// SnapshotSource returns a complete, secret-bearing local source snapshot. The
// exporter projects it to the fixed secret-free wire categories before durable
// preparation. Implementations must return independent copies.
type SnapshotSource func() SnapshotInput

// Runtime owns one hot-reloadable outbox and its single ordered outbound
// worker. Usage appends never perform network I/O.
type Runtime struct {
	mu           sync.RWMutex
	outbox       *Outbox
	config       config.UsageExportConfig
	enabled      bool
	worker       *worker
	snapshot     SnapshotSource
	lastApplyErr *StatusError
	lastUsageErr *StatusError
}

func (r *Runtime) SetSnapshotSource(source SnapshotSource) {
	r.mu.Lock()
	r.snapshot = source
	if r.worker != nil {
		r.worker.setSnapshotSource(source)
	}
	r.mu.Unlock()
}

func (r *Runtime) Apply(ctx context.Context, cfg config.UsageExportConfig) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !cfg.Enabled || cfg.Mode == config.UsageExportModeDisabled {
		r.mu.Lock()
		oldWorker, oldOutbox := r.worker, r.outbox
		r.worker, r.outbox = nil, nil
		r.config, r.enabled, r.lastApplyErr, r.lastUsageErr = cfg, false, nil, nil
		r.mu.Unlock()
		stopWorker(oldWorker)
		if oldOutbox != nil {
			return oldOutbox.Close()
		}
		return nil
	}

	opened, err := OpenOutbox(ctx, cfg.Outbox.Path, cfg.Outbox.MaxBytes)
	if err != nil {
		r.recordApplyError(cfg, err)
		return err
	}
	created, err := newWorker(cfg, opened, r.snapshot)
	if err != nil {
		_ = opened.Close()
		r.recordApplyError(cfg, err)
		return err
	}

	r.mu.Lock()
	oldWorker, oldOutbox := r.worker, r.outbox
	r.worker, r.outbox = created, opened
	r.config, r.enabled, r.lastApplyErr, r.lastUsageErr = cfg, true, nil, nil
	r.mu.Unlock()
	stopWorker(oldWorker)
	created.start()
	if oldOutbox != nil {
		return oldOutbox.Close()
	}
	return nil
}

// HandleUsage is the named usage plugin entry point. Projection and durable
// append happen on the usage dispatcher, but Keeper delivery never does.
//
// Because the plugin interface cannot return an error, projection and append
// failures are surfaced through the sanitized runtime status (LastError) so a
// silent drop is impossible. The recorded error is cleared on the next
// successful projection+append.
func (r *Runtime) HandleUsage(ctx context.Context, record coreusage.Record) {
	if r == nil {
		return
	}
	// The usage manager dispatches records asynchronously, after the proxy
	// request that produced them has completed and net/http has cancelled the
	// request-scoped context. Durable outbox reads/writes must not depend on
	// request-context lifetime, so detach cancellation and deadlines while
	// preserving the values (request ID, endpoint, client metadata, response
	// status/headers) carried on the context.
	if ctx == nil {
		ctx = context.Background()
	} else {
		ctx = context.WithoutCancel(ctx)
	}
	r.mu.RLock()
	if !r.enabled || r.outbox == nil {
		applyFailed := r.lastApplyErr != nil
		r.mu.RUnlock()
		if applyFailed {
			return
		}
		// A usage event arriving while the runtime is disabled is a real drop
		// and must be observable through the sanitized status seam.
		r.recordUsageError(protocolError("invalid_field"))
		return
	}
	outbox := r.outbox
	privacy := r.config.Privacy
	r.mu.RUnlock()

	_, secret, bound, err := outbox.Binding(ctx)
	if err != nil || !bound {
		// Unbound or unreadable outbox: surface a stable non-retryable code so
		// the status API can report the drop without leaking the secret. Local
		// usage-side failures have no scheduled retry, so the code must be
		// non-retryable for the frozen state precedence to derive a valid
		// blocked/degraded state (retryable codes require a nextRetryAt).
		r.recordUsageError(protocolError("invalid_field"))
		return
	}
	payload, err := ProjectUsage(ctx, record, secret, privacy)
	for i := range secret {
		secret[i] = 0
	}
	if err != nil {
		// Projection produced nothing; the failure must be observable through
		// the sanitized status seam. The projection error is wrapped to the
		// closest stable code so the LastError never leaks raw request fields.
		r.recordUsageError(classifyProjectionError(err))
		return
	}
	if _, err = outbox.Append(ctx, payload); err != nil {
		// Append failure (outbox full, sequence exhausted, storage error,
		// context cancellation, ...). The runtime must observe the failure so
		// the status API can report it; the raw error is reduced to a stable
		// code so the LastError never leaks the inner cause.
		r.recordUsageError(classifyAppendError(err))
		return
	}
	// Successful append: clear any previously observed usage-side failure so
	// the status returns to the connected/retrying state.
	r.clearUsageError()
	r.mu.RLock()
	worker := r.worker
	r.mu.RUnlock()
	if worker != nil {
		worker.notifyUsage()
	}
}

// Append is retained for Task 3 compatibility and tests. Production usage
// must use HandleUsage so raw legacy envelopes cannot enter the push outbox.
func (r *Runtime) Append(ctx context.Context, payload []byte) error {
	r.mu.RLock()
	if !r.enabled || r.outbox == nil {
		r.mu.RUnlock()
		return nil
	}
	outbox := r.outbox
	_, err := outbox.Append(ctx, payload)
	worker := r.worker
	r.mu.RUnlock()
	if err == nil && worker != nil {
		worker.notifyUsage()
	}
	return err
}

func (r *Runtime) Status(ctx context.Context) (OutboxStatus, error) {
	r.mu.RLock()
	if !r.enabled || r.outbox == nil {
		r.mu.RUnlock()
		return OutboxStatus{}, nil
	}
	outbox := r.outbox
	r.mu.RUnlock()
	return outbox.Status(ctx)
}

// ManagementStatus returns the exact redacted management status shape.
func (r *Runtime) ManagementStatus(ctx context.Context) (StatusResponse, error) {
	metadata := map[string]int64{
		string(CategoryAuthFiles):          0,
		string(CategoryAPIKeys):            0,
		string(CategoryProviderIdentities): 0,
	}
	r.mu.RLock()
	cfg, enabled, outbox, worker := r.config, r.enabled, r.outbox, r.worker
	var applyErr *StatusError
	if r.lastApplyErr != nil {
		copy := *r.lastApplyErr
		applyErr = &copy
	}
	var usageErr *StatusError
	if r.lastUsageErr != nil {
		copy := *r.lastUsageErr
		usageErr = &copy
	}
	r.mu.RUnlock()
	if applyErr != nil {
		enabled = true
	}
	if usageErr != nil {
		enabled = true
	}

	response := StatusResponse{
		State:             StateDisabled,
		Enabled:           enabled,
		TokenConfigured:   cfg.Keeper.UsageExportTokenConfigured(),
		MetadataRevisions: metadata,
	}
	if !enabled || cfg.Mode == config.UsageExportModeDisabled || outbox == nil {
		if applyErr != nil {
			response.State, response.LastError = StateBlocked, applyErr
		}
		if usageErr != nil {
			response.State, response.LastError = StateBlocked, usageErr
		}
		return response, nil
	}

	outboxStatus, err := outbox.Status(ctx)
	if err != nil {
		stable := statusErrorFromError(err)
		response.State, response.LastError = StateBlocked, stable
		return response, nil
	}
	stream := outboxStatus.StreamID
	next := outboxStatus.NextSequence
	acknowledged := outboxStatus.AcknowledgedThrough
	response.StreamID = &stream
	response.NextSequence = &next
	response.AcknowledgedThrough = &acknowledged
	response.BacklogEvents = outboxStatus.BacklogEvents
	response.BacklogBytes = outboxStatus.BacklogBytes
	response.OldestBacklogAt = stringTimeValue(outboxStatus.OldestBacklogAt)
	for category, revision := range outboxStatus.MetadataRevisions {
		response.MetadataRevisions[string(category)] = revision
	}

	var workerStatus workerStatusSnapshot
	if worker != nil {
		workerStatus = worker.statusSnapshot()
	}
	response.Instance = workerStatus.instance
	response.LastAttemptAt = stringTime(workerStatus.lastAttemptAt)
	response.LastSuccessAt = stringTime(workerStatus.lastSuccessAt)
	response.NextRetryAt = stringTime(workerStatus.nextRetryAt)
	if workerStatus.hasNextExpected {
		expected := workerStatus.nextExpectedSequence
		response.NextExpectedSequence = &expected
	}
	response.LastError = workerStatus.lastError
	// Frozen precedence: apply-side start errors win over usage-side drop
	// errors, which win over transient delivery-side errors. This guarantees
	// silent drops are never observable as a "connected" state.
	if applyErr != nil {
		response.LastError = applyErr
	} else if usageErr != nil {
		response.LastError = usageErr
	}

	// Frozen precedence:
	//   blocked:   enabled + nonretryable lastError + no scheduled retry + no successful instance identity yet
	//   degraded:  enabled + nonretryable lastError + no scheduled retry + instance identity present
	//   retrying:  enabled + retryable lastError + scheduled retry
	//   starting:  enabled + no lastError + no success yet
	//   connected: enabled + no error + success
	switch {
	case response.LastError != nil && !response.LastError.Retryable && response.NextRetryAt == nil && response.Instance == nil:
		response.State = StateBlocked
	case response.LastError != nil && !response.LastError.Retryable && response.NextRetryAt == nil && response.Instance != nil:
		response.State = StateDegraded
	case response.LastError != nil && response.LastError.Retryable && response.NextRetryAt != nil:
		response.State = StateRetrying
	case response.LastSuccessAt == nil:
		response.State = StateStarting
	default:
		response.State = StateConnected
	}
	return response, nil
}

func (r *Runtime) Close() error {
	r.mu.Lock()
	worker, outbox := r.worker, r.outbox
	r.worker, r.outbox = nil, nil
	r.enabled = false
	r.lastUsageErr = nil
	r.mu.Unlock()
	stopWorker(worker)
	if outbox != nil {
		return outbox.Close()
	}
	return nil
}

func stopWorker(w *worker) {
	if w != nil {
		w.stop()
	}
}

func (r *Runtime) assertCurrent(outbox *Outbox) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.outbox != outbox {
		return fmt.Errorf("keeper export runtime was reconfigured")
	}
	return nil
}

func (r *Runtime) recordApplyError(cfg config.UsageExportConfig, err error) {
	r.mu.Lock()
	// An initial outbox-open failure has no active runtime to retain. Keep the
	// attempted config so the management status can accurately report that a
	// direct Keeper token was loaded, rather than misleadingly showing the
	// zero-value configuration. A failed hot reload keeps its working runtime
	// and configuration unchanged.
	if r.outbox == nil {
		r.config = cfg
		r.enabled = cfg.Enabled
	}
	r.lastApplyErr = statusErrorFromError(err)
	r.mu.Unlock()
}

// recordUsageError classifies a HandleUsage failure and atomically stores a
// sanitized StatusError so the runtime status seam can surface silent drops
// without leaking raw request material or outbox internals.
func (r *Runtime) recordUsageError(stable *Error) {
	r.mu.Lock()
	r.lastUsageErr = &StatusError{
		Code:      stable.Code,
		Message:   stable.Message,
		Retryable: stable.Retryable,
		At:        formatTimestamp(time.Now()),
	}
	r.mu.Unlock()
}

// clearUsageError nilies any previously observed usage-side failure. Called
// after a successful append so the sealed status returns to its normal
// connected/retrying state.
func (r *Runtime) clearUsageError() {
	r.mu.Lock()
	r.lastUsageErr = nil
	r.mu.Unlock()
}

// classifyProjectionError reduces a ProjectUsage error to the closest stable
// section-9 code so the sanitized LastError cannot leak the raw error string.
func classifyProjectionError(err error) *Error {
	if err == nil {
		return protocolError("invalid_field")
	}
	var protocol *Error
	if errors.As(err, &protocol) {
		return protocol
	}
	return protocolError("invalid_field")
}

// classifyAppendError reduces an outbox Append error to the closest stable
// section-9 code. Local append failures have no scheduled retry, so the code
// must be non-retryable for the frozen state precedence to derive a valid
// blocked/degraded state (retryable codes require a nextRetryAt). The stable
// storage_error/service_unavailable codes are retryable, so every local append
// failure reduces to the non-retryable invalid_field code.
func classifyAppendError(err error) *Error {
	if err == nil {
		return protocolError("invalid_field")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return protocolError("invalid_field")
	}
	if errors.Is(err, ErrOutboxFull) {
		return protocolError("invalid_field")
	}
	if errors.Is(err, ErrOutboxClosed) || errors.Is(err, ErrSequenceExhausted) || errors.Is(err, ErrInstanceBindingMismatch) {
		return protocolError("invalid_field")
	}
	var protocol *Error
	if errors.As(err, &protocol) {
		return protocol
	}
	return protocolError("invalid_field")
}

func statusErrorFromError(err error) *StatusError {
	stable := protocolError("storage_error")
	var protocol *Error
	if errors.As(err, &protocol) {
		stable = protocol
	}
	return &StatusError{Code: stable.Code, Message: stable.Message, Retryable: stable.Retryable, At: formatTimestamp(time.Now())}
}

func stringTimeValue(value *time.Time) *string {
	if value == nil {
		return nil
	}
	return stringTime(*value)
}

func metadataPending(outbox *Outbox, ctx context.Context) bool {
	if outbox == nil {
		return false
	}
	pending, err := outbox.PendingMetadata(ctx, []MetadataCategory{CategoryAuthFiles, CategoryAPIKeys, CategoryProviderIdentities})
	return err == nil && len(pending) > 0
}
