package keeperexport

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// TestBoundedRuneSafeNoSplitUTF8 is the failing-first gate for the UTF-8
// byte-slice bug. With auth_index = strings.Repeat("a",127)+"🙂" the
// projection must produce a valid UTF-8 source whose byte length is <= 128
// without splitting the smile emoji across two bytes.
func TestBoundedRuneSafeNoSplitUTF8(t *testing.T) {
	// "🙂" is 4 bytes in UTF-8 (U+1F642). 127 bytes of ASCII + 4-byte emoji
	// would naively be sliced to 128 bytes, sanitizing the trailing byte and
	// producing an invalid UTF-8 sequence. The bounded() helper must instead
	// drop trailing bytes until the next valid code-point boundary.
	authIndex := strings.Repeat("a", 127) + "🙂" // 131 bytes, 128 runes
	if bytes := len(authIndex); bytes != 131 {
		t.Fatalf("auth index byte length = %d, want 131", bytes)
	}
	if !utf8.ValidString(authIndex) {
		t.Fatalf("auth index must be valid UTF-8 before projection")
	}

	record := coreusage.Record{
		Provider:     "openai-compatible-task10-upstream",
		ExecutorType: "OpenAICompatExecutor",
		Model:        "task10-model",
		Alias:        "task10-model",
		APIKey:       "sk-task10-shared-client-key-0123456789abcdef",
		AuthIndex:    authIndex,
		AuthType:     "apikey",
		Source:       "shared-display",
		RequestedAt:  time.Now().UTC(),
		Detail:       coreusage.Detail{InputTokens: 11, OutputTokens: 7, TotalTokens: 18},
		Generate:     coreusage.GenerateFlag(true),
	}
	privacy := config.UsageExportPrivacyConfig{}
	ctx := internallogging.WithRequestID(context.Background(), "utf8-bounded-test")

	body, err := ProjectUsage(ctx, record, []byte("01234567890123456789012345678901"), privacy)
	if err != nil {
		t.Fatalf("ProjectUsage failed: %v", err)
	}

	// Round trip through the strict decoder to confirm the wire payload is
	// valid and the source field is bounded, valid UTF-8, and <= 128 bytes.
	var raw struct {
		Source    string `json:"source"`
		AuthIndex string `json:"auth_index"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("projected payload is not valid JSON: %v\n%s", err, body)
	}
	if raw.RequestID != "utf8-bounded-test" {
		t.Fatalf("request_id = %q, want utf8-bounded-test", raw.RequestID)
	}
	if !utf8.ValidString(raw.Source) {
		t.Fatalf("source field is not valid UTF-8: %q (bytes=%d)", raw.Source, len(raw.Source))
	}
	if len(raw.Source) > 128 {
		t.Fatalf("source byte length = %d, must be <= 128", len(raw.Source))
	}
	// The slice must end on a rune boundary, so the JSON-encoded string must
	// also be valid UTF-8 and pass the strict decoder.
	if _, perr := decodeUsagePayload(body); perr != nil {
		t.Fatalf("projected payload rejected by strict decoder: %v", perr)
	}
}

// TestBoundedRuneSafeAcrossAllProjections ensures every per-string bounded
// projection that accepts user-influenced input stays valid UTF-8 and within
// the frozen byte cap.
func TestBoundedRuneSafeAcrossAllProjections(t *testing.T) {
	// Build a long, valid UTF-8 string that exceeds 128 bytes: many emoji
	// mixed with ASCII so any byte-slice naive truncation would split a rune.
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString("a")
		b.WriteString("🙂")
	}
	overlong := b.String() // 600 bytes, 200 emoji + 200 ASCII runes
	if !utf8.ValidString(overlong) {
		t.Fatalf("overlong fixture must be valid UTF-8")
	}
	if len(overlong) <= 128 {
		t.Fatalf("overlong fixture too short: %d", len(overlong))
	}

	record := coreusage.Record{
		Provider:     overlong,
		ExecutorType: overlong,
		Model:        overlong,
		Alias:        overlong,
		APIKey:       "sk-task10-shared-client-key-0123456789abcdef",
		AuthIndex:    overlong,
		AuthType:     overlong,
		Source:       overlong,
		RequestedAt:  time.Now().UTC(),
		Detail:       coreusage.Detail{InputTokens: 11, OutputTokens: 7, TotalTokens: 18},
		Generate:     coreusage.GenerateFlag(true),
	}
	privacy := config.UsageExportPrivacyConfig{}
	ctx := internallogging.WithRequestID(context.Background(), "utf8-bounded-all")

	body, err := ProjectUsage(ctx, record, []byte("01234567890123456789012345678901"), privacy)
	if err != nil {
		t.Fatalf("ProjectUsage failed: %v", err)
	}
	// Bound the encoded payload length as a sanity check (projected source 128,
	// auth_index 256, model 128, etc, must all stay valid UTF-8).
	if len(body) > MaxPayloadBytes {
		t.Fatalf("projection produced %d bytes, exceeds MaxPayloadBytes=%d", len(body), MaxPayloadBytes)
	}
	if _, perr := decodeUsagePayload(body); perr != nil {
		t.Fatalf("strict decoder rejected payload: %v\n%s", perr, body)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("payload not valid JSON: %v\n%s", err, body)
	}
	for _, cap := range []struct {
		key string
		max int
	}{
		// Stable contract caps for user-influenced projection strings.
		{"source", 128},
		{"auth_index", 256},
		{"model", 128},
		{"alias", 128},
		{"executor_type", 128},
		{"auth_type", 128},
		{"provider", 128},
		{"endpoint", 128},
		{"reasoning_effort", 64},
		{"service_tier", 64},
		{"request_id", 256},
	} {
		v, ok := raw[cap.key].(string)
		if !ok {
			t.Fatalf("field %q missing or not a string", cap.key)
		}
		if !utf8.ValidString(v) {
			t.Fatalf("field %q is not valid UTF-8: %q (bytes=%d)", cap.key, v, len(v))
		}
		if len(v) > cap.max {
			t.Fatalf("field %q byte length = %d, cap = %d", cap.key, len(v), cap.max)
		}
	}
}

// TestBoundedCapExactBoundary is the contract gate: a 128-byte exact input
// must project with byte length exactly 128, and a 129-byte input must
// project to exactly 128 bytes (no overshoot, no undershoot, no split).
func TestBoundedCapExactBoundary(t *testing.T) {
	ascii127 := strings.Repeat("a", 127)
	ascii128 := strings.Repeat("a", 128)
	ascii129 := strings.Repeat("a", 129)
	for _, in := range []string{ascii127, ascii128, ascii129} {
		out := bounded(in, 128)
		if len(out) > 128 {
			t.Fatalf("bounded(%q) returned %d bytes, want <= 128", in, len(out))
		}
		if !utf8.ValidString(out) {
			t.Fatalf("bounded(%q) returned invalid UTF-8", in)
		}
	}
	if got := bounded(ascii128, 128); len(got) != 128 {
		t.Fatalf("bounded(128 ascii) length = %d, want 128", len(got))
	}
	if got := bounded(ascii129, 128); len(got) != 128 {
		t.Fatalf("bounded(129 ascii) length = %d, want 128", len(got))
	}
	// 4-byte emoji at position 128: naive byte slice would split.
	emoji := strings.Repeat("a", 127) + "🙂" // 131 bytes
	out := bounded(emoji, 128)
	if !utf8.ValidString(out) {
		t.Fatalf("bounded(127a+🙂) returned invalid UTF-8: %q (bytes=%d)", out, len(out))
	}
	if len(out) > 128 {
		t.Fatalf("bounded(127a+🙂) returned %d bytes, want <= 128", len(out))
	}
}
