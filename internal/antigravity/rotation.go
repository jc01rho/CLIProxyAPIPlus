package antigravity

import (
	"math"
	"math/rand"
	"sort"
	"sync"
	"time"
)

const (
	// DefaultHealthInitial is the initial score for new accounts.
	DefaultHealthInitial = 70
	// DefaultHealthSuccessReward is added after a successful request.
	DefaultHealthSuccessReward = 1
	// DefaultHealthRateLimitPenalty is applied after a rate-limit response.
	DefaultHealthRateLimitPenalty = -10
	// DefaultHealthFailurePenalty is applied after auth, network, or other failures.
	DefaultHealthFailurePenalty = -20
	// DefaultHealthRecoveryRatePerHour is the passive recovery rate per rested hour.
	DefaultHealthRecoveryRatePerHour = 2
	// DefaultHealthMinUsable is the minimum health score considered usable.
	DefaultHealthMinUsable = 50
	// DefaultHealthMaxScore is the maximum health score cap.
	DefaultHealthMaxScore = 100
	// StickinessBonus prevents unnecessary switching away from the current account.
	StickinessBonus = 150
	// SwitchThreshold is the minimum base-score advantage required to switch accounts.
	SwitchThreshold = 100
)

// HealthScoreConfig controls account health scoring and passive recovery.
type HealthScoreConfig struct{ Initial, SuccessReward, RateLimitPenalty, FailurePenalty, RecoveryRatePerHour, MinUsable, MaxScore float64 }

// DefaultHealthScoreConfig returns the reference Antigravity health-score configuration.
func DefaultHealthScoreConfig() HealthScoreConfig {
	return HealthScoreConfig{DefaultHealthInitial, DefaultHealthSuccessReward, DefaultHealthRateLimitPenalty, DefaultHealthFailurePenalty, DefaultHealthRecoveryRatePerHour, DefaultHealthMinUsable, DefaultHealthMaxScore}
}

type healthScoreState struct {
	Score                    float64
	LastUpdated, LastSuccess time.Time
	ConsecutiveFailures      int
}

// HealthScoreSnapshot is a read-only view of account health.
type HealthScoreSnapshot struct {
	Score               float64
	ConsecutiveFailures int
}

// HealthScoreTracker tracks health scores for accounts.
type HealthScoreTracker struct {
	mu     sync.RWMutex
	scores map[int]healthScoreState
	config HealthScoreConfig
}

// NewHealthScoreTracker creates a health tracker. Zero fields are filled with defaults.
func NewHealthScoreTracker(config HealthScoreConfig) *HealthScoreTracker {
	cfg := DefaultHealthScoreConfig()
	if config.Initial != 0 {
		cfg.Initial = config.Initial
	}
	if config.SuccessReward != 0 {
		cfg.SuccessReward = config.SuccessReward
	}
	if config.RateLimitPenalty != 0 {
		cfg.RateLimitPenalty = config.RateLimitPenalty
	}
	if config.FailurePenalty != 0 {
		cfg.FailurePenalty = config.FailurePenalty
	}
	if config.RecoveryRatePerHour != 0 {
		cfg.RecoveryRatePerHour = config.RecoveryRatePerHour
	}
	if config.MinUsable != 0 {
		cfg.MinUsable = config.MinUsable
	}
	if config.MaxScore != 0 {
		cfg.MaxScore = config.MaxScore
	}
	return &HealthScoreTracker{scores: map[int]healthScoreState{}, config: cfg}
}

// GetScore returns the current score, applying floor(hoursSinceUpdate*recoveryRatePerHour).
func (t *HealthScoreTracker) GetScore(accountIndex int) float64 {
	if t == nil {
		return DefaultHealthInitial
	}
	t.mu.RLock()
	state, ok := t.scores[accountIndex]
	cfg := t.config
	t.mu.RUnlock()
	if !ok {
		return cfg.Initial
	}
	recovered := math.Floor(time.Since(state.LastUpdated).Hours() * cfg.RecoveryRatePerHour)
	return math.Min(cfg.MaxScore, state.Score+recovered)
}

// RecordSuccess improves account health after a successful request.
func (t *HealthScoreTracker) RecordSuccess(accountIndex int) {
	if t == nil {
		return
	}
	now := time.Now()
	current := t.GetScore(accountIndex)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.scores[accountIndex] = healthScoreState{Score: math.Min(t.config.MaxScore, current+t.config.SuccessReward), LastUpdated: now, LastSuccess: now}
}

// RecordRateLimit applies the reference rate-limit penalty.
func (t *HealthScoreTracker) RecordRateLimit(accountIndex int) {
	if t == nil {
		return
	}
	now := time.Now()
	current := t.GetScore(accountIndex)
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.scores[accountIndex]
	t.scores[accountIndex] = healthScoreState{Score: math.Max(0, current+t.config.RateLimitPenalty), LastUpdated: now, LastSuccess: s.LastSuccess, ConsecutiveFailures: s.ConsecutiveFailures + 1}
}

// RecordFailure applies the reference failure penalty.
func (t *HealthScoreTracker) RecordFailure(accountIndex int) {
	if t == nil {
		return
	}
	now := time.Now()
	current := t.GetScore(accountIndex)
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.scores[accountIndex]
	t.scores[accountIndex] = healthScoreState{Score: math.Max(0, current+t.config.FailurePenalty), LastUpdated: now, LastSuccess: s.LastSuccess, ConsecutiveFailures: s.ConsecutiveFailures + 1}
}

// IsUsable reports whether account health is at least MinUsable.
func (t *HealthScoreTracker) IsUsable(accountIndex int) bool {
	if t == nil {
		return DefaultHealthInitial >= DefaultHealthMinUsable
	}
	return t.GetScore(accountIndex) >= t.config.MinUsable
}

// GetConsecutiveFailures returns consecutive failures for an account.
func (t *HealthScoreTracker) GetConsecutiveFailures(accountIndex int) int {
	if t == nil {
		return 0
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.scores[accountIndex].ConsecutiveFailures
}

// Reset removes health state for an account.
func (t *HealthScoreTracker) Reset(accountIndex int) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.scores, accountIndex)
}

// Snapshot returns current explicit health states.
func (t *HealthScoreTracker) Snapshot() map[int]HealthScoreSnapshot {
	out := map[int]HealthScoreSnapshot{}
	if t == nil {
		return out
	}
	t.mu.RLock()
	ids := make([]int, 0, len(t.scores))
	for id := range t.scores {
		ids = append(ids, id)
	}
	t.mu.RUnlock()
	for _, id := range ids {
		out[id] = HealthScoreSnapshot{Score: t.GetScore(id), ConsecutiveFailures: t.GetConsecutiveFailures(id)}
	}
	return out
}

var jitterRand = struct {
	sync.Mutex
	r *rand.Rand
}{r: rand.New(rand.NewSource(time.Now().UnixNano()))}

// AddJitter adds random jitter to a delay; jitterFactor defaults to 0.3 when zero.
func AddJitter(base time.Duration, jitterFactor float64) time.Duration {
	if jitterFactor == 0 {
		jitterFactor = 0.3
	}
	baseMS := float64(base) / float64(time.Millisecond)
	jitterRange := baseMS * jitterFactor
	jitterRand.Lock()
	jitter := (jitterRand.r.Float64()*2 - 1) * jitterRange
	jitterRand.Unlock()
	return time.Duration(math.Max(0, math.Round(baseMS+jitter))) * time.Millisecond
}

// RandomDelay returns a random delay between min and max, rounded to milliseconds.
func RandomDelay(min, max time.Duration) time.Duration {
	minMS := float64(min) / float64(time.Millisecond)
	maxMS := float64(max) / float64(time.Millisecond)
	jitterRand.Lock()
	v := minMS + jitterRand.r.Float64()*(maxMS-minMS)
	jitterRand.Unlock()
	return time.Duration(math.Round(v)) * time.Millisecond
}

// CooldownTracker tracks per-account cooldown deadlines.
type CooldownTracker struct {
	mu        sync.RWMutex
	deadlines map[int]time.Time
}

// NewCooldownTracker creates an empty cooldown tracker.
func NewCooldownTracker() *CooldownTracker { return &CooldownTracker{deadlines: map[int]time.Time{}} }

// Set records a cooldown duration, clearing it for non-positive durations.
func (t *CooldownTracker) Set(accountIndex int, duration time.Duration) {
	if t == nil {
		return
	}
	if duration <= 0 {
		t.Clear(accountIndex)
		return
	}
	t.SetUntil(accountIndex, time.Now().Add(duration))
}

// SetUntil records a cooldown deadline.
func (t *CooldownTracker) SetUntil(accountIndex int, deadline time.Time) {
	if t == nil {
		return
	}
	if deadline.IsZero() || !deadline.After(time.Now()) {
		t.Clear(accountIndex)
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.deadlines[accountIndex] = deadline
}

// IsCoolingDown reports whether an account is cooling down.
func (t *CooldownTracker) IsCoolingDown(accountIndex int) bool { return t.Remaining(accountIndex) > 0 }

// Remaining returns remaining cooldown duration.
func (t *CooldownTracker) Remaining(accountIndex int) time.Duration {
	if t == nil {
		return 0
	}
	t.mu.RLock()
	deadline, ok := t.deadlines[accountIndex]
	t.mu.RUnlock()
	if !ok {
		return 0
	}
	rem := time.Until(deadline)
	if rem <= 0 {
		t.Clear(accountIndex)
		return 0
	}
	return rem
}

// Clear removes cooldown state.
func (t *CooldownTracker) Clear(accountIndex int) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.deadlines, accountIndex)
}

// Snapshot returns active cooldown deadlines and prunes expired entries.
func (t *CooldownTracker) Snapshot() map[int]time.Time {
	out := map[int]time.Time{}
	if t == nil {
		return out
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, deadline := range t.deadlines {
		if !deadline.After(now) {
			delete(t.deadlines, id)
			continue
		}
		out[id] = deadline
	}
	return out
}

// AccountWithMetrics contains account selector inputs.
type AccountWithMetrics struct {
	Index                        int
	LastUsed                     time.Time
	HealthScore                  float64
	IsRateLimited, IsCoolingDown bool
}

// SortByLRUWithHealth filters unavailable accounts and sorts by LRU, then health.
func SortByLRUWithHealth(accounts []AccountWithMetrics, minHealthScore float64) []AccountWithMetrics {
	if minHealthScore == 0 {
		minHealthScore = DefaultHealthMinUsable
	}
	filtered := make([]AccountWithMetrics, 0, len(accounts))
	for _, a := range accounts {
		if !a.IsRateLimited && !a.IsCoolingDown && a.HealthScore >= minHealthScore {
			filtered = append(filtered, a)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		if !filtered[i].LastUsed.Equal(filtered[j].LastUsed) {
			return filtered[i].LastUsed.Before(filtered[j].LastUsed)
		}
		return filtered[i].HealthScore > filtered[j].HealthScore
	})
	return filtered
}

// TokenBalanceTracker is the token-bucket surface required by SelectHybridAccount.
type TokenBalanceTracker interface {
	GetTokens(accountIndex int) float64
	HasTokens(accountIndex int, cost ...float64) bool
	GetMaxTokens() float64
}
type accountWithTokens struct {
	AccountWithMetrics
	Tokens float64
}
type scoredAccount struct {
	Index            int
	BaseScore, Score float64
	IsCurrent        bool
}

// SelectHybridAccount selects with health*2 + tokens/max*500 + freshness*0.1 plus stickiness.
func SelectHybridAccount(accounts []AccountWithMetrics, tracker TokenBalanceTracker, currentAccountIndex *int, minHealthScore float64) *int {
	if tracker == nil {
		return nil
	}
	if minHealthScore == 0 {
		minHealthScore = DefaultHealthMinUsable
	}
	candidates := make([]accountWithTokens, 0, len(accounts))
	for _, a := range accounts {
		if a.IsRateLimited || a.IsCoolingDown || a.HealthScore < minHealthScore || !tracker.HasTokens(a.Index, 1) {
			continue
		}
		candidates = append(candidates, accountWithTokens{AccountWithMetrics: a, Tokens: tracker.GetTokens(a.Index)})
	}
	if len(candidates) == 0 {
		return nil
	}
	maxTokens := tracker.GetMaxTokens()
	scored := make([]scoredAccount, 0, len(candidates))
	for _, a := range candidates {
		base := calculateHybridScore(a, maxTokens)
		isCurrent := currentAccountIndex != nil && a.Index == *currentAccountIndex
		bonus := 0.0
		if isCurrent {
			bonus = StickinessBonus
		}
		scored = append(scored, scoredAccount{Index: a.Index, BaseScore: base, Score: base + bonus, IsCurrent: isCurrent})
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
	best := scored[0]
	var current *scoredAccount
	for i := range scored {
		if scored[i].IsCurrent {
			current = &scored[i]
			break
		}
	}
	if current != nil && !best.IsCurrent {
		if best.BaseScore-current.BaseScore < SwitchThreshold {
			selected := current.Index
			return &selected
		}
	}
	selected := best.Index
	return &selected
}
func calculateHybridScore(a accountWithTokens, maxTokens float64) float64 {
	health := a.HealthScore * 2
	tokens := (a.Tokens / maxTokens) * 100 * 5
	freshness := math.Min(time.Since(a.LastUsed).Seconds(), 3600) * 0.1
	return math.Max(0, health+tokens+freshness)
}

var globalHealthTrackerMu sync.Mutex
var globalHealthTracker *HealthScoreTracker

// GetHealthTracker returns the global health tracker.
func GetHealthTracker() *HealthScoreTracker {
	globalHealthTrackerMu.Lock()
	defer globalHealthTrackerMu.Unlock()
	if globalHealthTracker == nil {
		globalHealthTracker = NewHealthScoreTracker(HealthScoreConfig{})
	}
	return globalHealthTracker
}

// InitHealthTracker replaces the global health tracker.
func InitHealthTracker(config HealthScoreConfig) *HealthScoreTracker {
	globalHealthTrackerMu.Lock()
	defer globalHealthTrackerMu.Unlock()
	globalHealthTracker = NewHealthScoreTracker(config)
	return globalHealthTracker
}
