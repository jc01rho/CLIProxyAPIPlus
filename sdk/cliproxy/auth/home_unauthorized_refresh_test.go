package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

const homeUnauthorizedRefreshProvider = "home-unauthorized-refresh"

type homeUnauthorizedRefreshDispatcher struct {
	calls atomic.Int32
}

func (*homeUnauthorizedRefreshDispatcher) HeartbeatOK() bool { return true }

func (d *homeUnauthorizedRefreshDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	d.calls.Add(1)
	return json.Marshal(homeAuthDispatchResponse{Auth: Auth{
		ID:       "home-refresh-auth",
		Provider: homeUnauthorizedRefreshProvider,
		Status:   StatusActive,
		Attributes: map[string]string{
			AttributeAuthKind: AuthKindOAuth,
			"websockets":      "true",
		},
		Metadata: map[string]any{
			"access_token": "stale-access-token",
		},
	}})
}

func (*homeUnauthorizedRefreshDispatcher) AbortAmbiguousDispatch() {}

type homeUnauthorizedRefreshExecutor struct {
	streamMode         string
	refreshErr         error
	keepStale          bool
	retainSelection    bool
	requirePrepared    bool
	alwaysUnauthorized bool
	countAccessTokens  []string
	nilRetryStream     bool
	nilRetryChunks     bool
	executeCalls       atomic.Int32
	countCalls         atomic.Int32
	streamCalls        atomic.Int32
	refreshCalls       atomic.Int32
	prepareCalls       atomic.Int32
	refreshInputsMu    sync.Mutex
	refreshInputs      []string
}

func (*homeUnauthorizedRefreshExecutor) Identifier() string { return homeUnauthorizedRefreshProvider }

func (e *homeUnauthorizedRefreshExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.executeCalls.Add(1)
	if e.retainSelection {
		if lifecycle, ok := opts.ExecutionLifecycle.(interface{ Retain() }); ok {
			lifecycle.Retain()
		}
	}
	if authAccessToken(auth) == "stale-access-token" {
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusUnauthorized, Message: "expired access token"}
	}
	if e.requirePrepared && auth.Metadata["project_id"] != "prepared-project" {
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusBadRequest, Message: "missing prepared auth"}
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *homeUnauthorizedRefreshExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.streamCalls.Add(1)
	if authAccessToken(auth) == "stale-access-token" {
		switch e.streamMode {
		case "bootstrap":
			chunks := make(chan cliproxyexecutor.StreamChunk, 1)
			chunks <- cliproxyexecutor.StreamChunk{Err: &Error{HTTPStatus: http.StatusUnauthorized, Message: "expired access token"}}
			close(chunks)
			return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
		case "started":
			chunks := make(chan cliproxyexecutor.StreamChunk, 2)
			chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("started")}
			chunks <- cliproxyexecutor.StreamChunk{Err: &Error{HTTPStatus: http.StatusUnauthorized, Message: "expired access token"}}
			close(chunks)
			return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
		default:
			return nil, &Error{HTTPStatus: http.StatusUnauthorized, Message: "expired access token"}
		}
	}
	if e.requirePrepared && auth.Metadata["project_id"] != "prepared-project" {
		return nil, &Error{HTTPStatus: http.StatusBadRequest, Message: "missing prepared auth"}
	}
	if e.nilRetryStream {
		return nil, nil
	}
	if e.nilRetryChunks {
		return &cliproxyexecutor.StreamResult{}, nil
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("ok")}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *homeUnauthorizedRefreshExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	e.refreshCalls.Add(1)
	e.refreshInputsMu.Lock()
	e.refreshInputs = append(e.refreshInputs, authAccessToken(auth))
	e.refreshInputsMu.Unlock()
	if e.refreshErr != nil {
		return nil, e.refreshErr
	}
	updated := auth.Clone()
	if e.keepStale {
		return updated, nil
	}
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	updated.Metadata["access_token"] = "fresh-access-token"
	if e.requirePrepared {
		delete(updated.Metadata, "project_id")
	}
	return updated, nil
}

func (e *homeUnauthorizedRefreshExecutor) ShouldPrepareRequestAuth(auth *Auth) bool {
	return e.requirePrepared && auth != nil && auth.Metadata["project_id"] != "prepared-project"
}

func (e *homeUnauthorizedRefreshExecutor) PrepareRequestAuth(_ context.Context, auth *Auth) (*Auth, error) {
	e.prepareCalls.Add(1)
	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	updated.Metadata["project_id"] = "prepared-project"
	return updated, nil
}

func (e *homeUnauthorizedRefreshExecutor) CountTokens(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	call := int(e.countCalls.Add(1))
	if call <= len(e.countAccessTokens) {
		effective := auth.Clone()
		if effective.Metadata == nil {
			effective.Metadata = make(map[string]any)
		}
		effective.Metadata["access_token"] = e.countAccessTokens[call-1]
		NotifyAccessTokenFingerprint(ctx, effective)
	}
	if e.alwaysUnauthorized || authAccessToken(auth) == "stale-access-token" {
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusUnauthorized, Message: "expired access token"}
	}
	if e.requirePrepared && auth.Metadata["project_id"] != "prepared-project" {
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusBadRequest, Message: "missing prepared auth"}
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (*homeUnauthorizedRefreshExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func newHomeUnauthorizedRefreshManager(dispatcher *homeUnauthorizedRefreshDispatcher, executor *homeUnauthorizedRefreshExecutor) *Manager {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	manager.RegisterExecutor(executor)
	return manager
}

func TestHomeUnauthorizedRefreshesSameSelectionBeforeRedispatch(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*Manager) error
	}{
		{
			name: "execute",
			run: func(manager *Manager) error {
				_, errExecute := manager.Execute(context.Background(), []string{homeUnauthorizedRefreshProvider}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "count_tokens",
			run: func(manager *Manager) error {
				_, errCount := manager.ExecuteCount(context.Background(), []string{homeUnauthorizedRefreshProvider}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{})
				return errCount
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := &homeUnauthorizedRefreshDispatcher{}
			executor := &homeUnauthorizedRefreshExecutor{}
			manager := newHomeUnauthorizedRefreshManager(dispatcher, executor)

			if errRun := test.run(manager); errRun != nil {
				t.Fatalf("execution error = %v", errRun)
			}
			if got := dispatcher.calls.Load(); got != 1 {
				t.Fatalf("Home dispatch calls = %d, want 1", got)
			}
			if got := executor.refreshCalls.Load(); got != 1 {
				t.Fatalf("refresh calls = %d, want 1", got)
			}
			if test.name == "execute" && executor.executeCalls.Load() != 2 {
				t.Fatalf("execute calls = %d, want 2", executor.executeCalls.Load())
			}
			if test.name == "count_tokens" && executor.countCalls.Load() != 2 {
				t.Fatalf("count calls = %d, want 2", executor.countCalls.Load())
			}
		})
	}
}

func TestHomeUnauthorizedRefreshRepreparesAuthBeforeRetry(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*Manager) error
	}{
		{
			name: "execute",
			run: func(manager *Manager) error {
				_, errExecute := manager.Execute(context.Background(), []string{homeUnauthorizedRefreshProvider}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "count_tokens",
			run: func(manager *Manager) error {
				_, errCount := manager.ExecuteCount(context.Background(), []string{homeUnauthorizedRefreshProvider}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{})
				return errCount
			},
		},
		{
			name: "stream",
			run: func(manager *Manager) error {
				result, errStream := manager.ExecuteStream(context.Background(), []string{homeUnauthorizedRefreshProvider}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{Stream: true})
				if errStream != nil {
					return errStream
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						return chunk.Err
					}
				}
				return nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := &homeUnauthorizedRefreshDispatcher{}
			executor := &homeUnauthorizedRefreshExecutor{requirePrepared: true}
			manager := newHomeUnauthorizedRefreshManager(dispatcher, executor)

			if errRun := test.run(manager); errRun != nil {
				t.Fatalf("execution error = %v", errRun)
			}
			if got := executor.refreshCalls.Load(); got != 1 {
				t.Fatalf("refresh calls = %d, want 1", got)
			}
			if got := executor.prepareCalls.Load(); got != 2 {
				t.Fatalf("prepare calls = %d, want initial preparation and refreshed preparation", got)
			}
		})
	}
}

func TestHomeUnauthorizedRefreshUpdatesRetainedSelection(t *testing.T) {
	dispatcher := &homeUnauthorizedRefreshDispatcher{}
	executor := &homeUnauthorizedRefreshExecutor{retainSelection: true}
	manager := newHomeUnauthorizedRefreshManager(dispatcher, executor)
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.ExecutionSessionMetadataKey: "refresh-session",
		cliproxyexecutor.PinnedAuthMetadataKey:       "home-refresh-auth",
	}}

	for range 2 {
		if _, errExecute := manager.Execute(ctx, []string{homeUnauthorizedRefreshProvider}, cliproxyexecutor.Request{Model: "model-a"}, opts); errExecute != nil {
			t.Fatalf("Execute() error = %v", errExecute)
		}
	}
	if got := dispatcher.calls.Load(); got != 1 {
		t.Fatalf("Home dispatch calls = %d, want one retained selection", got)
	}
	if got := executor.refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want refreshed token reused by retained selection", got)
	}
	if got := executor.executeCalls.Load(); got != 3 {
		t.Fatalf("execute calls = %d, want stale attempt, retry, and retained reuse", got)
	}
}

func TestRefreshHomeSelectionReusesConcurrentNewerToken(t *testing.T) {
	executor := &homeUnauthorizedRefreshExecutor{requirePrepared: true}
	selection := &HomeDispatchSelection{
		Auth:     &Auth{ID: "home-refresh-auth", Provider: homeUnauthorizedRefreshProvider, Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth}, Metadata: map[string]any{"access_token": "fresh-access-token"}},
		Executor: executor,
		Provider: homeUnauthorizedRefreshProvider,
	}
	failed := &Auth{ID: "home-refresh-auth", Provider: homeUnauthorizedRefreshProvider, Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth}, Metadata: map[string]any{"access_token": "stale-access-token"}}
	manager := NewManager(nil, nil, nil)

	updated, reused, errRefresh := manager.RefreshHomeSelectionAfterUnauthorized(context.Background(), selection, failed)
	if errRefresh != nil || !reused || authAccessToken(updated) != "fresh-access-token" {
		t.Fatalf("RefreshHomeSelectionAfterUnauthorized() = %#v, %v, %v", updated, reused, errRefresh)
	}
	if got := executor.refreshCalls.Load(); got != 0 {
		t.Fatalf("refresh calls = %d, want 0 when selection already has a newer token", got)
	}
	if got := executor.prepareCalls.Load(); got != 1 {
		t.Fatalf("prepare calls = %d, want reused token prepared once", got)
	}
	if updated.Metadata["project_id"] != "prepared-project" {
		t.Fatalf("reused auth metadata = %#v, want prepared project", updated.Metadata)
	}
}

func TestHomeUnauthorizedRefreshIsAttemptedAtMostOnce(t *testing.T) {
	dispatcher := &homeUnauthorizedRefreshDispatcher{}
	executor := &homeUnauthorizedRefreshExecutor{keepStale: true}
	manager := newHomeUnauthorizedRefreshManager(dispatcher, executor)

	_, errExecute := manager.Execute(context.Background(), []string{homeUnauthorizedRefreshProvider}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{})
	if statusCodeFromError(errExecute) != http.StatusUnauthorized {
		t.Fatalf("Execute() error = %v, want original 401", errExecute)
	}
	if got := executor.refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want exactly 1", got)
	}
	if got := executor.executeCalls.Load(); got != 2 {
		t.Fatalf("execute calls = %d, want initial attempt and one retry", got)
	}
}

func TestHomeCountTokensReportsEveryUnauthorizedAttempt(t *testing.T) {
	records := make(chan coreusage.Record, 8)
	const pluginName = "auth-home-count-unauthorized-test"
	coreusage.RegisterNamedPlugin(pluginName, homeResultCapturePlugin{records: records})
	t.Cleanup(func() {
		coreusage.RegisterNamedPlugin(pluginName, homeResultCapturePlugin{})
	})

	dispatcher := &homeUnauthorizedRefreshDispatcher{}
	executor := &homeUnauthorizedRefreshExecutor{
		alwaysUnauthorized: true,
		countAccessTokens:  []string{"executor-internal-token", "retry-internal-token"},
	}
	manager := newHomeUnauthorizedRefreshManager(dispatcher, executor)
	_, errCount := manager.ExecuteCount(context.Background(), []string{homeUnauthorizedRefreshProvider}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{})
	if statusCodeFromError(errCount) != http.StatusUnauthorized {
		t.Fatalf("ExecuteCount() error = %v, want final 401", errCount)
	}
	if got := executor.refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}
	executor.refreshInputsMu.Lock()
	refreshInputs := append([]string(nil), executor.refreshInputs...)
	executor.refreshInputsMu.Unlock()
	if len(refreshInputs) != 1 || refreshInputs[0] != "executor-internal-token" {
		t.Fatalf("refresh input tokens = %#v, want internally refreshed token", refreshInputs)
	}
	if got := executor.countCalls.Load(); got != 2 {
		t.Fatalf("CountTokens calls = %d, want 2", got)
	}

	wantHashes := map[string]bool{
		AccessTokenSHA256(&Auth{Metadata: map[string]any{"access_token": "executor-internal-token"}}): false,
		AccessTokenSHA256(&Auth{Metadata: map[string]any{"access_token": "retry-internal-token"}}):    false,
	}
	matchedRecords := 0
	deadline := time.After(time.Second)
	for remaining := len(wantHashes); remaining > 0; {
		select {
		case record := <-records:
			if record.AuthID != "home-refresh-auth" || record.Fail.StatusCode != http.StatusUnauthorized {
				continue
			}
			matchedRecords++
			if seen, ok := wantHashes[record.AccessTokenSHA256]; ok && !seen {
				wantHashes[record.AccessTokenSHA256] = true
				remaining--
			}
		case <-deadline:
			t.Fatalf("unauthorized attempt fingerprints = %#v", wantHashes)
		}
	}
	timer := time.NewTimer(50 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case record := <-records:
			if record.AuthID == "home-refresh-auth" && record.Fail.StatusCode == http.StatusUnauthorized {
				matchedRecords++
			}
		case <-timer.C:
			if matchedRecords != 2 {
				t.Fatalf("unauthorized usage records = %d, want exactly 2", matchedRecords)
			}
			return
		}
	}
}

func TestHomeNoCandidateAfterRefreshFailurePreservesRefreshError(t *testing.T) {
	refreshErr := &Error{Code: "refresh_temporarily_unavailable", HTTPStatus: http.StatusServiceUnavailable, Message: "refresh unavailable"}
	noCandidate := &Error{Code: "auth_not_found", HTTPStatus: http.StatusServiceUnavailable, Message: "no auth available"}
	if !shouldReturnLastErrorOnPickFailure(true, refreshErr, noCandidate) {
		t.Fatal("Home no-candidate error would overwrite the original refresh error")
	}
}

func TestHomeUnauthorizedTransientRefreshFailureIsReturned(t *testing.T) {
	dispatcher := &homeUnauthorizedRefreshDispatcher{}
	executor := &homeUnauthorizedRefreshExecutor{
		refreshErr: &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "Home refresh temporarily unavailable"},
	}
	manager := newHomeUnauthorizedRefreshManager(dispatcher, executor)

	_, errExecute := manager.Execute(context.Background(), []string{homeUnauthorizedRefreshProvider}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{})
	if statusCodeFromError(errExecute) != http.StatusServiceUnavailable {
		t.Fatalf("Execute() error = %v, want transient 503", errExecute)
	}
	if got := executor.executeCalls.Load(); got != 1 {
		t.Fatalf("execute calls = %d, want 1", got)
	}
	if got := executor.refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}
}

func TestHomeUnauthorizedStreamRefreshesAtMostOnceAcrossRedispatch(t *testing.T) {
	dispatcher := &homeUnauthorizedRefreshDispatcher{}
	executor := &homeUnauthorizedRefreshExecutor{keepStale: true}
	manager := newHomeUnauthorizedRefreshManager(dispatcher, executor)

	_, errStream := manager.ExecuteStream(context.Background(), []string{homeUnauthorizedRefreshProvider}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{Stream: true})
	if statusCodeFromError(errStream) != http.StatusUnauthorized {
		t.Fatalf("ExecuteStream() error = %v, want original 401", errStream)
	}
	if got := executor.refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want exactly 1", got)
	}
	if got := executor.streamCalls.Load(); got != 2 {
		t.Fatalf("stream calls = %d, want initial attempt and one retry", got)
	}
}

func TestHomeUnauthorizedBootstrapRetryRejectsEmptyStream(t *testing.T) {
	for _, test := range []struct {
		name           string
		nilRetryStream bool
		nilRetryChunks bool
	}{
		{name: "nil result", nilRetryStream: true},
		{name: "nil chunks", nilRetryChunks: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := &homeUnauthorizedRefreshDispatcher{}
			executor := &homeUnauthorizedRefreshExecutor{
				streamMode:     "bootstrap",
				nilRetryStream: test.nilRetryStream,
				nilRetryChunks: test.nilRetryChunks,
			}
			manager := newHomeUnauthorizedRefreshManager(dispatcher, executor)

			result, errStream := manager.ExecuteStream(context.Background(), []string{homeUnauthorizedRefreshProvider}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{Stream: true})
			if errStream != nil {
				t.Fatalf("ExecuteStream() error = %v", errStream)
			}
			var streamErr error
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					streamErr = chunk.Err
				}
			}
			var authErr *Error
			if !errors.As(streamErr, &authErr) || authErr.Code != "empty_stream" {
				t.Fatalf("stream error = %#v, want empty_stream", streamErr)
			}
			if got := executor.streamCalls.Load(); got != 2 {
				t.Fatalf("stream calls = %d, want initial attempt and one retry", got)
			}
		})
	}
}

func TestHomeUnauthorizedStartedStreamDoesNotReplay(t *testing.T) {
	dispatcher := &homeUnauthorizedRefreshDispatcher{}
	executor := &homeUnauthorizedRefreshExecutor{streamMode: "started"}
	manager := newHomeUnauthorizedRefreshManager(dispatcher, executor)

	result, errStream := manager.ExecuteStream(context.Background(), []string{homeUnauthorizedRefreshProvider}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{Stream: true})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	sawPayload := false
	sawUnauthorized := false
	for chunk := range result.Chunks {
		if string(chunk.Payload) == "started" {
			sawPayload = true
		}
		if statusCodeFromError(chunk.Err) == http.StatusUnauthorized {
			sawUnauthorized = true
		}
	}
	if !sawPayload || !sawUnauthorized {
		t.Fatalf("stream results = payload %v unauthorized %v, want both", sawPayload, sawUnauthorized)
	}
	if got := executor.refreshCalls.Load(); got != 0 {
		t.Fatalf("refresh calls = %d, want 0 after stream started", got)
	}
	if got := executor.streamCalls.Load(); got != 1 {
		t.Fatalf("stream calls = %d, want 1", got)
	}
}

func TestHomeUnauthorizedStreamRefreshesBeforeRedispatch(t *testing.T) {
	for _, mode := range []string{"synchronous", "bootstrap"} {
		t.Run(mode, func(t *testing.T) {
			dispatcher := &homeUnauthorizedRefreshDispatcher{}
			executor := &homeUnauthorizedRefreshExecutor{streamMode: mode}
			manager := newHomeUnauthorizedRefreshManager(dispatcher, executor)

			result, errStream := manager.ExecuteStream(context.Background(), []string{homeUnauthorizedRefreshProvider}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{Stream: true})
			if errStream != nil {
				t.Fatalf("ExecuteStream() error = %v", errStream)
			}
			var payload string
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatalf("stream chunk error = %v", chunk.Err)
				}
				payload += string(chunk.Payload)
			}
			if payload != "ok" {
				t.Fatalf("stream payload = %q, want ok", payload)
			}
			if got := dispatcher.calls.Load(); got != 1 {
				t.Fatalf("Home dispatch calls = %d, want 1", got)
			}
			if got := executor.refreshCalls.Load(); got != 1 {
				t.Fatalf("refresh calls = %d, want 1", got)
			}
			if got := executor.streamCalls.Load(); got != 2 {
				t.Fatalf("stream calls = %d, want 2", got)
			}
		})
	}
}
