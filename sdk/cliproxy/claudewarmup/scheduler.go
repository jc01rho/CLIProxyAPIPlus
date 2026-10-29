package claudewarmup

import (
	"context"
	"errors"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	log "github.com/sirupsen/logrus"
)

const (
	// checkInterval is how often the scheduler compares the clock with the cron schedule.
	checkInterval = 15 * time.Second
	// maxLateness drops a fire that is detected too late (process stalled or host
	// suspended) instead of starting the 5-hour window at an unexpected time.
	maxLateness = 2 * time.Minute
)

// Scheduler fires Run on the configured cron schedule. The configuration is
// re-read on every check so hot reloads take effect without a restart.
type Scheduler struct {
	config func() config.ClaudeWarmupConfig
	now    func() time.Time
	run    func(ctx context.Context, cfg config.ClaudeWarmupConfig)

	lastCheck   time.Time
	lastInvalid string
}

// NewScheduler builds a scheduler that runs warmups through mgr using the live config.
func NewScheduler(mgr Manager, cfg func() config.ClaudeWarmupConfig) *Scheduler {
	return &Scheduler{
		config: cfg,
		now:    time.Now,
		run: func(ctx context.Context, c config.ClaudeWarmupConfig) {
			if _, errRun := Run(ctx, mgr, c, Options{}); errRun != nil && !errors.Is(errRun, context.Canceled) {
				log.Warnf("claude warmup: scheduled run ended with error: %v", errRun)
			}
		},
	}
}

// Start runs the scheduler loop until ctx is cancelled. Fires that elapsed
// before Start (for example while the server was down) are skipped.
func (s *Scheduler) Start(ctx context.Context) {
	s.lastCheck = s.now()
	go func() {
		ticker := time.NewTicker(checkInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.tick(ctx)
			}
		}
	}()
}

// tick fires at most one run when a scheduled minute fell in (lastCheck, now].
// It reports whether a run was started.
func (s *Scheduler) tick(ctx context.Context) bool {
	now := s.now()
	prev := s.lastCheck
	s.lastCheck = now
	cfg := s.config()
	if !cfg.Enabled {
		return false
	}
	cfg = cfg.WithDefaults()
	schedule, errParse := ParseSchedule(cfg.Schedule)
	if errParse != nil {
		if s.lastInvalid != cfg.Schedule {
			s.lastInvalid = cfg.Schedule
			log.Warnf("claude warmup: invalid schedule, warmup disabled until fixed: %v", errParse)
		}
		return false
	}
	s.lastInvalid = ""
	fire := schedule.Next(prev.In(cfg.Location()))
	if fire.IsZero() || fire.After(now) {
		return false
	}
	// Several fires may have elapsed after a stall; only the latest can be on time.
	for next := schedule.Next(fire); !next.IsZero() && !next.After(now); next = schedule.Next(fire) {
		fire = next
	}
	if late := now.Sub(fire); late > maxLateness {
		log.Infof("claude warmup: skipping fire at %s detected %s late", fire.Format(time.RFC3339), late.Truncate(time.Second))
		return false
	}
	log.Infof("claude warmup: scheduled fire at %s", fire.Format(time.RFC3339))
	s.run(ctx, cfg)
	return true
}
