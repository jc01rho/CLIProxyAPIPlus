package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// A config-declared alias (`models: [{name: <upstream>, alias: <exposed>}]`) is
// registered under the alias ID. The dispatcher's resolveRequestedModelForAuth
// only rewrites a requested model back to its upstream name when the registered
// entry carries ExecutionTarget; with an empty ExecutionTarget it treats the
// entry as a real registered model and ships the bare alias upstream.
//
// The motivating failure: a claude-api-key entry declaring
// `{name: claude-fable-5-1, alias: fable}` exposed "fable" in /v1/models, but
// calling it sent the literal "fable" to the gateway. Because that gateway does
// not 404 on unknown models, the request silently fell through and returned an
// unrelated model on every call instead of Claude Fable 5.1.
func TestBuildClaudeConfigModels_AliasExposesExecutionTarget(t *testing.T) {
	entry := &config.ClaudeKey{
		APIKey:  "sk-test",
		BaseURL: "https://gateway.example",
		Models: []config.ClaudeModel{
			{Name: "claude-fable-5-1", Alias: "fable"},
			{Name: "claude-sonnet-5", Alias: "sonnet"},
			// An entry whose alias equals its name is a real registered model.
			{Name: "claude-opus-5", Alias: "claude-opus-5"},
			// An entry with no alias at all is likewise a real model.
			{Name: "claude-haiku-4-5-20251001"},
		},
	}

	models := buildClaudeConfigModels(entry)
	byID := make(map[string]*ModelInfo, len(models))
	for _, m := range models {
		if m == nil {
			continue
		}
		byID[m.ID] = m
	}

	fable, ok := byID["fable"]
	if !ok {
		t.Fatalf("alias %q not registered; got %v", "fable", keysOf(byID))
	}
	if fable.ExecutionTarget != "claude-fable-5-1" {
		t.Fatalf("fable ExecutionTarget = %q, want %q", fable.ExecutionTarget, "claude-fable-5-1")
	}

	sonnet, ok := byID["sonnet"]
	if !ok {
		t.Fatalf("alias %q not registered; got %v", "sonnet", keysOf(byID))
	}
	if sonnet.ExecutionTarget != "claude-sonnet-5" {
		t.Fatalf("sonnet ExecutionTarget = %q, want %q", sonnet.ExecutionTarget, "claude-sonnet-5")
	}

	// Real registered models must keep ExecutionTarget empty, otherwise the
	// registry's alias-collision handling (see model_registry.go) would treat
	// them as alias-exposed entries.
	selfAliased, ok := byID["claude-opus-5"]
	if !ok {
		t.Fatalf("model %q not registered; got %v", "claude-opus-5", keysOf(byID))
	}
	if selfAliased.ExecutionTarget != "" {
		t.Fatalf("self-aliased ExecutionTarget = %q, want empty", selfAliased.ExecutionTarget)
	}

	bare, ok := byID["claude-haiku-4-5-20251001"]
	if !ok {
		t.Fatalf("model %q not registered; got %v", "claude-haiku-4-5-20251001", keysOf(byID))
	}
	if bare.ExecutionTarget != "" {
		t.Fatalf("unaliased ExecutionTarget = %q, want empty", bare.ExecutionTarget)
	}
}

func keysOf(m map[string]*ModelInfo) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
