package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/zcode"
)

// fakeZcodeFlow drives runZcodeLogin without touching the broker.
type fakeZcodeFlow struct {
	startErr  error
	flow      *zcode.CLIFlow
	polls     []*zcode.CLIPollResult
	pollErr   error
	pollCount int

	provisionErr error
	provisions   int

	exchangeErr error
	exchanges   int
}

func (f *fakeZcodeFlow) StartCLIFlow(context.Context) (*zcode.CLIFlow, error) {
	if f.startErr != nil {
		return nil, f.startErr
	}
	if f.flow != nil {
		return f.flow, nil
	}
	return &zcode.CLIFlow{
		FlowID:          "flow-1",
		AuthorizeURL:    "https://zcode.z.ai/authorize?flow=1",
		PollToken:       "poll-token",
		PollIntervalSec: 1,
		ExpiresAt:       time.Now().Add(time.Minute),
	}, nil
}

func (f *fakeZcodeFlow) PollCLIFlow(context.Context, string, string) (*zcode.CLIPollResult, error) {
	f.pollCount++
	if f.pollErr != nil {
		return nil, f.pollErr
	}
	if len(f.polls) == 0 {
		return &zcode.CLIPollResult{Status: "pending"}, nil
	}
	result := f.polls[0]
	if len(f.polls) > 1 {
		f.polls = f.polls[1:]
	}
	return result, nil
}

func (f *fakeZcodeFlow) ProvisionFromUpstream(_ context.Context, upstream, token string) (*zcode.Credentials, error) {
	f.provisions++
	if f.provisionErr != nil {
		return nil, f.provisionErr
	}
	return &zcode.Credentials{AccessToken: upstream, ZcodeToken: token, Email: "user@example.com"}, nil
}

func (f *fakeZcodeFlow) GenerateAuthURL(state, _ string) string {
	return "https://zcode.z.ai/authorize?state=" + state
}

func (f *fakeZcodeFlow) ExchangeCode(context.Context, string, string, string) (*zcode.Credentials, error) {
	f.exchanges++
	if f.exchangeErr != nil {
		return nil, f.exchangeErr
	}
	return &zcode.Credentials{AccessToken: "pasted-key", Email: "user@example.com"}, nil
}

// TestRunZcodeLoginUsesDeviceFlow verifies the broker device flow is the
// default path: a ready poll provisions the upstream token and never prompts.
func TestRunZcodeLoginUsesDeviceFlow(t *testing.T) {
	flow := &fakeZcodeFlow{polls: []*zcode.CLIPollResult{{
		Status:         "ready",
		ZaiAccessToken: "zai-access",
		ZcodeToken:     "broker-jwt",
	}}}
	creds, err := runZcodeLogin(context.Background(), flow, &LoginOptions{NoBrowser: true})
	if err != nil {
		t.Fatalf("runZcodeLogin() error = %v", err)
	}
	if creds.AccessToken != "zai-access" || creds.ZcodeToken != "broker-jwt" {
		t.Errorf("creds = %+v, want the provisioned upstream token", creds)
	}
	if flow.provisions != 1 {
		t.Errorf("provisions = %d, want 1", flow.provisions)
	}
	if flow.exchanges != 0 {
		t.Errorf("exchanges = %d, want 0 (paste flow must not run)", flow.exchanges)
	}
}

// TestRunZcodeLoginRejectedFlowDoesNotFallBack verifies a broker rejection is
// terminal: the paste flow would otherwise re-prompt after the user refused.
func TestRunZcodeLoginRejectedFlowDoesNotFallBack(t *testing.T) {
	flow := &fakeZcodeFlow{polls: []*zcode.CLIPollResult{{Status: "failed"}}}
	_, err := runZcodeLogin(context.Background(), flow, &LoginOptions{NoBrowser: true})
	if err == nil {
		t.Fatal("runZcodeLogin() error = nil, want a rejection error")
	}
	if flow.exchanges != 0 {
		t.Errorf("exchanges = %d, want 0 (no paste fallback after a rejection)", flow.exchanges)
	}
}

// TestRunZcodeLoginProvisionError verifies provisioning failures surface.
func TestRunZcodeLoginProvisionError(t *testing.T) {
	flow := &fakeZcodeFlow{
		polls:        []*zcode.CLIPollResult{{Status: "ready", ZaiAccessToken: "zai-access"}},
		provisionErr: errors.New("broker down"),
	}
	_, err := runZcodeLogin(context.Background(), flow, &LoginOptions{NoBrowser: true})
	if err == nil || !strings.Contains(err.Error(), "provision failed") {
		t.Fatalf("runZcodeLogin() error = %v, want a provision failure", err)
	}
}

// TestRunZcodeLoginFallsBackToPaste verifies an unavailable broker degrades to
// the manual paste flow instead of failing the login.
func TestRunZcodeLoginFallsBackToPaste(t *testing.T) {
	flow := &fakeZcodeFlow{startErr: errors.New("broker unreachable")}
	creds, err := runZcodeLogin(context.Background(), flow, &LoginOptions{
		NoBrowser: true,
		Prompt:    func(string) (string, error) { return "authorization-code", nil },
	})
	if err != nil {
		t.Fatalf("runZcodeLogin() error = %v", err)
	}
	if creds.AccessToken != "pasted-key" {
		t.Errorf("creds.AccessToken = %q, want the pasted-code exchange result", creds.AccessToken)
	}
	if flow.exchanges != 1 || flow.pollCount != 0 {
		t.Errorf("exchanges/polls = %d/%d, want 1/0", flow.exchanges, flow.pollCount)
	}
}

// TestRunZcodeLoginHonoursBrokerDeadline verifies an already-expired flow is
// rejected before any poll (no infinite wait when the broker says it is over).
func TestRunZcodeLoginHonoursBrokerDeadline(t *testing.T) {
	flow := &fakeZcodeFlow{flow: &zcode.CLIFlow{
		FlowID:          "flow-1",
		AuthorizeURL:    "https://zcode.z.ai/authorize?flow=1",
		PollToken:       "poll-token",
		PollIntervalSec: 1,
		ExpiresAt:       time.Now().Add(-time.Second),
	}}
	_, err := runZcodeLogin(context.Background(), flow, &LoginOptions{NoBrowser: true})
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("runZcodeLogin() error = %v, want an expiry error", err)
	}
	if flow.pollCount != 0 {
		t.Errorf("polls = %d, want 0 (expired flow must not poll)", flow.pollCount)
	}
}

// TestRunZcodeLoginStopsOnContextCancellation verifies a cancelled login does
// not fall back to the interactive paste prompt.
func TestRunZcodeLoginStopsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	flow := &fakeZcodeFlow{startErr: context.Canceled}
	_, err := runZcodeLogin(ctx, flow, &LoginOptions{
		NoBrowser: true,
		Prompt:    func(string) (string, error) { t.Fatal("prompt must not run"); return "", nil },
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runZcodeLogin() error = %v, want context.Canceled", err)
	}
}
