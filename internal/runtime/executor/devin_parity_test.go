package executor

import (
	"bytes"
	"testing"
)

// TestDevinRequestMatchesNativeClient pins the fields the native client always
// sends. The wire layout was aligned field by field against the published
// implementation of the same protocol.
func TestDevinRequestMatchesNativeClient(t *testing.T) {
	spec := devinChatSpec{
		Token:       "tok",
		ModelUID:    "swe-2-high",
		System:      "be brief",
		Messages:    []devinMessage{{role: "user", content: "hi"}},
		SessionUUID: "sess-1",
		CascadeID:   "casc-1",
		PromptID:    "prompt-1",
		ExecutionID: "exec-1",
		Tools:       []devinToolDef{{Name: "web_search", Description: "d", Parameters: []byte("{}")}},
		Config:      devinDefaultCompletionConfig(),
	}
	body := devinBuildChatRequestFull(spec)[5:]

	for _, want := range []string{"auto", "exec-1", "casc-1", "prompt-1"} {
		if !bytes.Contains(body, []byte(want)) {
			t.Fatalf("request missing %q", want)
		}
	}
	for _, field := range []int{
		devinReqToolChoiceField,
		devinReqCacheOptionsField,
		devinReqExecutionIDField,
	} {
		found := false
		devinScanFields(body, func(num, _ int, _ uint64, _ []byte) bool {
			if num == field {
				found = true
				return false
			}
			return true
		})
		if !found {
			t.Fatalf("field %d missing from the request", field)
		}
	}
}

// TestDevinStopPatternsAlwaysSent pins that the turn-boundary control tokens
// ride every request. Without them the model can emit control tokens verbatim
// or run past the turn boundary.
func TestDevinStopPatternsAlwaysSent(t *testing.T) {
	cfg := devinDefaultCompletionConfig()
	if len(cfg.StopPatterns) == 0 {
		t.Fatal("default config carries no stop patterns")
	}
	enc := devinEncodeCompletionConfig(cfg)
	for _, want := range []string{"<|user|>", "<|bot|>", "<|endoftext|>", "<|end_of_turn|>"} {
		if !bytes.Contains(enc, []byte(want)) {
			t.Fatalf("stop pattern %q missing", want)
		}
	}
}

// TestDevinUserStopSequencesExtendDefaults pins that caller stop values are
// added to the control tokens rather than replacing them.
func TestDevinUserStopSequencesExtendDefaults(t *testing.T) {
	cfg := devinExtractCompletionConfig([]byte(`{"stop":["DONE","HALT"]}`))
	enc := devinEncodeCompletionConfig(cfg)
	for _, want := range []string{"<|user|>", "DONE", "HALT"} {
		if !bytes.Contains(enc, []byte(want)) {
			t.Fatalf("stop pattern %q missing", want)
		}
	}

	single := devinExtractCompletionConfig([]byte(`{"stop":"STOPHERE"}`))
	if !bytes.Contains(devinEncodeCompletionConfig(single), []byte("STOPHERE")) {
		t.Fatal("string stop value not applied")
	}
}

// TestDevinCompletionConfigDefaultsMatchNative pins the sampling defaults.
func TestDevinCompletionConfigDefaultsMatchNative(t *testing.T) {
	cfg := devinDefaultCompletionConfig()
	if cfg.Temperature != 0.4 {
		t.Fatalf("temperature = %v, want 0.4", cfg.Temperature)
	}
	if cfg.TopP != 1 {
		t.Fatalf("top_p = %v, want 1", cfg.TopP)
	}
	if cfg.TopK != 50 {
		t.Fatalf("top_k = %d, want 50", cfg.TopK)
	}
}
