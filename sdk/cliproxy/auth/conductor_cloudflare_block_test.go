package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const cloudflareEdgeBlockPage = `<!doctype html> <!--[if lt IE 7]> <html class="no-js ie6 oldie" lang="en-US"> <![endif]--> <html class="no-js" lang="en-US"> <head> <title>Access denied | ds-api.example.net used Cloudflare to restrict access | Cloudflare</title> </head> <body><div id="cf-error-details"><span>1010</span> <a href="https://developers.cloudflare.com/support/troubleshooting/http-status-codes/cloudflare-1xxx-errors/error-1010/">Error 1010</a></div></body></html>`

type cloudflareBlockExecutor struct {
	identifier string
	blockedID  string
	mu         sync.Mutex
	calls      []string
}

func (e *cloudflareBlockExecutor) Identifier() string { return e.identifier }

func (e *cloudflareBlockExecutor) fail(auth *Auth) error {
	e.mu.Lock()
	e.calls = append(e.calls, auth.ID)
	e.mu.Unlock()
	if auth.ID == e.blockedID {
		return &Error{HTTPStatus: http.StatusForbidden, Message: cloudflareEdgeBlockPage}
	}
	return &Error{HTTPStatus: http.StatusInternalServerError, Message: "cloudflare-block test failure"}
}

func (e *cloudflareBlockExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, e.fail(auth)
}

func (e *cloudflareBlockExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, e.fail(auth)
}

func (*cloudflareBlockExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *cloudflareBlockExecutor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, e.fail(auth)
}

func (*cloudflareBlockExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *cloudflareBlockExecutor) callCounts() map[string]int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return countRetryRoundIDs(e.calls)
}

func TestIsCloudflareChallengeErrorMessage_EdgeAccessDeniedPage(t *testing.T) {
	if !isCloudflareChallengeErrorMessage(cloudflareEdgeBlockPage) {
		t.Fatal("expected Cloudflare edge access-denied page to be classified as a block")
	}
	if isCloudflareChallengeErrorMessage(`{"error":{"message":"forbidden","type":"invalid_request_error"}}`) {
		t.Fatal("expected a plain JSON 403 not to be classified as a Cloudflare block")
	}
}

func TestExecuteCloudflareBlockNeverRetriesBlockedCredential(t *testing.T) {
	tests := []struct {
		name   string
		invoke func(*Manager, cliproxyexecutor.Request) error
	}{
		{
			name: "non-stream",
			invoke: func(manager *Manager, req cliproxyexecutor.Request) error {
				_, err := manager.Execute(context.Background(), []string{"cloudflare-block-test"}, req, cliproxyexecutor.Options{})
				return err
			},
		},
		{
			name: "count-tokens",
			invoke: func(manager *Manager, req cliproxyexecutor.Request) error {
				_, err := manager.ExecuteCount(context.Background(), []string{"cloudflare-block-test"}, req, cliproxyexecutor.Options{})
				return err
			},
		},
		{
			name: "stream",
			invoke: func(manager *Manager, req cliproxyexecutor.Request) error {
				_, err := manager.ExecuteStream(context.Background(), []string{"cloudflare-block-test"}, req, cliproxyexecutor.Options{Stream: true})
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			manager.SetRetryConfig(3, 0, 0)
			executor := &cloudflareBlockExecutor{identifier: "cloudflare-block-test", blockedID: "cloudflare-block-a"}
			manager.RegisterExecutor(executor)
			registerRetryRoundLocalAuths(t, manager, "cloudflare-block-test", "cloudflare-block-model", map[string]int{
				"cloudflare-block-a": 3,
				"cloudflare-block-b": 3,
			})

			if err := test.invoke(manager, cliproxyexecutor.Request{Model: "cloudflare-block-model"}); err == nil {
				t.Fatal("execution error = nil, want terminal error")
			}
			counts := executor.callCounts()
			if counts["cloudflare-block-a"] != 1 {
				t.Fatalf("blocked credential calls = %d, want 1 (no re-request); counts=%v", counts["cloudflare-block-a"], counts)
			}
			if counts["cloudflare-block-b"] < 2 {
				t.Fatalf("fallback credential calls = %d, want it to keep serving retry rounds; counts=%v", counts["cloudflare-block-b"], counts)
			}
		})
	}
}
