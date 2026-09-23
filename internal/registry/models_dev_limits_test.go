package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRefreshModelsDevLimitsReplacesSnapshotOnEveryCall(t *testing.T) {
	previous := modelsDevLimits.Load()
	t.Cleanup(func() { modelsDevLimits.Store(previous) })

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch requests.Add(1) {
		case 1:
			_, _ = w.Write([]byte(`{"openai/gpt-6-sol":{"limit":{"context":1050000,"input":922000,"output":128000}}}`))
		case 2:
			_, _ = w.Write([]byte(`{"openai/gpt-6-sol":{"limit":{"context":1100000,"output":128000}}}`))
		default:
			_, _ = w.Write([]byte(`not JSON`))
		}
	}))
	defer server.Close()

	for _, want := range []int{1050000, 1100000} {
		if err := RefreshModelsDevLimits(context.Background(), server.URL); err != nil {
			t.Fatalf("refresh models.dev: %v", err)
		}
		got, ok := LookupModelsDevLimit("gpt-6-sol")
		if !ok || got.Context != want || got.Output != 128000 {
			t.Fatalf("models.dev limit = (%+v, %v), want %d / 128000", got, ok, want)
		}
	}
	if requests.Load() != 2 {
		t.Fatalf("models.dev requests = %d, want 2", requests.Load())
	}

	if err := RefreshModelsDevLimits(context.Background(), server.URL); err == nil {
		t.Fatal("invalid catalog refresh succeeded")
	}
	if got, _ := LookupModelsDevLimit("gpt-6-sol"); got.Context != 1100000 {
		t.Fatalf("invalid refresh changed existing limit: %+v", got)
	}
}

func TestParseModelsDevLimitsMatchesOnlyUnambiguousIDs(t *testing.T) {
	catalog, err := parseModelsDevLimits([]byte(`{
		"openai/gpt-6-sol":{"limit":{"context":1050000,"input":922000,"output":128000}},
		"anthropic/claude-opus-5-5":{"limit":{"context":1000000,"output":128000}},
		"openai/shared":{"limit":{"context":1000000,"output":10000}},
		"other/shared":{"limit":{"context":200000,"output":10000}},
		"other/unknown":{"limit":{}}
	}`))
	if err != nil {
		t.Fatalf("parse models.dev: %v", err)
	}
	if got := catalog.byModelID["gpt-6-sol"]; got.Context != 1050000 || got.Input != 922000 {
		t.Fatalf("gpt-6-sol = %+v", got)
	}
	if got := catalog.byModelID["claude-opus-5-5"]; got.Context != 1000000 || got.Output != 128000 {
		t.Fatalf("claude-opus-5-5 = %+v", got)
	}
	if _, ok := catalog.byModelID["shared"]; ok {
		t.Fatal("ambiguous unqualified ID matched")
	}
	if got := catalog.exact["other/shared"]; got.Context != 200000 {
		t.Fatalf("qualified ID = %+v", got)
	}
	if _, ok := catalog.byModelID["unknown"]; ok {
		t.Fatal("model without limits matched")
	}
}

func TestRefreshModelsDevLimitsRejectsHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	err := RefreshModelsDevLimits(context.Background(), server.URL)
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("refresh error = %v, want HTTP 503", err)
	}
}

func TestApplyModelsDevLimitDoesNotChangeRegisteredModel(t *testing.T) {
	previous := modelsDevLimits.Load()
	t.Cleanup(func() { modelsDevLimits.Store(previous) })
	catalog, err := parseModelsDevLimits([]byte(`{"openai/gpt-6-sol":{"limit":{"context":1050000,"output":128000}}}`))
	if err != nil {
		t.Fatalf("parse models.dev: %v", err)
	}
	modelsDevLimits.Store(catalog)

	registered := &ModelInfo{ID: "gpt-6-sol", ContextLength: 272000, MaxCompletionTokens: 64000}
	listed := cloneModelInfo(registered)
	ApplyModelsDevLimit(listed)
	if listed.ContextLength != 1050000 || listed.MaxContextLength != 1050000 || listed.MaxCompletionTokens != 128000 {
		t.Fatalf("listed limits = %d/%d/%d", listed.ContextLength, listed.MaxContextLength, listed.MaxCompletionTokens)
	}
	if registered.ContextLength != 272000 || registered.MaxCompletionTokens != 64000 {
		t.Fatalf("registered limits changed: %+v", registered)
	}
	alias := &ModelInfo{ID: "sol-alias", MetadataModelID: "gpt-6-sol"}
	ApplyModelsDevLimit(alias)
	if alias.ContextLength != 1050000 || alias.MaxCompletionTokens != 128000 {
		t.Fatalf("explicit alias limits = %+v", alias)
	}
}
