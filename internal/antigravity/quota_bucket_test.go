package antigravity

import (
	"testing"
	"time"
)

func TestTokenBucketSetConfigMaterializesExistingBucketsAndClampsInitial(t *testing.T) {
	tracker := NewTokenBucketTracker(TokenBucketConfig{
		MaxTokens:                 10,
		RegenerationRatePerMinute: 6,
		InitialTokens:             50,
	})
	if got := tracker.Config().InitialTokens; got != 10 {
		t.Fatalf("clamped InitialTokens = %v, want 10", got)
	}

	tracker.Consume(1, 5)
	tracker.mu.Lock()
	tracker.buckets[1] = tokenBucketState{Tokens: 5, LastUpdated: time.Now().Add(-time.Minute)}
	tracker.mu.Unlock()

	tracker.SetConfig(TokenBucketConfig{
		MaxTokens:                 100,
		RegenerationRatePerMinute: 600,
		InitialTokens:             200,
	})
	if got := tracker.Config().InitialTokens; got != 100 {
		t.Fatalf("SetConfig clamped InitialTokens = %v, want 100", got)
	}
	got := tracker.GetTokens(1)
	if got < 9.9 || got > 15 {
		t.Fatalf("GetTokens after SetConfig = %v, want ~10 (materialized under old rate, not 600/min)", got)
	}
}

func TestTokenBucketLowInitialMaterializesAndRecovers(t *testing.T) {
	tracker := NewTokenBucketTracker(TokenBucketConfig{
		MaxTokens:                 5,
		RegenerationRatePerMinute: 60,
		InitialTokens:             0.2,
	})
	if tracker.HasTokens(7, 1) {
		t.Fatal("expected initial 0.2 tokens to fail HasTokens(1)")
	}
	wait, ok := tracker.TimeUntilTokens(7, 1)
	if !ok || wait <= 0 {
		t.Fatalf("TimeUntilTokens = (%v, %v), want positive retryable wait", wait, ok)
	}

	tracker.mu.Lock()
	state, exists := tracker.buckets[7]
	if !exists {
		t.Fatal("expected unseen low-initial bucket to be materialized")
	}
	state.LastUpdated = state.LastUpdated.Add(-time.Minute)
	tracker.buckets[7] = state
	tracker.mu.Unlock()

	if !tracker.HasTokens(7, 1) {
		t.Fatal("expected materialized bucket to recover tokens over time")
	}
}
