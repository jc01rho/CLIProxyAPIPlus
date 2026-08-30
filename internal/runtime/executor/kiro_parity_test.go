package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type blockingReadCloser struct {
	closed chan struct{}
}

func (b *blockingReadCloser) Read([]byte) (int, error) {
	<-b.closed
	return 0, io.ErrClosedPipe
}

func (b *blockingReadCloser) Close() error {
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}
	return nil
}

func TestBuildKiroEndpointConfigsRuntimeCredentialUsesRuntimePrimary(t *testing.T) {
	t.Parallel()

	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token": "token",
			"auth_method":  "social",
			"api_region":   "eu-west-1",
		},
	}

	configs := buildKiroEndpointConfigsForAuth(auth)
	if len(configs) != 1 {
		t.Fatalf("endpoint configs = %#v, want runtime root only", configs)
	}
	if got, want := configs[0].Name, "KiroRuntime"; got != want {
		t.Fatalf("primary endpoint name = %q, want %q", got, want)
	}
	if got, want := configs[0].URL, "https://runtime.eu-west-1.kiro.dev/"; got != want {
		t.Fatalf("primary endpoint URL = %q, want %q", got, want)
	}
	if got, want := configs[0].Origin, "AI_EDITOR"; got != want {
		t.Fatalf("primary endpoint origin = %q, want %q", got, want)
	}
}

func TestBuildKiroEndpointConfigsBuilderIDUsesRuntimeRoot(t *testing.T) {
	t.Parallel()

	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token": "token",
			"auth_method":  "builder-id",
			"api_region":   "us-east-1",
		},
	}

	configs := buildKiroEndpointConfigsForAuth(auth)
	if len(configs) != 1 {
		t.Fatalf("endpoint configs = %#v, want only the runtime generation endpoint", configs)
	}
	if got, want := configs[0].Name, "KiroRuntime"; got != want {
		t.Fatalf("endpoint name = %q, want %q", got, want)
	}
	if got, want := configs[0].URL, "https://runtime.us-east-1.kiro.dev/"; got != want {
		t.Fatalf("endpoint URL = %q, want %q", got, want)
	}
	if got, want := configs[0].Origin, "AI_EDITOR"; got != want {
		t.Fatalf("endpoint origin = %q, want %q", got, want)
	}
}

func TestEffectiveProfileArnUsesBuilderIDRequestFallback(t *testing.T) {
	t.Parallel()

	builderID := &cliproxyauth.Auth{Metadata: map[string]any{
		"auth_method": "builder-id",
		"auth_type":   "aws_sso_oidc",
	}}
	if got := getEffectiveProfileArnWithWarning(builderID, ""); got != kiroBuilderIDProfileARN {
		t.Fatalf("Builder ID profile ARN = %q, want %q", got, kiroBuilderIDProfileARN)
	}

	social := &cliproxyauth.Auth{Metadata: map[string]any{"auth_method": "social"}}
	if got := getEffectiveProfileArnWithWarning(social, ""); got != "" {
		t.Fatalf("profileless social profile ARN = %q, want empty", got)
	}

	const ownProfile = "arn:aws:codewhisperer:eu-west-1:123456789012:profile/OWN"
	if got := getEffectiveProfileArnWithWarning(builderID, ownProfile); got != ownProfile {
		t.Fatalf("own profile ARN = %q, want %q", got, ownProfile)
	}
}

func TestNormalizeKiroStopReason(t *testing.T) {
	tests := []struct {
		reason     string
		hasText    bool
		hasToolUse bool
		want       string
		wantErr    bool
	}{
		{reason: "END_TURN", hasText: true, want: "end_turn"},
		{reason: "TOOL_USE", hasToolUse: true, want: "tool_use"},
		{reason: "TOOL_USE", wantErr: true},
		{reason: "MAX_TOKENS", want: "max_tokens"},
		{reason: "MAX_TOKEN", want: "max_tokens"},
		{reason: "LENGTH", want: "max_tokens"},
		{reason: "COMPLETE", hasText: true, want: "end_turn"},
		{reason: "MODEL_CONTEXT_WINDOW_EXCEEDED", wantErr: true},
		{reason: "CONTENT_FILTERED", want: "refusal"},
		{reason: "CONTENT_FILTER", want: "refusal"},
	}
	for _, tt := range tests {
		got, err := normalizeKiroStopReason(tt.reason, tt.hasText, tt.hasToolUse)
		if (err != nil) != tt.wantErr {
			t.Fatalf("normalizeKiroStopReason(%q) error = %v, wantErr=%v", tt.reason, err, tt.wantErr)
		}
		if got != tt.want {
			t.Fatalf("normalizeKiroStopReason(%q) = %q, want %q", tt.reason, got, tt.want)
		}
	}
}

func TestShouldRefreshKiroForbiddenUnlessSuspended(t *testing.T) {
	t.Parallel()

	if !shouldRefreshKiroForbidden([]byte(`{"message":"Access denied"}`)) {
		t.Fatal("ordinary 403 should refresh once")
	}
	if shouldRefreshKiroForbidden([]byte(`{"reason":"TEMPORARILY_SUSPENDED"}`)) {
		t.Fatal("suspension 403 must not refresh")
	}
}

func TestReadKiroFirstStreamBytePreservesBody(t *testing.T) {
	t.Parallel()

	reader, err := readKiroFirstStreamByte(context.Background(), io.NopCloser(bytes.NewBufferString("stream")), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "stream" {
		t.Fatalf("stream body = %q, want stream", got)
	}
}

func TestReadKiroFirstStreamByteRejectsEmptyStream(t *testing.T) {
	t.Parallel()

	_, err := readKiroFirstStreamByte(context.Background(), io.NopCloser(bytes.NewReader(nil)), time.Second)
	if err == nil {
		t.Fatal("expected empty stream error")
	}
}

func TestKiroTimedReadCloserBoundsInterChunkDelay(t *testing.T) {
	t.Parallel()

	reader := &kiroTimedReadCloser{
		body:    &blockingReadCloser{closed: make(chan struct{})},
		ctx:     context.Background(),
		timeout: 10 * time.Millisecond,
	}
	defer reader.Close()
	_, err := reader.Read(make([]byte, 1))
	if err == nil || !strings.Contains(err.Error(), "stream read timeout") {
		t.Fatalf("Read error = %v, want stream read timeout", err)
	}
}

func TestApplyKiroGenerationHeadersMatchesCLI2191(t *testing.T) {
	t.Parallel()

	req, err := http.NewRequest(http.MethodPost, "https://runtime.us-east-1.kiro.dev/", nil)
	if err != nil {
		t.Fatal(err)
	}
	applyDynamicFingerprint(req, &cliproxyauth.Auth{ID: "auth"})

	if got := req.Header.Get("User-Agent"); got != kiroCLIUserAgent {
		t.Fatalf("User-Agent = %q, want %q", got, kiroCLIUserAgent)
	}
	if got := req.Header.Get("x-amz-user-agent"); got != kiroCLIAmzUserAgent {
		t.Fatalf("x-amz-user-agent = %q, want %q", got, kiroCLIAmzUserAgent)
	}
	if got := req.Header.Get("x-kiro-attempt"); got != "1;max=3" {
		t.Fatalf("x-kiro-attempt = %q", got)
	}
	if got := req.Header.Get("x-amzn-kiro-agent-mode"); got != "" {
		t.Fatalf("obsolete x-amzn-kiro-agent-mode remained: %q", got)
	}
	if kiroContentType != "application/x-amz-json-1.0" {
		t.Fatalf("kiroContentType = %q", kiroContentType)
	}
}

func TestResolveKiroAPIRegionValidatesEnvironment(t *testing.T) {
	t.Setenv("KIRO_API_REGION", "bad.example.com/path")
	auth := &cliproxyauth.Auth{Metadata: map[string]any{
		"auth_method": "social",
		"api_region":  "eu-west-1",
	}}
	if got := resolveKiroAPIRegion(auth); got != "eu-west-1" {
		t.Fatalf("invalid KIRO_API_REGION fallback = %q, want eu-west-1", got)
	}

	t.Setenv("KIRO_API_REGION", "ap-northeast-1")
	if got := resolveKiroAPIRegion(auth); got != "ap-northeast-1" {
		t.Fatalf("valid KIRO_API_REGION = %q, want ap-northeast-1", got)
	}
}

func TestSleepKiroRetryHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	err := sleepKiroRetry(ctx, time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("sleepKiroRetry error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("cancelled retry sleep took %v", elapsed)
	}
}
