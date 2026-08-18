package copilot

import (
	"testing"
)

func boolPtr(v bool) *bool { return &v }

func modelEntry(id string, picker *bool, policyState string, toolCalls any) CopilotModelEntry {
	entry := CopilotModelEntry{ID: id, ModelPickerEnabled: picker}
	if policyState != "" {
		entry.Policy = &CopilotModelPolicy{State: policyState}
	}
	if toolCalls != nil {
		entry.Capabilities = map[string]any{"supports": map[string]any{"tool_calls": toolCalls}}
	}
	return entry
}

func entryIDs(entries []CopilotModelEntry) []string {
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.ID)
	}
	return ids
}

func TestFilterAvailableCopilotModelsPickerPreferred(t *testing.T) {
	entries := []CopilotModelEntry{
		modelEntry("picker-on", boolPtr(true), "enabled", nil),
		modelEntry("picker-off", boolPtr(false), "enabled", nil),
		modelEntry("picker-disabled", boolPtr(true), "disabled", nil),
		modelEntry("no-tool-calls", boolPtr(true), "enabled", false),
	}
	got := entryIDs(FilterAvailableCopilotModels(entries, false))
	if len(got) != 1 || got[0] != "picker-on" {
		t.Fatalf("filtered = %v, want [picker-on]", got)
	}
}

func TestFilterAvailableCopilotModelsPolicyFallback(t *testing.T) {
	entries := []CopilotModelEntry{
		modelEntry("policy-enabled", boolPtr(false), "enabled", nil),
		modelEntry("policy-none", boolPtr(false), "", nil),
	}
	// Individual accounts fall back to policy-enabled models when no
	// picker-enabled model exists.
	got := entryIDs(FilterAvailableCopilotModels(entries, true))
	if len(got) != 1 || got[0] != "policy-enabled" {
		t.Fatalf("fallback filtered = %v, want [policy-enabled]", got)
	}
	// Non-individual endpoints keep strict picker semantics (empty result).
	if got := FilterAvailableCopilotModels(entries, false); len(got) != 0 {
		t.Fatalf("strict filtered = %v, want empty", entryIDs(got))
	}
}

func TestFilterAvailableCopilotModelsNoMetadataPassthrough(t *testing.T) {
	entries := []CopilotModelEntry{
		{ID: "plain-a"},
		{ID: "plain-b"},
	}
	got := FilterAvailableCopilotModels(entries, false)
	if len(got) != 2 {
		t.Fatalf("no-metadata filter dropped entries: %v", entryIDs(got))
	}
}

func TestSupportsToolCallsExplicitFalseOnly(t *testing.T) {
	bare := CopilotModelEntry{ID: "x"}
	if !bare.SupportsToolCalls() {
		t.Error("missing capabilities must keep tool calls")
	}
	noTools := modelEntry("x", nil, "", false)
	if noTools.SupportsToolCalls() {
		t.Error("explicit tool_calls=false must disable tool calls")
	}
	withTools := modelEntry("x", nil, "", true)
	if !withTools.SupportsToolCalls() {
		t.Error("explicit tool_calls=true must keep tool calls")
	}
}
