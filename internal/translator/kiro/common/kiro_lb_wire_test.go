package common

import "testing"

// Ported from kiro-lb src/model_resolver.rs (807c8ab): the inverted spelling
// claude-<major>.<minor>-<family> and a trailing context marker are upstream
// facts this fork must keep accepting, so the provenance is part of the contract.
func TestNormalizeKiroModelIDKiroLBSpellings(t *testing.T) {
	t.Parallel()

	const canonical = "claude-sonnet-4.5"
	if got := NormalizeKiroModelID("claude-sonnet-4-5"); got != canonical {
		t.Fatalf("NormalizeKiroModelID(%q) = %q, want %q", "claude-sonnet-4-5", got, canonical)
	}

	for _, input := range []string{
		"claude-4.5-sonnet",
		"claude-4.5-sonnet-20250929",
		"claude-4.5-sonnet[1m]",
		"claude-4.5-sonnet[200k]",
	} {
		input := input
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			if got := NormalizeKiroModelID(input); got != canonical {
				t.Fatalf("NormalizeKiroModelID(%q) = %q, want %q", input, got, canonical)
			}
		})
	}
}

func TestNormalizeKiroModelIDKeepsUnknownContextFreeIDs(t *testing.T) {
	t.Parallel()

	const unknown = "vendor-future-model"
	if got := NormalizeKiroModelID(unknown); got != unknown {
		t.Fatalf("NormalizeKiroModelID(%q) = %q, want %q", unknown, got, unknown)
	}
}
