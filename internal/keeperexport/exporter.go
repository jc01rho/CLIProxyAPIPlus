package keeperexport

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

type workerStatusSnapshot struct {
	instance             *InstanceRef
	lastAttemptAt        time.Time
	lastSuccessAt        time.Time
	nextRetryAt          time.Time
	nextExpectedSequence int64
	hasNextExpected      bool
	lastError            *StatusError
}

func (w *worker) notifyStatus() {
	w.statusHookMu.RLock()
	hook := w.statusHook
	w.statusHookMu.RUnlock()
	if hook != nil {
		hook()
	}
}

func (w *worker) setStatusHook(hook func()) {
	w.statusHookMu.Lock()
	w.statusHook = hook
	w.statusHookMu.Unlock()
}

type worker struct {
	cfg      config.UsageExportConfig
	outbox   *Outbox
	client   *http.Client
	baseURL  string
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	usage    chan struct{}
	metadata chan struct{}

	sourceMu     sync.RWMutex
	source       SnapshotSource
	statusMu     sync.RWMutex
	status       workerStatusSnapshot
	statusHookMu sync.RWMutex
	statusHook   func()
}

func newWorker(cfg config.UsageExportConfig, outbox *Outbox, source SnapshotSource) (*worker, error) {
	client, err := newHTTPClient(cfg.Keeper, time.Duration(cfg.Delivery.RequestTimeoutMs)*time.Millisecond)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &worker{cfg: cfg, outbox: outbox, client: client, baseURL: strings.TrimRight(cfg.Keeper.URL, "/"), ctx: ctx, cancel: cancel, done: make(chan struct{}), usage: make(chan struct{}, 1), metadata: make(chan struct{}, 1), source: source}, nil
}

func newHTTPClient(cfg config.UsageExportKeeperConfig, timeout time.Duration) (*http.Client, error) {
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if cfg.CAFile != nil {
		pem, readErr := os.ReadFile(*cfg.CAFile)
		if readErr != nil || len(pem) == 0 || !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("keeper CA bundle is invalid")
		}
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	if cfg.ClientCertFile != nil {
		certificate, certErr := tls.LoadX509KeyPair(*cfg.ClientCertFile, *cfg.ClientKeyFile)
		if certErr != nil {
			return nil, fmt.Errorf("load Keeper client certificate: %w", certErr)
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	transport.DisableCompression = true
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) == 0 || !sameHTTPSOrigin(via[0].URL, req.URL) {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}, nil
}

func sameHTTPSOrigin(first, next *url.URL) bool {
	if first == nil || next == nil || !strings.EqualFold(first.Scheme, "https") || !strings.EqualFold(next.Scheme, "https") {
		return false
	}
	return strings.EqualFold(first.Host, next.Host)
}

func (w *worker) setSnapshotSource(source SnapshotSource) {
	w.sourceMu.Lock()
	w.source = source
	w.sourceMu.Unlock()
}
func (w *worker) start() { go w.run() }
func (w *worker) stop() {
	w.cancel()
	<-w.done
	if transport, ok := w.client.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
}
func (w *worker) notifyUsage() {
	select {
	case w.usage <- struct{}{}:
	default:
	}
}
func (w *worker) notifyMetadata() {
	select {
	case w.metadata <- struct{}{}:
	default:
	}
}

func (w *worker) run() {
	defer close(w.done)
	flushTimer := time.NewTimer(0)
	defer flushTimer.Stop()
	metadataTimer := time.NewTimer(0)
	defer metadataTimer.Stop()
	backoff := time.Duration(w.cfg.Delivery.InitialBackoffMs) * time.Millisecond
	bound := false
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-w.usage:
		case <-w.metadata:
		case <-flushTimer.C:
		case <-metadataTimer.C:
		}
		if !bound {
			if err := w.bindIdentity(); err != nil {
				w.recordFailure(err)
				if !retryableExportError(err) {
					<-w.ctx.Done()
					return
				}
				w.scheduleRetry(backoff)
				if !waitContext(w.ctx, backoff) {
					return
				}
				w.clearRetry()
				backoff = nextBackoff(backoff, time.Duration(w.cfg.Delivery.MaxBackoffMs)*time.Millisecond)
				continue
			}
			bound = true
			backoff = time.Duration(w.cfg.Delivery.InitialBackoffMs) * time.Millisecond
		}
		if err := w.deliverUsage(); err != nil {
			w.recordFailure(err)
			if !retryableExportError(err) {
				<-w.ctx.Done()
				return
			}
			w.scheduleRetry(backoff)
			if !waitContext(w.ctx, backoff) {
				return
			}
			w.clearRetry()
			backoff = nextBackoff(backoff, time.Duration(w.cfg.Delivery.MaxBackoffMs)*time.Millisecond)
			continue
		}
		if w.cfg.Metadata.Enabled {
			if err := w.deliverMetadata(); err != nil {
				w.recordFailure(err)
				if !retryableExportError(err) {
					<-w.ctx.Done()
					return
				}
				w.scheduleRetry(backoff)
				if !waitContext(w.ctx, backoff) {
					return
				}
				w.clearRetry()
				backoff = nextBackoff(backoff, time.Duration(w.cfg.Delivery.MaxBackoffMs)*time.Millisecond)
				continue
			}
		}
		backoff = time.Duration(w.cfg.Delivery.InitialBackoffMs) * time.Millisecond
		resetTimer(flushTimer, time.Duration(w.cfg.Delivery.FlushIntervalMs)*time.Millisecond)
		resetTimer(metadataTimer, time.Duration(w.cfg.Metadata.IntervalMs)*time.Millisecond)
	}
}

func (w *worker) bindIdentity() error {
	body, status, err := w.request(http.MethodGet, "/api/v1/export/identity", nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return decodeRemoteFailure(status, body, http.Header{})
	}
	identity, perr := DecodeIdentityResponse(body)
	if perr != nil {
		return protocolError("keeper_invalid_response")
	}
	if !contains(identity.Credential.Scopes, "identity:test") {
		return protocolError("insufficient_scope")
	}
	_, err = w.outbox.BindInstance(w.ctx, identity.Instance.InstanceID)
	if err == nil {
		w.recordIdentitySuccess(identity.Instance)
	}
	return err
}

func (w *worker) deliverUsage() error {
	for {
		status, err := w.outbox.Status(w.ctx)
		if err != nil || status.BacklogEvents == 0 {
			return err
		}
		events, err := w.listBatch(status.StreamID)
		if err != nil || len(events) == 0 {
			return err
		}
		body := marshalUsageBatch(status.StreamID, events)
		response, httpStatus, err := w.request(http.MethodPost, "/api/v1/export/usage-batches", body)
		if err != nil {
			return err
		}
		if httpStatus != http.StatusOK {
			return decodeRemoteFailure(httpStatus, response, http.Header{})
		}
		ack, perr := DecodeUsageAck(response)
		if perr != nil {
			return perr
		}
		if perr = ValidateUsageAck(ack, status.StreamID, status.AcknowledgedThrough, status.NextSequence, int64(len(events))); perr != nil {
			return perr
		}
		if err = w.outbox.Acknowledge(w.ctx, ack.AcknowledgedThrough); err != nil {
			return err
		}
		w.recordUsageSuccess(ack.NextExpectedSequence)
		if ack.AcknowledgedThrough < events[len(events)-1].Sequence {
			return nil
		}
	}
}

func (w *worker) listBatch(streamID string) ([]OutboxEvent, error) {
	limit := int(w.cfg.Delivery.MaxBatchEvents)
	events, err := w.outbox.List(w.ctx, limit, w.cfg.Delivery.MaxBatchBytes)
	if err != nil {
		return nil, err
	}
	for len(events) > 0 && len(marshalUsageBatch(streamID, events)) > int(w.cfg.Delivery.MaxBatchBytes) {
		events = events[:len(events)-1]
	}
	return events, nil
}

func marshalUsageBatch(streamID string, events []OutboxEvent) []byte {
	wire := usageBatchWire{ProtocolVersion: ProtocolVersion, StreamID: streamID, Events: make([]usageEventWire, 0, len(events))}
	for _, event := range events {
		wire.Events = append(wire.Events, usageEventWire{Sequence: event.Sequence, Payload: json.RawMessage(event.Payload)})
	}
	body, _ := json.Marshal(wire)
	return body
}

func (w *worker) deliverMetadata() error {
	categories := configuredCategories(w.cfg.Metadata.Categories)
	pending, err := w.outbox.PendingMetadata(w.ctx, categories)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		w.sourceMu.RLock()
		source := w.source
		w.sourceMu.RUnlock()
		if source == nil {
			return nil
		}
		input := source()
		_, secret, bound, bindErr := w.outbox.Binding(w.ctx)
		if bindErr != nil || !bound {
			return bindErr
		}
		defer func() {
			for i := range secret {
				secret[i] = 0
			}
		}()
		status, statusErr := w.outbox.Status(w.ctx)
		if statusErr != nil {
			return statusErr
		}
		for _, category := range categories {
			revision := status.MetadataRevisions[category] + 1
			if floor := w.outbox.MetadataRevisionFloor(w.ctx, category); floor+1 > revision {
				revision = floor + 1
			}
			body, digest, projectErr := ProjectMetadata(input, secret, category, revision, time.Now().UTC())
			if projectErr != nil {
				return projectErr
			}
			prepared, prepareErr := w.outbox.PrepareMetadata(w.ctx, category, digest, body)
			if prepareErr != nil {
				return prepareErr
			}
			if prepared.Pending {
				pending = append(pending, *prepared)
			}
		}
	}
	for _, item := range pending {
		recoveredConflict := false
		for {
			response, status, respHeaders, requestErr := w.requestWithHeaders(http.MethodPut, "/api/v1/export/metadata/"+string(item.Category), item.Body)
			if requestErr != nil {
				return requestErr
			}
			if status != http.StatusOK {
				requestErr = decodeRemoteFailure(status, response, respHeaders)
				var remoteErr *Error
				if !errors.As(requestErr, &remoteErr) {
					return requestErr
				}
				if remoteErr.Code == "stale_revision" && remoteErr.CurrentRevision > item.Revision {
					targetRevision := remoteErr.CurrentRevision + 1
					if targetRevision <= item.Revision {
						return requestErr
					}
					w.recordKeeperRevision(item.Category, remoteErr.CurrentRevision)
					replacement, replacementErr := w.supersedeConflictingMetadata(item, targetRevision)
					if replacementErr != nil {
						return replacementErr
					}
					item = *replacement
					continue
				}
				if recoveredConflict || remoteErr.Code != "conflicting_revision" {
					return requestErr
				}
				targetRevision := item.Revision + 1
				if remoteErr.CurrentRevision > item.Revision {
					targetRevision = remoteErr.CurrentRevision + 1
				}
				replacement, replacementErr := w.supersedeConflictingMetadata(item, targetRevision)
				if replacementErr != nil {
					return replacementErr
				}
				item = *replacement
				recoveredConflict = true
				continue
			}
			applied, perr := DecodeMetadataApplyResponse(response)
			snapshot, snapshotErr := DecodeMetadataSnapshot(item.Body, item.Category)
			if perr != nil || snapshotErr != nil || applied.Category != item.Category || applied.Revision != item.Revision || applied.ItemCount != metadataItemCount(snapshot, item.Category) {
				return protocolError("keeper_invalid_response")
			}
			if err = w.outbox.AcknowledgeMetadata(w.ctx, item.Category, item.Revision); err != nil {
				return err
			}
			w.recordSuccess()
			break
		}
	}
	return nil
}

func (w *worker) supersedeConflictingMetadata(item PreparedMetadata, targetRevision int64) (*PreparedMetadata, error) {
	w.sourceMu.RLock()
	source := w.source
	w.sourceMu.RUnlock()
	if source == nil {
		return nil, protocolError("internal_error")
	}
	_, secret, bound, err := w.outbox.Binding(w.ctx)
	if err != nil || !bound {
		return nil, protocolError("storage_error")
	}
	defer func() {
		for i := range secret {
			secret[i] = 0
		}
	}()
	if targetRevision <= item.Revision {
		targetRevision = item.Revision + 1
	}
	body, digest, err := ProjectMetadata(source(), secret, item.Category, targetRevision, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if targetRevision == item.Revision+1 {
		return w.outbox.SupersedePendingMetadata(w.ctx, item.Category, item.Revision, digest, body)
	}
	return w.outbox.AdvancePendingMetadata(w.ctx, item.Category, item.Revision, targetRevision, digest, body)
}

func (w *worker) recordKeeperRevision(category MetadataCategory, keeperCurrent int64) {
	_ = w.outbox.SetMetadataRevisionFloor(w.ctx, category, keeperCurrent)
}

func (w *worker) recordAttempt() {
	w.statusMu.Lock()
	w.status.lastAttemptAt = time.Now().UTC()
	w.statusMu.Unlock()
	w.notifyStatus()
}

func (w *worker) recordIdentitySuccess(instance InstanceRef) {
	w.statusMu.Lock()
	copy := instance
	w.status.instance = &copy
	w.status.lastSuccessAt = time.Now().UTC()
	w.status.lastError = nil
	w.status.nextRetryAt = time.Time{}
	w.statusMu.Unlock()
	w.notifyStatus()
}

func (w *worker) recordUsageSuccess(nextExpected int64) {
	w.statusMu.Lock()
	w.status.nextExpectedSequence = nextExpected
	w.status.hasNextExpected = true
	w.status.lastSuccessAt = time.Now().UTC()
	w.status.lastError = nil
	w.status.nextRetryAt = time.Time{}
	w.statusMu.Unlock()
	w.notifyStatus()
}

func (w *worker) recordSuccess() {
	w.statusMu.Lock()
	w.status.lastSuccessAt = time.Now().UTC()
	w.status.lastError = nil
	w.status.nextRetryAt = time.Time{}
	w.statusMu.Unlock()
	w.notifyStatus()
}

func (w *worker) recordFailure(err error) {
	stable := protocolError("internal_error")
	var protocol *Error
	if errors.As(err, &protocol) {
		stable = protocol
	} else {
		stable = protocolError("storage_error")
	}
	w.statusMu.Lock()
	w.status.lastError = &StatusError{Code: stable.Code, Message: stable.Message, Retryable: stable.Retryable, At: formatTimestamp(time.Now())}
	w.statusMu.Unlock()
	w.notifyStatus()
}

func (w *worker) scheduleRetry(backoff time.Duration) {
	w.statusMu.Lock()
	w.status.nextRetryAt = time.Now().UTC().Add(backoff)
	w.statusMu.Unlock()
	w.notifyStatus()
}

func (w *worker) clearRetry() {
	w.statusMu.Lock()
	w.status.nextRetryAt = time.Time{}
	w.statusMu.Unlock()
	w.notifyStatus()
}

func (w *worker) statusSnapshot() workerStatusSnapshot {
	w.statusMu.RLock()
	defer w.statusMu.RUnlock()
	copy := w.status
	if w.status.instance != nil {
		instance := *w.status.instance
		copy.instance = &instance
	}
	if w.status.lastError != nil {
		lastError := *w.status.lastError
		copy.lastError = &lastError
	}
	return copy
}

func (w *worker) request(method, path string, body []byte) ([]byte, int, error) {
	data, status, _, err := w.requestWithHeaders(method, path, body)
	return data, status, err
}

func (w *worker) requestWithHeaders(method, path string, body []byte) ([]byte, int, http.Header, error) {
	w.recordAttempt()
	token := w.cfg.Keeper.UsageExportToken()
	if token == "" {
		return nil, 0, nil, protocolError("token_env_unset")
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(w.ctx, method, w.baseURL+path, reader)
	if err != nil {
		return nil, 0, nil, protocolError("keeper_unreachable")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := w.client.Do(req)
	token = ""
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
			return nil, 0, nil, protocolError("keeper_timeout")
		}
		if isTLSValidationError(err) {
			return nil, 0, nil, protocolError("keeper_tls_error")
		}
		return nil, 0, nil, protocolError("keeper_unreachable")
	}
	defer response.Body.Close()
	if perr := validateKeeperResponseHeaders(response, MaxBodyBytes, false); perr != nil {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil, 0, nil, perr
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, MaxBodyBytes+1))
	if err != nil {
		return nil, 0, nil, protocolError("keeper_unreachable")
	}
	if len(responseBody) > MaxBodyBytes {
		return nil, 0, nil, protocolError("keeper_invalid_response")
	}
	return responseBody, response.StatusCode, response.Header, nil
}

func decodeRemoteFailure(status int, body []byte, headers http.Header) error {
	var err error
	if envelope, perr := DecodeErrorEnvelope(body); perr == nil && envelope.Error.HTTPStatus == status {
		// Map the remote code to the local stable error so the
		// remote body/message is never echoed back.
		err = protocolError(envelope.Error.Code)
	} else {
		switch {
		case status == http.StatusUnauthorized:
			err = protocolError("invalid_credential")
		case status == http.StatusForbidden:
			err = protocolError("insufficient_scope")
		case status == http.StatusTooManyRequests:
			err = protocolError("rate_limited")
		case status >= 500:
			err = protocolError("service_unavailable")
		default:
			err = protocolError("keeper_invalid_response")
		}
	}
	var remoteErr *Error
	if errors.As(err, &remoteErr) && headers != nil {
		if raw := strings.TrimSpace(headers.Get(HeaderCurrentRevision)); raw != "" {
			if current, parseErr := strconv.ParseInt(raw, 10, 64); parseErr == nil && current > 0 {
				remoteErr.CurrentRevision = current
			}
		}
	}
	return err
}

func metadataItemCount(snapshot *MetadataSnapshot, category MetadataCategory) int64 {
	switch category {
	case CategoryAuthFiles:
		return int64(len(snapshot.AuthFiles))
	case CategoryAPIKeys:
		return int64(len(snapshot.APIKeys))
	case CategoryProviderIdentities:
		return int64(len(snapshot.ProviderIdentities))
	default:
		return -1
	}
}

func isTLSValidationError(err error) bool {
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalidCertificate x509.CertificateInvalidError
	var verification *tls.CertificateVerificationError
	return errors.As(err, &unknownAuthority) || errors.As(err, &hostname) || errors.As(err, &invalidCertificate) || errors.As(err, &verification)
}

func retryableExportError(err error) bool {
	var protocol *Error
	return errors.As(err, &protocol) && protocol.Retryable
}
func configuredCategories(raw []string) []MetadataCategory {
	result := make([]MetadataCategory, 0, len(raw))
	for _, item := range raw {
		result = append(result, MetadataCategory(item))
	}
	return result
}
func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func nextBackoff(current, maximum time.Duration) time.Duration {
	current *= 2
	if current > maximum {
		return maximum
	}
	return current
}
func waitContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}
