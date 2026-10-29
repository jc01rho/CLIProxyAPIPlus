package claudewarmup

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func newTestScheduler(clock *fakeClock, cfg *config.ClaudeWarmupConfig, runs *[]time.Time) *Scheduler {
	return &Scheduler{
		config: func() config.ClaudeWarmupConfig { return *cfg },
		now:    clock.Now,
		run: func(_ context.Context, _ config.ClaudeWarmupConfig) {
			*runs = append(*runs, clock.now)
		},
	}
}

func TestSchedulerFiresOnceWhenScheduledMinutePasses(t *testing.T) {
	seoul := mustLoc(t, "Asia/Seoul")
	clock := &fakeClock{now: time.Date(2026, 10, 29, 7, 49, 40, 0, seoul)}
	cfg := config.ClaudeWarmupConfig{Enabled: true}
	var runs []time.Time
	s := newTestScheduler(clock, &cfg, &runs)
	s.lastCheck = clock.now

	clock.now = clock.now.Add(checkInterval) // 07:49:55
	if s.tick(context.Background()) {
		t.Fatal("fired before 07:50")
	}
	clock.now = clock.now.Add(checkInterval) // 07:50:10
	if !s.tick(context.Background()) {
		t.Fatal("did not fire after 07:50")
	}
	clock.now = clock.now.Add(checkInterval) // 07:50:25
	if s.tick(context.Background()) {
		t.Fatal("fired twice for the same minute")
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
}

func TestSchedulerSkipsFireMissedBeforeStart(t *testing.T) {
	seoul := mustLoc(t, "Asia/Seoul")
	// Server starts at 08:30, after today's 07:50 fire.
	clock := &fakeClock{now: time.Date(2026, 10, 29, 8, 30, 0, 0, seoul)}
	cfg := config.ClaudeWarmupConfig{Enabled: true}
	var runs []time.Time
	s := newTestScheduler(clock, &cfg, &runs)
	s.lastCheck = clock.now
	for i := 0; i < 10; i++ {
		clock.now = clock.now.Add(checkInterval)
		s.tick(context.Background())
	}
	if len(runs) != 0 {
		t.Fatalf("missed fire ran late: %v", runs)
	}
}

func TestSchedulerSkipsFireDetectedTooLate(t *testing.T) {
	seoul := mustLoc(t, "Asia/Seoul")
	clock := &fakeClock{now: time.Date(2026, 10, 29, 7, 40, 0, 0, seoul)}
	cfg := config.ClaudeWarmupConfig{Enabled: true}
	var runs []time.Time
	s := newTestScheduler(clock, &cfg, &runs)
	s.lastCheck = clock.now
	// Host suspended across the fire; next check happens at 09:00.
	clock.now = time.Date(2026, 10, 29, 9, 0, 0, 0, seoul)
	if s.tick(context.Background()) || len(runs) != 0 {
		t.Fatal("stale fire must be skipped")
	}
}

func TestSchedulerHonorsWeekdayFilterAndHotReload(t *testing.T) {
	seoul := mustLoc(t, "Asia/Seoul")
	// Saturday 2026-10-31 07:49:50.
	clock := &fakeClock{now: time.Date(2026, 10, 31, 7, 49, 50, 0, seoul)}
	cfg := config.ClaudeWarmupConfig{Enabled: true}
	var runs []time.Time
	s := newTestScheduler(clock, &cfg, &runs)
	s.lastCheck = clock.now
	clock.now = clock.now.Add(checkInterval)
	if s.tick(context.Background()) {
		t.Fatal("default weekday schedule fired on Saturday")
	}
	// Hot reload: include weekends at 08:00.
	cfg.Schedule = "0 8 * * *"
	clock.now = time.Date(2026, 10, 31, 8, 0, 5, 0, seoul)
	if !s.tick(context.Background()) {
		t.Fatal("reloaded schedule did not fire")
	}
	// Disabled config never fires.
	cfg.Enabled = false
	clock.now = time.Date(2026, 11, 1, 7, 59, 55, 0, seoul)
	s.tick(context.Background())
	clock.now = clock.now.Add(checkInterval)
	if s.tick(context.Background()) {
		t.Fatal("disabled schedule fired")
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
}

func TestSchedulerUsesConfiguredTimezone(t *testing.T) {
	// 07:50 in UTC is 16:50 in Seoul; the fire must follow the configured zone.
	clock := &fakeClock{now: time.Date(2026, 10, 29, 7, 49, 50, 0, time.UTC)}
	cfg := config.ClaudeWarmupConfig{Enabled: true, Timezone: "UTC"}
	var runs []time.Time
	s := newTestScheduler(clock, &cfg, &runs)
	s.lastCheck = clock.now
	clock.now = clock.now.Add(checkInterval)
	if !s.tick(context.Background()) {
		t.Fatal("UTC schedule did not fire at 07:50 UTC")
	}
	cfg.Timezone = "" // default Asia/Seoul
	clock.now = time.Date(2026, 10, 30, 7, 49, 50, 0, time.UTC)
	s.lastCheck = clock.now
	clock.now = clock.now.Add(checkInterval)
	if s.tick(context.Background()) {
		t.Fatal("Seoul schedule fired at 07:50 UTC")
	}
}

func TestSchedulerInvalidScheduleDoesNotFire(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 10, 29, 7, 49, 50, 0, time.UTC)}
	cfg := config.ClaudeWarmupConfig{Enabled: true, Schedule: "bogus"}
	var runs []time.Time
	s := newTestScheduler(clock, &cfg, &runs)
	s.lastCheck = clock.now
	clock.now = clock.now.Add(time.Hour)
	if s.tick(context.Background()) {
		t.Fatal("invalid schedule fired")
	}
}
