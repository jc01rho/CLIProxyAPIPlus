package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// ExecuteWithAuth runs one non-streaming request on the named auth without any
// selector involvement. It reuses the normal per-auth execution plumbing
// (transport, request-auth preparation, one refresh on 401) and records the
// outcome through MarkResult so cooldown and quota observation stay current.
// Callers own retries and auth eligibility.
func (m *Manager) ExecuteWithAuth(ctx context.Context, authID string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if m == nil {
		return cliproxyexecutor.Response{}, &Error{Code: "manager_unavailable", Message: "auth manager is nil"}
	}
	auth, ok := m.GetByID(strings.TrimSpace(authID))
	if !ok || auth == nil {
		return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: fmt.Sprintf("auth %q not found", authID), HTTPStatus: http.StatusNotFound}
	}
	provider := strings.TrimSpace(auth.Provider)
	executor, okExecutor := m.Executor(provider)
	if !okExecutor || executor == nil {
		return cliproxyexecutor.Response{}, &Error{Code: "executor_not_found", Message: fmt.Sprintf("no executor registered for provider %q", provider)}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = cliproxyexecutor.WithRequestProxyURL(ctx, opts.ProxyURL)
	execCtx := SetProviderAuthInContext(ctx, provider, auth.ID, auth.Label)
	if rt := m.roundTripperFor(auth); rt != nil {
		execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
		execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", rt)
	}
	execCtx = newUpstreamAttemptContext(execCtx)
	modelKey := m.selectionModelKeyForAuth(auth, req.Model)
	if modelKey == "" {
		modelKey = canonicalModelKey(req.Model)
	}

	prepared, errPrepare := m.prepareRequestAuth(execCtx, executor, auth)
	if errPrepare != nil {
		// A cancelled caller is not an account failure; keep cooldown state untouched.
		if errCancel := claudeOAuthRequestCancellation(execCtx, auth, errPrepare); errCancel != nil {
			return cliproxyexecutor.Response{}, errCancel
		}
		m.MarkResult(execCtx, Result{AuthID: auth.ID, Provider: provider, Model: modelKey, RouteModel: req.Model, Success: false, Error: resultErrorFromError(errPrepare), Options: opts, AccessToken: authAccessTokenForRequest(execCtx, auth, opts.Headers), CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch})
		return cliproxyexecutor.Response{}, errPrepare
	}
	auth = prepared
	executor = executorForAuth(executor, auth)

	executionSeq := m.nextExecutionSeq()
	resp, errExec := executor.Execute(execCtx, auth, req, opts)
	errExec = markUpstreamExecutionAttemptFromContext(execCtx, errExec)
	if errExec != nil && execCtx.Err() == nil {
		refreshCtx := newUpstreamAttemptContext(execCtx)
		if refreshed, okRefresh := m.tryRefreshAfterUnauthorized(refreshCtx, auth, errExec, false, opts.Headers); okRefresh {
			auth = refreshed
			execCtx = newUpstreamAttemptContext(execCtx)
			executionSeq = m.nextExecutionSeq()
			resp, errExec = executor.Execute(execCtx, auth, req, opts)
			errExec = markUpstreamExecutionAttemptFromContext(execCtx, errExec)
		}
	}

	if errExec != nil {
		if errCancel := claudeOAuthRequestCancellation(execCtx, auth, errExec); errCancel != nil {
			return cliproxyexecutor.Response{}, errCancel
		}
	}
	result := Result{AuthID: auth.ID, Provider: provider, Model: modelKey, RouteModel: req.Model, Success: errExec == nil, Options: opts, AccessToken: authAccessTokenForRequest(execCtx, auth, opts.Headers), CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch, ExecutionSeq: executionSeq}
	if errExec != nil {
		result.Error = resultErrorFromError(errExec)
		result.RetryAfter = retryAfterFromError(errExec)
		result.CredentialScope = isCredentialScopedError(errExec)
	}
	m.MarkResult(execCtx, result)
	return resp, unwrapUpstreamExecutionAttempt(errExec)
}
