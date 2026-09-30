package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestParseKiroEventStreamRecoversSplitJSONAndPreservesSignatureUsageAndCredits(t *testing.T) {
	t.Parallel()

	stream := bytes.NewBufferString(`junk{"reasoningContentEvent":{"text":"think","signature":"opaque-signature"}}{"messageMetadataEvent":{"tokenUsage":{"uncachedInputTokens":7,"cacheReadInputTokens":3,"cacheWriteInputTokens":2,"outputTokens":5}}}{"meteringEvent":{"unit":"credit","usage":1.25}}{"meteringEvent":{"unit":"credits","usage":0.75}}{"assistantResponseEvent":{"content":"answer"}}{"messageStopEvent":{"stopReason":"END_TURN"}}`)
	content, signatures, _, usageInfo, stopReason, credits, err := NewKiroExecutor(&config.Config{}).parseEventStream(stream, "kiro-claude-sonnet-4-5")
	if err != nil {
		t.Fatal(err)
	}
	if content != "<thinking>think</thinking>answer" {
		t.Fatalf("content = %q", content)
	}
	if len(signatures) != 1 || signatures[0] != "opaque-signature" {
		t.Fatalf("signatures = %#v", signatures)
	}
	if usageInfo.CacheReadTokens != 3 || usageInfo.CacheCreationTokens != 2 {
		t.Fatalf("cache usage = %#v", usageInfo)
	}
	if credits != 2 {
		t.Fatalf("credits = %v, want 2", credits)
	}
	if stopReason != "end_turn" {
		t.Fatalf("stop reason = %q", stopReason)
	}
}

func TestKiroMachineIDPersistsAcrossRefreshTokenRotation(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "kiro.json")
	if err := os.WriteFile(path, []byte(`{"access_token":"access","refresh_token":"first"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	executor := NewKiroExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "account", Attributes: map[string]string{"path": path}, Metadata: map[string]any{"access_token": "access", "refresh_token": "first"}}
	first, err := executor.withKiroMachineID(auth)
	if err != nil {
		t.Fatal(err)
	}
	rotated := &cliproxyauth.Auth{ID: "account", Attributes: map[string]string{"path": path}, Metadata: map[string]any{"access_token": "new-access", "refresh_token": "rotated"}}
	second, err := executor.withKiroMachineID(rotated)
	if err != nil {
		t.Fatal(err)
	}
	if first.Metadata["kiro_machine_id"] != second.Metadata["kiro_machine_id"] {
		t.Fatalf("persisted machine IDs differ: %q != %q", first.Metadata["kiro_machine_id"], second.Metadata["kiro_machine_id"])
	}
}

func TestKiroCreditsAreAddedToUsageAndReported(t *testing.T) {
	t.Parallel()

	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	recordKiroCredits(ctx, 2.5)
	if got := internallogging.GetResponseHeaders(ctx).Get("X-Kiro-Credits-Used"); got != "2.5" {
		t.Fatalf("reported credits = %q", got)
	}
	got := addKiroCreditsToResponse([]byte(`data: {"usage":{"input_tokens":1}}`), 2.5)
	if !bytes.Contains(got, []byte(`"credits_used":2.5`)) {
		t.Fatalf("response credits = %s", got)
	}
}

func TestEnsureKiroMachineIDSurvivesRefreshTokenRotation(t *testing.T) {
	t.Parallel()

	auth := &cliproxyauth.Auth{ID: "account", Metadata: map[string]any{"refresh_token": "first"}}
	first := ensureKiroMachineID(auth)
	auth.Metadata["refresh_token"] = "rotated"
	if got := ensureKiroMachineID(auth); got != first {
		t.Fatalf("machine ID changed on refresh rotation: %q != %q", got, first)
	}
	if got, _ := auth.Metadata["kiro_machine_id"].(string); got != first {
		t.Fatalf("metadata machine ID = %q, want %q", got, first)
	}
}

type kiroModelRoundTripper struct{}

func (kiroModelRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != "management.us-east-1.kiro.dev" {
		return nil, io.ErrUnexpectedEOF
	}
	if req.Header.Get("X-Amz-Target") != "KiroControlPlaneBearerService.ListAvailableModels" {
		return nil, io.ErrUnexpectedEOF
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewBufferString(`{"models":[{"modelId":"claude-sonnet-4.5","modelName":"Sonnet","tokenLimits":{"maxInputTokens":123}}]}`)),
	}, nil
}

func TestFetchKiroModelsUsesManagementForSocialAccounts(t *testing.T) {
	t.Parallel()

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(kiroModelRoundTripper{}))
	auth := &cliproxyauth.Auth{Metadata: map[string]any{
		"access_token": "token", "auth_method": "google", "api_region": "us-east-1",
	}}
	models := FetchKiroModels(ctx, auth, &config.Config{})
	if len(models) != 2 || models[0].ID != "kiro-claude-sonnet-4-5" || models[1].ID != "kiro-claude-sonnet-4-5-agentic" {
		t.Fatalf("models = %#v", models)
	}
}
