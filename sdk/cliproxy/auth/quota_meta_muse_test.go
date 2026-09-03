package auth

import (
	"net/http"
	"testing"
	"time"
)

func TestIsMetaMuseProvider_when_OpenAICompatibleMeta(t *testing.T) {
	// Given
	provider := "openai-compatible-meta"

	// When
	matches := IsMetaMuseProvider(provider)

	// Then
	if !matches {
		t.Fatal("openai-compatible-meta must be recognized as Meta Muse")
	}
}

func TestQuotaStateMuseUsage_when_MetaSubscriptionSnapshotObserved(t *testing.T) {
	// Given
	observedAt := time.Unix(1787279282, 0).UTC()
	var quota QuotaState
	headers := http.Header{
		"X-Muse-Fivehour-Used-Percent": {"42.5"},
		"X-Muse-Fivehour-Reset-At":     {"1788431188"},
		"X-Muse-Weekly-Used-Percent":   {"63"},
		"X-Muse-Weekly-Reset-At":       {"1788739200"},
	}

	// When
	changed := quota.ObserveResponseHeadersForProvider("openai-compatible-meta", headers, observedAt)

	// Then
	if !changed {
		t.Fatal("quota observation was not recorded")
	}
	usage, ok := quota.MuseUsage()
	if !ok {
		t.Fatal("MuseUsage() ok = false, want true")
	}
	if usage.FiveHourUsedPercent == nil || *usage.FiveHourUsedPercent != 42.5 {
		t.Fatalf("five-hour used percent = %v, want 42.5", usage.FiveHourUsedPercent)
	}
	if usage.FiveHourResetAt == nil || usage.FiveHourResetAt.Unix() != 1788431188 {
		t.Fatalf("five-hour reset = %v, want epoch 1788431188", usage.FiveHourResetAt)
	}
	if usage.WeeklyUsedPercent == nil || *usage.WeeklyUsedPercent != 63 {
		t.Fatalf("weekly used percent = %v, want 63", usage.WeeklyUsedPercent)
	}
	if usage.WeeklyResetAt == nil || usage.WeeklyResetAt.Unix() != 1788739200 {
		t.Fatalf("weekly reset = %v, want epoch 1788739200", usage.WeeklyResetAt)
	}
	if !usage.ObservedAt.Equal(observedAt) {
		t.Fatalf("observed at = %v, want %v", usage.ObservedAt, observedAt)
	}
}

func TestQuotaStateMuseUsage_when_NonMetaProviderUsesMuseSignals(t *testing.T) {
	// Given
	var quota QuotaState
	headers := http.Header{"X-Muse-Fivehour-Used-Percent": {"42.5"}}

	// When
	changed := quota.ObserveResponseHeadersForProvider("openai-compatible-other", headers, time.Unix(1, 0))

	// Then
	if changed {
		t.Fatal("non-Meta OpenAI-compatible provider must not record Meta signals")
	}
}
