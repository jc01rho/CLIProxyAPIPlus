package redisqueue

import "testing"

func TestDefaultRetentionIsAtLeast300Seconds(t *testing.T) {
	if defaultRetentionSeconds < 300 {
		t.Errorf("defaultRetentionSeconds = %d, want >= 300", defaultRetentionSeconds)
	}
}

func TestSetRetentionSecondsUsesDefaultWhenZero(t *testing.T) {
	original := retentionSeconds.Load()
	t.Cleanup(func() { retentionSeconds.Store(original) })

	SetRetentionSeconds(0)
	if got := retentionSeconds.Load(); got != defaultRetentionSeconds {
		t.Errorf("retentionSeconds after SetRetentionSeconds(0) = %d, want %d", got, defaultRetentionSeconds)
	}
}

func TestSetRetentionSecondsClampedToMax(t *testing.T) {
	original := retentionSeconds.Load()
	t.Cleanup(func() { retentionSeconds.Store(original) })

	SetRetentionSeconds(int(maxRetentionSeconds) + 100)
	if got := retentionSeconds.Load(); got != maxRetentionSeconds {
		t.Errorf("retentionSeconds after SetRetentionSeconds(max+100) = %d, want %d", got, maxRetentionSeconds)
	}
}
