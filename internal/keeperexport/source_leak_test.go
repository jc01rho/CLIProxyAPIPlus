package keeperexport_test

import (
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/keeperexport"
	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// secretSourceKey simulates a legacy resolveUsageSource fallback that placed a
// raw provider/client API key into Record.Source (the Task 10 leak path).
const (
	secretSourceKey = "sk-task10-shared-provider-key-0123456789abcdef"
	clientAPIKey    = "sk-task10-shared-client-key-abcdef0123456789"
)

// TestProjectUsageNeverEmitsRawKeyInSource is the Task 10 secret-leak gate at
// the CPA projection seam. A record whose legacy Source equals a raw API key
// must project to a bounded CPA-local NON-SECRET stable source identifier; the
// raw key must never appear in the wire payload, and the fingerprint field
// remains the only API-key identity on the wire.
func TestProjectUsageNeverEmitsRawKeyInSource(t *testing.T) {
	record := coreusage.Record{
		Provider:     "openai",
		ExecutorType: "OpenAICompatExecutor",
		Model:        "task10-model",
		Alias:        "task10-model",
		APIKey:       clientAPIKey,
		AuthIndex:    "auth-stable-index",
		AuthType:     "apikey",
		Source:       secretSourceKey, // legacy raw-key fallback leaks here
		Generate:     coreusage.GenerateFlag(true),
		Detail: coreusage.Detail{
			InputTokens:    10,
			OutputTokens:   4,
			TotalTokens:    18,
			TokenBreakdown: coreusage.TokenBreakdown{},
		},
	}
	privacy := config.UsageExportPrivacyConfig{}
	ctx := internallogging.WithRequestID(context.Background(), "req-task10-leak-test")
	body, err := keeperexport.ProjectUsage(ctx, record, []byte("01234567890123456789012345678901"), privacy)
	if err != nil {
		t.Fatalf("ProjectUsage failed: %v", err)
	}

	if strings.Contains(string(body), secretSourceKey) || strings.Contains(string(body), clientAPIKey) {
		t.Fatalf("raw API key leaked into projected usage payload: %s", body)
	}

	// The projection must remain valid against the strict wire decoder.
	batch, perr := keeperexport.DecodeUsageBatch(buildBatchEnvelope(t, body))
	if perr != nil {
		t.Fatalf("projected payload rejected by strict decoder: %v", perr)
	}
	payload := batch.Events[0].Payload
	if payload.Source == secretSourceKey {
		t.Fatalf("source field still holds the raw API key: %q", payload.Source)
	}
	if strings.Contains(payload.Source, "sk-") {
		t.Fatalf("source field contains a raw-key-looking secret: %q", payload.Source)
	}
	if payload.APIKeyFingerprint == nil {
		t.Fatalf("api_key_fingerprint must be the wire API-key identity")
	}
	if payload.Source != record.AuthIndex {
		t.Fatalf("source = %q, want stable non-secret auth index %q", payload.Source, record.AuthIndex)
	}
}

func buildBatchEnvelope(t *testing.T, payload []byte) []byte {
	t.Helper()
	return []byte(`{"protocolVersion":"keeper-export/v1","streamId":"` + fixtureStreamID + `","events":[{"sequence":1,"payload":` + string(payload) + `}]}`)
}
