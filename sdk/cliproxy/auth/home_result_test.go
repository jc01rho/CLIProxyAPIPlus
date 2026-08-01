package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

type homeResultCapturePlugin struct {
	records chan coreusage.Record
}

func (p homeResultCapturePlugin) HandleUsage(_ context.Context, record coreusage.Record) {
	if p.records != nil {
		p.records <- record
	}
}

func TestReportHomeUnauthorizedPublishesTokenVersionedFailure(t *testing.T) {
	records := make(chan coreusage.Record, 8)
	const pluginName = "auth-home-result-test"
	coreusage.RegisterNamedPlugin(pluginName, homeResultCapturePlugin{records: records})
	t.Cleanup(func() {
		coreusage.RegisterNamedPlugin(pluginName, homeResultCapturePlugin{})
	})

	auth := &Auth{
		ID:         "home-result-auth",
		Index:      "home-result-index",
		Provider:   "codex",
		Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
		Metadata: map[string]any{
			"token": map[string]any{"accessToken": " current-access-token "},
		},
	}
	ctx := coreusage.WithRequestedModelAlias(context.Background(), "client-model")
	NewManager(nil, nil, nil).ReportHomeUnauthorized(ctx, auth, "codex", "upstream-model")

	deadline := time.After(time.Second)
	for {
		select {
		case record := <-records:
			if record.AuthID != auth.ID {
				continue
			}
			if !record.Failed || record.Fail.StatusCode != http.StatusUnauthorized {
				t.Fatalf("failure = %#v, want 401", record.Fail)
			}
			if record.AuthIndex != auth.Index {
				t.Fatalf("auth index = %q, want %q", record.AuthIndex, auth.Index)
			}
			if record.AccessTokenSHA256 != AccessTokenSHA256(auth) || record.AccessTokenSHA256 == "" {
				t.Fatalf("access token fingerprint = %q", record.AccessTokenSHA256)
			}
			if record.Model != "upstream-model" || record.Alias != "client-model" {
				t.Fatalf("model/alias = %q/%q", record.Model, record.Alias)
			}
			if coreusage.GenerateEnabled(record.Generate) {
				t.Fatal("result-only unauthorized record was marked as generation")
			}
			if record.Detail.TotalTokens != 0 {
				t.Fatalf("result-only tokens = %d, want 0", record.Detail.TotalTokens)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for Home unauthorized usage record")
		}
	}
}

func TestReportHomeUnauthorizedRequiresTokenFingerprint(t *testing.T) {
	records := make(chan coreusage.Record, 1)
	const pluginName = "auth-home-result-empty-token-test"
	coreusage.RegisterNamedPlugin(pluginName, homeResultCapturePlugin{records: records})
	t.Cleanup(func() {
		coreusage.RegisterNamedPlugin(pluginName, homeResultCapturePlugin{})
	})

	NewManager(nil, nil, nil).ReportHomeUnauthorized(context.Background(), &Auth{
		ID:       "home-result-no-token",
		Index:    "home-result-no-token",
		Provider: "codex",
	}, "codex", "model")

	timer := time.NewTimer(50 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case record := <-records:
			if record.AuthID == "home-result-no-token" {
				t.Fatalf("unexpected usage record without token fingerprint: %#v", record)
			}
		case <-timer.C:
			return
		}
	}
}
