package auth

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// RoundRobinSelector provides a simple provider scoped round-robin selection strategy.
type RoundRobinSelector struct {
	mu      sync.Mutex
	cursors map[string]int
	maxKeys int
	Mode    string // "key-based" or empty for default behavior
}

// FillFirstSelector selects the first available credential (deterministic ordering).
// This "burns" one account before moving to the next, which can help stagger
// rolling-window subscription caps (e.g. chat message limits).
type FillFirstSelector struct{}

// WeightedRobinSelector provides weighted random selection via shuffled cycles.
// Priority values are interpreted as weights: higher priority auths receive
// proportionally more traffic. Auths with priority 0 (or no priority) are
// treated as weight 1.
//
// Each model/alias maintains its own independent shuffled cycle so that
// requests for different aliases do not interfere with each other's
// progress. Within one cycle every auth appears exactly its weight number
// of times — guaranteeing execution even for low-weight keys in small
// sample sizes.
//
// To prevent unbounded memory growth across thousands of configured models/
// aliases, the selector evicts auths that have not been picked within
// `lruEvictWindow` from the cycle. The eviction is a soft filter: if it
// would empty the cycle entirely, the full set is used as a fallback so
// that traffic is never starved.
type WeightedRobinSelector struct {
	mu             sync.Mutex
	cycles         map[string]*aliasCycle // per-model/alias cycle state keyed by model string
	lastUsed       map[string]time.Time   // LRU: last time each auth was picked (by ID)
	lruEvictWindow time.Duration          // 0 disables eviction; default 24h
	knownAuths     map[string]*Auth       // all auths ever observed via Pick, for QueueState display
	pickedCounts   map[string]uint64      // per-auth total pick count since process start (by ID)
	totalPicks     uint64                 // total Pick() selections served by this selector
	lastPickedAt   time.Time              // timestamp of the most recent successful Pick()
}

// aliasCycle holds the shuffled cycle and cursor for a single model/alias.
// Each model/alias maintains its own independent aliasCycle so that
// traffic for different aliases does not share a cursor and does not
// trigger cycle rebuilds when other aliases are picked.
type aliasCycle struct {
	cycle       []*Auth         // shuffled cycle, length = normalized totalWeight
	head        int             // pop position (front of queue)
	totalWeight int             // total weight when cycle was built (GCD-normalized)
	gcd         int             // GCD used to normalize totalWeight; 0 if cycle is empty
	weightHash  uint64          // FNV hash of auth IDs × weights when cycle was built
	authIDs     map[string]struct{} // auth ID set captured at build time, for invalidation
}

const defaultLRUEvictWindow = 24 * time.Hour

func (s *WeightedRobinSelector) now() time.Time {
	return time.Now()
}

func (s *WeightedRobinSelector) shouldEvict(auth *Auth, now time.Time) bool {
	if s.lruEvictWindow <= 0 || auth == nil {
		return false
	}
	last, ok := s.lastUsed[auth.ID]
	if !ok {
		// Never picked: keep it (otherwise newly added auths would never
		// enter the cycle).
		return false
	}
	return now.Sub(last) > s.lruEvictWindow
}

// evictUnusedAuths returns the subset of `auths` that have been used
// within the LRU window, or the full set if the filtered set would be
// empty. This prevents the cycle from being starved when many auths are
// stale.
func (s *WeightedRobinSelector) evictUnusedAuths(auths []*Auth) []*Auth {
	if s.lruEvictWindow <= 0 || len(auths) == 0 {
		return auths
	}
	now := s.now()
	kept := make([]*Auth, 0, len(auths))
	for _, a := range auths {
		if a == nil {
			continue
		}
		if !s.shouldEvict(a, now) {
			kept = append(kept, a)
		}
	}
	if len(kept) == 0 {
		// Fallback: keep at least one auth (prefer the one most recently
		// used) so the selector never returns "no auth available" simply
		// because every auth happens to be stale.
		var newest *Auth
		var newestAt time.Time
		for _, a := range auths {
			if a == nil {
				continue
			}
			if last, ok := s.lastUsed[a.ID]; ok && (newest == nil || last.After(newestAt)) {
				newest = a
				newestAt = last
			}
		}
		if newest != nil {
			kept = append(kept, newest)
		} else {
			kept = append(kept, auths[0])
		}
	}
	return kept
}

type blockReason int

const (
	blockReasonNone blockReason = iota
	blockReasonCooldown
	blockReasonDisabled
	blockReasonOther
)

type modelCooldownError struct {
	model    string
	resetIn  time.Duration
	provider string
}

func newModelCooldownError(model, provider string, resetIn time.Duration) *modelCooldownError {
	if resetIn < 0 {
		resetIn = 0
	}
	return &modelCooldownError{
		model:    model,
		provider: provider,
		resetIn:  resetIn,
	}
}

func (e *modelCooldownError) Error() string {
	modelName := e.model
	if modelName == "" {
		modelName = "requested model"
	}
	message := fmt.Sprintf("All credentials for model %s are cooling down", modelName)
	if e.provider != "" {
		message = fmt.Sprintf("%s via provider %s", message, e.provider)
	}
	resetSeconds := int(math.Ceil(e.resetIn.Seconds()))
	if resetSeconds < 0 {
		resetSeconds = 0
	}
	displayDuration := e.resetIn
	if displayDuration > 0 && displayDuration < time.Second {
		displayDuration = time.Second
	} else {
		displayDuration = displayDuration.Round(time.Second)
	}
	errorBody := map[string]any{
		"code":          "model_cooldown",
		"message":       message,
		"model":         e.model,
		"reset_time":    displayDuration.String(),
		"reset_seconds": resetSeconds,
	}
	if e.provider != "" {
		errorBody["provider"] = e.provider
	}
	payload := map[string]any{"error": errorBody}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf(`{"error":{"code":"model_cooldown","message":"%s"}}`, message)
	}
	return string(data)
}

func (e *modelCooldownError) StatusCode() int {
	return http.StatusTooManyRequests
}

func (e *modelCooldownError) Headers() http.Header {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	resetSeconds := int(math.Ceil(e.resetIn.Seconds()))
	if resetSeconds < 0 {
		resetSeconds = 0
	}
	headers.Set("Retry-After", strconv.Itoa(resetSeconds))
	return headers
}

const (
	primaryPriorityBonus = 1_000_000
)

func authPriority(auth *Auth) int {
	if auth == nil {
		return 0
	}
	basePriority := 0
	if auth.Attributes != nil {
		raw := strings.TrimSpace(auth.Attributes["priority"])
		if raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil {
				basePriority = parsed
			}
		}
	}
	if basePriority < 0 {
		basePriority = 0
	}
	if auth.PrimaryInfo != nil && auth.PrimaryInfo.IsPrimary {
		return basePriority + primaryPriorityBonus
	}
	return basePriority
}

func canonicalModelKey(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	parsed := thinking.ParseSuffix(model)
	modelName := strings.TrimSpace(parsed.ModelName)
	if modelName == "" {
		return model
	}
	return modelName
}

func authWebsocketsEnabled(auth *Auth) bool {
	if auth == nil {
		return false
	}
	if len(auth.Attributes) > 0 {
		if raw := strings.TrimSpace(auth.Attributes["websockets"]); raw != "" {
			parsed, errParse := strconv.ParseBool(raw)
			if errParse == nil {
				return parsed
			}
		}
	}
	if len(auth.Metadata) == 0 {
		return false
	}
	raw, ok := auth.Metadata["websockets"]
	if !ok || raw == nil {
		return false
	}
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		parsed, errParse := strconv.ParseBool(strings.TrimSpace(v))
		if errParse == nil {
			return parsed
		}
	default:
	}
	return false
}

func preferCodexWebsocketAuths(ctx context.Context, provider string, available []*Auth) []*Auth {
	if len(available) == 0 {
		return available
	}
	if !cliproxyexecutor.DownstreamWebsocket(ctx) {
		return available
	}
	if !strings.EqualFold(strings.TrimSpace(provider), "codex") {
		return available
	}

	wsEnabled := make([]*Auth, 0, len(available))
	for i := 0; i < len(available); i++ {
		candidate := available[i]
		if authWebsocketsEnabled(candidate) {
			wsEnabled = append(wsEnabled, candidate)
		}
	}
	if len(wsEnabled) > 0 {
		return wsEnabled
	}
	return available
}

func collectAvailableByPriority(auths []*Auth, model string, now time.Time) (available map[int][]*Auth, cooldownCount int, earliest time.Time) {
	available = make(map[int][]*Auth)
	for i := 0; i < len(auths); i++ {
		candidate := auths[i]
		blocked, reason, next := isAuthBlockedForModel(candidate, model, now)
		if !blocked {
			priority := authPriority(candidate)
			available[priority] = append(available[priority], candidate)
			continue
		}
		if reason == blockReasonCooldown {
			cooldownCount++
			if !next.IsZero() && (earliest.IsZero() || next.Before(earliest)) {
				earliest = next
			}
		}
	}
	return available, cooldownCount, earliest
}

func getAvailableAuths(auths []*Auth, provider, model string, now time.Time) ([]*Auth, error) {
	if len(auths) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth candidates"}
	}

	availableByPriority, cooldownCount, earliest := collectAvailableByPriority(auths, model, now)
	if len(availableByPriority) == 0 {
		if cooldownCount == len(auths) && !earliest.IsZero() {
			providerForError := provider
			if providerForError == "mixed" {
				providerForError = ""
			}
			resetIn := earliest.Sub(now)
			if resetIn < 0 {
				resetIn = 0
			}
			return nil, newModelCooldownError(model, providerForError, resetIn)
		}
		return nil, &Error{Code: "auth_unavailable", Message: "no auth available"}
	}

	bestPriority := 0
	found := false
	for priority := range availableByPriority {
		if !found || priority > bestPriority {
			bestPriority = priority
			found = true
		}
	}

	available := availableByPriority[bestPriority]
	if len(available) > 1 {
		sort.Slice(available, func(i, j int) bool { return available[i].ID < available[j].ID })
	}
	return available, nil
}

// getAllAvailableAuths returns all non-blocked auths regardless of priority.
// Used by WeightedRobinSelector to distribute across all priorities by weight.
func getAllAvailableAuths(auths []*Auth, model string, now time.Time) []*Auth {
	var available []*Auth
	for _, a := range auths {
		if a == nil {
			continue
		}
		if a.Disabled || a.Status == StatusDisabled {
			continue
		}
		blocked, reason, _ := isAuthBlockedForModel(a, model, now)
		if blocked && (reason == blockReasonCooldown || reason == blockReasonDisabled) {
			continue
		}
		available = append(available, a)
	}
	if len(available) > 1 {
		sort.Slice(available, func(i, j int) bool { return available[i].ID < available[j].ID })
	}
	return available
}

// Pick selects the next available auth for the provider in a round-robin manner.
func (s *RoundRobinSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = opts
	now := time.Now()
	available, err := getAvailableAuths(auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	key := provider + ":" + canonicalModelKey(model)
	s.mu.Lock()
	if s.cursors == nil {
		s.cursors = make(map[string]int)
	}
	limit := s.maxKeys
	if limit <= 0 {
		limit = 4096
	}

	s.ensureCursorKey(key, limit)
	index := s.cursors[key]
	if index >= 2_147_483_640 {
		index = 0
	}
	s.cursors[key] = index + 1
	s.mu.Unlock()
	return available[index%len(available)], nil
}

// ensureCursorKey ensures the cursor map has capacity for the given key.
// Must be called with s.mu held.
func (s *RoundRobinSelector) ensureCursorKey(key string, limit int) {
	if _, ok := s.cursors[key]; !ok && len(s.cursors) >= limit {
		s.cursors = make(map[string]int)
	}
}

// Pick selects the first available auth for the provider in a deterministic manner.
func (s *FillFirstSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = opts
	now := time.Now()
	available, err := getAvailableAuths(auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	return available[0], nil
}

// Pick selects auths using weighted random selection where priority values are
// interpreted as weights (default 0 → weight 1). Each pick is random but
// probability is proportional to weight, so the ratio converges over time.
//
// The model string is used as the cycle key so that different model/alias
// requests maintain independent shuffled cycles and cursors. This prevents
// traffic for one alias from interfering with the cursor of another.
func (s *WeightedRobinSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (selected *Auth, _ error) {
	_ = opts
	now := time.Now()

	s.mu.Lock()
	if s.knownAuths == nil {
		s.knownAuths = make(map[string]*Auth, len(auths))
	}
	for _, a := range auths {
		if a != nil {
			s.knownAuths[a.ID] = a
		}
	}
	if s.lastUsed == nil {
		s.lastUsed = make(map[string]time.Time)
	}
	if s.pickedCounts == nil {
		s.pickedCounts = make(map[string]uint64)
	}
	if s.lruEvictWindow == 0 {
		s.lruEvictWindow = defaultLRUEvictWindow
	}
	s.mu.Unlock()

	available := getAllAvailableAuths(auths, model, now)
	if len(available) == 0 {
		cooldownCount := 0
		earliest := time.Time{}
		for _, a := range auths {
			if a != nil {
				blocked, reason, next := isAuthBlockedForModel(a, model, now)
				if blocked && reason == blockReasonCooldown {
					cooldownCount++
					if !next.IsZero() && (earliest.IsZero() || next.Before(earliest)) {
						earliest = next
					}
				}
			}
		}
		if cooldownCount == len(auths) && !earliest.IsZero() {
			return nil, newModelCooldownError(model, provider, earliest.Sub(now))
		}
		return nil, &Error{Code: "auth_unavailable", Message: "no auth available"}
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)

	if len(available) == 1 {
		s.mu.Lock()
		s.lastUsed[available[0].ID] = now
		s.pickedCounts[available[0].ID]++
		s.totalPicks++
		s.lastPickedAt = now
		s.mu.Unlock()
		return available[0], nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cycleAuths := s.evictUnusedAuths(available)
	if len(cycleAuths) == 0 {
		return nil, &Error{Code: "auth_unavailable", Message: "no auth available after LRU eviction"}
	}

	cycleKey := canonicalModelKey(model)
	if s.cycles == nil {
		s.cycles = make(map[string]*aliasCycle)
	}
	state, ok := s.cycles[cycleKey]
	if !ok {
		state = &aliasCycle{}
		s.cycles[cycleKey] = state
	}

	// Rebuild cycle when: (1) empty, (2) total weight changed, or
	// (3) any available auth changed its ID set since the last build.
	// Without this check, priority/weight edits after the first build
	// are silently ignored until the next full cycle wrap.
	if state.cycle == nil || len(state.cycle) == 0 {
		s.rebuildCycle(cycleAuths, state)
	} else {
		sameSet := state.authIDs != nil
		if sameSet {
			if len(state.authIDs) != len(cycleAuths) {
				sameSet = false
			} else {
				for _, a := range cycleAuths {
					if _, found := state.authIDs[a.ID]; !found {
						sameSet = false
						break
					}
				}
			}
		}
		newHash := calculateWeightHash(cycleAuths)
		if !sameSet || state.weightHash != newHash {
			s.rebuildCycle(cycleAuths, state)
		}
	}

	for attempts := 0; attempts < len(state.cycle); attempts++ {
		if state.head >= len(state.cycle) {
			state.head = 0
			s.rebuildCycle(cycleAuths, state)
		}
		selected := state.cycle[state.head]
		state.head++

		if s.shouldEvict(selected, now) {
			continue
		}

		s.lastUsed[selected.ID] = now
		s.pickedCounts[selected.ID]++
		s.totalPicks++
		s.lastPickedAt = now
		return selected, nil
	}

	s.rebuildCycle(cycleAuths, state)
	if len(state.cycle) == 0 {
		return nil, &Error{Code: "auth_unavailable", Message: "no valid auth found in cycle"}
	}

	selected = state.cycle[0]
	state.head = 1
	s.lastUsed[selected.ID] = now
	s.pickedCounts[selected.ID]++
	s.totalPicks++
	s.lastPickedAt = now
	return selected, nil
}

func authWeight(a *Auth) int {
	w := authPriority(a)
	if w <= 0 {
		return 1
	}
	return w
}

// calculateWeightGCD returns the greatest common divisor of the positive weights
// across the provided auths. If any weight is 0 (the authPriority default of 1
// is enforced upstream so this is rare), the GCD falls back to 1 to keep the
// denominator safe.
func calculateWeightGCD(auths []*Auth) int {
	g := 0
	for _, a := range auths {
		w := authWeight(a)
		if w <= 0 {
			continue
		}
		if g == 0 {
			g = w
			continue
		}
		for w != 0 {
			g, w = w, g%w
		}
	}
	if g <= 0 {
		return 1
	}
	return g
}

func collectAuthModelKeys(a *Auth) []string {
	if a == nil {
		return nil
	}
	if len(a.ModelStates) > 0 {
		keys := make([]string, 0, len(a.ModelStates))
		for k := range a.ModelStates {
			if k = strings.TrimSpace(k); k != "" {
				keys = append(keys, k)
			}
		}
		if len(keys) > 0 {
			sort.Strings(keys)
			return keys
		}
	}
	if p := strings.TrimSpace(a.Provider); p != "" {
		if a.Attributes != nil {
			if v := strings.TrimSpace(a.Attributes["compat_name"]); v != "" {
				return []string{v}
			}
			if v := strings.TrimSpace(a.Attributes["provider_key"]); v != "" {
				return []string{v}
			}
		}
		return []string{p}
	}
	return nil
}

func calculateTotalWeight(auths []*Auth) int {
	total := 0
	for _, a := range auths {
		total += authWeight(a)
	}
	return total
}

func calculateWeightHash(auths []*Auth) uint64 {
	sorted := make([]*Auth, len(auths))
	copy(sorted, auths)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].ID < sorted[j].ID
	})
	h := fnv.New64()
	var buf [8]byte
	for _, a := range sorted {
		h.Write([]byte(a.ID))
		binary.LittleEndian.PutUint64(buf[:], uint64(authWeight(a)))
		h.Write(buf[:])
	}
	return h.Sum64()
}

// QueueStateEntry represents a single entry in the weight-robin queue state.
type QueueStateEntry struct {
	AuthID      string   `json:"authId"`
	Name        string   `json:"name"`
	Provider    string   `json:"provider"`
	Weight      int      `json:"weight"`
	Position    int      `json:"position"`  // Position in cycle (-1 if not in cycle)
	InCycle     bool     `json:"inCycle"`   // Whether this auth is currently in the active cycle
	Available   bool     `json:"available"`
	PickedCount uint64   `json:"pickedCount"`            // total picks served by this auth since process start
	Models      []string `json:"models,omitempty"` // Models/aliases this auth supports (existing only)
}

// QueueStateSnapshot represents the current state of the weight-robin queue.
type QueueStateSnapshot struct {
	Entries          []QueueStateEntry      `json:"entries"`
	Cycle            []CycleEntry           `json:"cycle"`
	AliasCycles      map[string][]CycleEntry `json:"aliasCycles,omitempty"` // per-alias/model independent cycles
	CurrentIdx       int                    `json:"currentIdx"`
	TotalWeight      int                    `json:"totalWeight"`     // sum(weight / GCD) of the active cycle
	GCD              int                    `json:"gcd"`              // GCD used to normalize TotalWeight; 0 if cycle is empty
	CycleLength      int                    `json:"cycleLength"`
	LastPicked       string                 `json:"lastPicked,omitempty"`
	LastPickedAt     *time.Time             `json:"lastPickedAt,omitempty"` // timestamp of the most recent successful Pick()
	TotalPicks       uint64                 `json:"totalPicks"`             // total Pick() selections served by this selector
}

// CycleEntry represents a single position in the shuffled cycle.
type CycleEntry struct {
	AuthID   string `json:"authId"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Model    string `json:"model,omitempty"` // primary model/alias for this cycle position
}

// QueueState returns a snapshot of the current queue state for the cycle
// associated with `model`. When the selector has been used by multiple
// model/alias pools, each pool has its own independent cycle and cursor;
// this snapshot reflects only the cycle for the requested model.
//
// allAuths should contain every registered auth (typically coreManager.List())
// so the snapshot reflects the full set of providers, not just auths that have
// been routed through Pick() at least once. Auths only seen in Pick() (knownAuths)
// contribute lastPicked and recent-pick metadata, but the entry list itself is
// derived from allAuths.
func (s *WeightedRobinSelector) QueueState(provider, model string, allAuths []*Auth) QueueStateSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	cycleKey := canonicalModelKey(model)

	// If model is empty, don't create a meaningless cycle.
	// Return entries only (no cycle) so frontend shows available auths without fake cycle.
	if cycleKey == "" {
		now := time.Now()
		snapshot := QueueStateSnapshot{
			TotalPicks: s.totalPicks,
		}
		entryMap := make(map[string]*QueueStateEntry)
		for _, a := range allAuths {
			if a == nil || strings.TrimSpace(a.ID) == "" {
				continue
			}
			blocked, _, _ := isAuthBlockedForModel(a, model, now)
			entryMap[a.ID] = &QueueStateEntry{
				AuthID:      a.ID,
				Name:        a.Label,
				Provider:    a.Provider,
				Weight:      authWeight(a),
				Position:    -1,
				Available:   !blocked,
				InCycle:     false,
				PickedCount: s.pickedCounts[a.ID],
				Models:      collectAuthModelKeys(a),
			}
		}
		entries := make([]QueueStateEntry, 0, len(entryMap))
		for _, e := range entryMap {
			entries = append(entries, *e)
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].Weight != entries[j].Weight {
				return entries[i].Weight > entries[j].Weight
			}
			return entries[i].AuthID < entries[j].AuthID
		})
		snapshot.Entries = entries
		if len(s.cycles) > 0 {
			snapshot.AliasCycles = make(map[string][]CycleEntry, len(s.cycles))
			for aliasKey, ac := range s.cycles {
				if ac == nil || len(ac.cycle) == 0 {
					continue
				}
				remaining := ac.cycle[ac.head:]
				if len(remaining) > 20 {
					remaining = remaining[:20]
				}
				cycleEntries := make([]CycleEntry, len(remaining))
				for i, a := range remaining {
					if a != nil {
						models := collectAuthModelKeys(a)
						model := ""
						if len(models) > 0 {
							model = models[0]
						}
						cycleEntries[i] = CycleEntry{AuthID: a.ID, Name: a.Label, Provider: a.Provider, Model: model}
					}
				}
				snapshot.AliasCycles[aliasKey] = cycleEntries
			}
		}
		return snapshot
	}

	state, hasState := s.cycles[cycleKey]

	now := time.Now()
	snapshot := QueueStateSnapshot{
		TotalPicks: s.totalPicks,
	}

	if hasState {
		snapshot.CurrentIdx = state.head
		snapshot.TotalWeight = state.totalWeight
		snapshot.GCD = state.gcd
		snapshot.CycleLength = len(state.cycle)
		if state.head > 0 && state.head <= len(state.cycle) {
			snapshot.LastPicked = state.cycle[state.head-1].ID
		}
	}
	if !s.lastPickedAt.IsZero() {
		ts := s.lastPickedAt
		snapshot.LastPickedAt = &ts
	}

	cycleIndex := make(map[string]int)
	if hasState {
		cycleIndex = make(map[string]int, len(state.cycle))
		for i, a := range state.cycle[state.head:] {
			if a != nil {
				if _, exists := cycleIndex[a.ID]; !exists {
					cycleIndex[a.ID] = i
				}
			}
		}
	}

	entryMap := make(map[string]*QueueStateEntry)
	for _, a := range allAuths {
		if a == nil || strings.TrimSpace(a.ID) == "" {
			continue
		}
		blocked, _, _ := isAuthBlockedForModel(a, model, now)
		pos, inCycle := cycleIndex[a.ID]
		if !inCycle {
			pos = -1
		}
		entryMap[a.ID] = &QueueStateEntry{
			AuthID:      a.ID,
			Name:        a.Label,
			Provider:    a.Provider,
			Weight:      authWeight(a),
			Position:    pos,
			Available:   !blocked,
			InCycle:     inCycle,
			PickedCount: s.pickedCounts[a.ID],
			Models:      collectAuthModelKeys(a),
		}
	}

	entries := make([]QueueStateEntry, 0, len(entryMap))
	for _, e := range entryMap {
		entries = append(entries, *e)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Weight != entries[j].Weight {
			return entries[i].Weight > entries[j].Weight
		}
		return entries[i].AuthID < entries[j].AuthID
	})
	snapshot.Entries = entries

	if hasState {
		remaining := state.cycle[state.head:]
		if len(remaining) > 20 {
			remaining = remaining[:20]
		}
		cycleEntries := make([]CycleEntry, len(remaining))
		for i, a := range remaining {
			if a != nil {
				models := collectAuthModelKeys(a)
				model := ""
				if len(models) > 0 {
					model = models[0]
				}
				cycleEntries[i] = CycleEntry{AuthID: a.ID, Name: a.Label, Provider: a.Provider, Model: model}
			}
		}
		snapshot.Cycle = cycleEntries
	}

	if len(s.cycles) > 0 {
		snapshot.AliasCycles = make(map[string][]CycleEntry, len(s.cycles))
		for aliasKey, ac := range s.cycles {
			if ac == nil || len(ac.cycle) == 0 {
				continue
			}
			remaining := ac.cycle[ac.head:]
			if len(remaining) > 20 {
				remaining = remaining[:20]
			}
			entries := make([]CycleEntry, len(remaining))
			for i, a := range remaining {
				if a != nil {
					models := collectAuthModelKeys(a)
					model := ""
					if len(models) > 0 {
						model = models[0]
					}
					entries[i] = CycleEntry{AuthID: a.ID, Name: a.Label, Provider: a.Provider, Model: model}
				}
			}
			snapshot.AliasCycles[aliasKey] = entries
		}
	}

	return snapshot
}

// ResetCycles clears all cached cycle state so that subsequent Pick calls
// rebuild cycles from the current auth set. This is called after config
// reloads or auth set changes to ensure stale auths are evicted.
func (s *WeightedRobinSelector) ResetCycles() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cycles = make(map[string]*aliasCycle)
	s.lastUsed = make(map[string]time.Time)
}

// unwrapWeightedRobin extracts a WeightedRobinSelector whether it is the
// top-level selector or wrapped inside a SessionAffinitySelector.
func unwrapWeightedRobin(selector Selector) (*WeightedRobinSelector, bool) {
	switch s := selector.(type) {
	case *WeightedRobinSelector:
		return s, true
	case *SessionAffinitySelector:
		wr, ok := s.fallback.(*WeightedRobinSelector)
		return wr, ok
	default:
		return nil, false
	}
}

func (s *WeightedRobinSelector) rebuildCycle(auths []*Auth, state *aliasCycle) {
	gcd := calculateWeightGCD(auths)
	total := calculateTotalWeight(auths) / gcd
	cycle := make([]*Auth, 0, total)
	for _, a := range auths {
		w := authWeight(a) / gcd
		for j := 0; j < w; j++ {
			cycle = append(cycle, a)
		}
	}
	rand.Shuffle(len(cycle), func(i, j int) {
		cycle[i], cycle[j] = cycle[j], cycle[i]
	})
	state.cycle = cycle
	state.totalWeight = total
	state.gcd = gcd
	state.weightHash = calculateWeightHash(auths)
	state.authIDs = make(map[string]struct{}, len(auths))
	for _, a := range auths {
		if a != nil {
			state.authIDs[a.ID] = struct{}{}
		}
	}
	state.head = 0
}

func isAuthBlockedForModel(auth *Auth, model string, now time.Time) (bool, blockReason, time.Time) {
	if auth == nil {
		return true, blockReasonOther, time.Time{}
	}
	if auth.Disabled || auth.Status == StatusDisabled {
		return true, blockReasonDisabled, time.Time{}
	}
	if model != "" {
		if len(auth.ModelStates) > 0 {
			state, ok := auth.ModelStates[model]
			if (!ok || state == nil) && model != "" {
				baseModel := canonicalModelKey(model)
				if baseModel != "" && baseModel != model {
					state, ok = auth.ModelStates[baseModel]
				}
			}
			if ok && state != nil {
				if state.Status == StatusDisabled {
					return true, blockReasonDisabled, time.Time{}
				}
				if state.Unavailable {
					if state.NextRetryAfter.IsZero() {
						return false, blockReasonNone, time.Time{}
					}
					if state.NextRetryAfter.After(now) {
						next := state.NextRetryAfter
						if !state.Quota.NextRecoverAt.IsZero() && state.Quota.NextRecoverAt.After(now) {
							next = state.Quota.NextRecoverAt
						}
						if next.Before(now) {
							next = now
						}
						if state.Quota.Exceeded {
							return true, blockReasonCooldown, next
						}
						return true, blockReasonOther, next
					}
				}
				return false, blockReasonNone, time.Time{}
			}
		}
		return false, blockReasonNone, time.Time{}
	}
	if auth.Unavailable && auth.NextRetryAfter.After(now) {
		next := auth.NextRetryAfter
		if !auth.Quota.NextRecoverAt.IsZero() && auth.Quota.NextRecoverAt.After(now) {
			next = auth.Quota.NextRecoverAt
		}
		if next.Before(now) {
			next = now
		}
		if auth.Quota.Exceeded {
			return true, blockReasonCooldown, next
		}
		return true, blockReasonOther, next
	}
	return false, blockReasonNone, time.Time{}
}

// sessionPattern matches Claude Code user_id format:
// user_{hash}_account__session_{uuid}
var sessionPattern = regexp.MustCompile(`_session_([a-f0-9-]+)$`)

// SessionAffinitySelector wraps another selector with session-sticky behavior.
// It extracts session ID from multiple sources and maintains session-to-auth
// mappings with automatic failover when the bound auth becomes unavailable.
type SessionAffinitySelector struct {
	fallback Selector
	cache    *SessionCache
}

// SessionAffinityConfig configures the session affinity selector.
type SessionAffinityConfig struct {
	Fallback Selector
	TTL      time.Duration
}

// NewSessionAffinitySelector creates a new session-aware selector.
func NewSessionAffinitySelector(fallback Selector) *SessionAffinitySelector {
	return NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: fallback,
		TTL:      time.Hour,
	})
}

// NewSessionAffinitySelectorWithConfig creates a selector with custom configuration.
func NewSessionAffinitySelectorWithConfig(cfg SessionAffinityConfig) *SessionAffinitySelector {
	if cfg.Fallback == nil {
		cfg.Fallback = &RoundRobinSelector{}
	}
	if cfg.TTL <= 0 {
		cfg.TTL = time.Hour
	}
	return &SessionAffinitySelector{
		fallback: cfg.Fallback,
		cache:    NewSessionCache(cfg.TTL),
	}
}

// Pick selects an auth with session affinity when possible.
// Priority for session ID extraction:
//  1. metadata.user_id (Claude Code format with _session_{uuid}) - highest priority
//  2. X-Session-ID header
//  3. Session_id header (Codex)
//  4. X-Client-Request-Id header (PI)
//  5. metadata.user_id (non-Claude Code format)
//  6. conversation_id field in request body
//  7. Stable hash from first few messages content (fallback)
//
// Note: The cache key includes provider, session ID, and model to handle cases where
// a session uses multiple models (e.g., gemini-2.5-pro and gemini-3-flash-preview)
// that may be supported by different auth credentials, and to avoid cross-provider conflicts.
func (s *SessionAffinitySelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	entry := selectorLogEntry(ctx)
	primaryID, fallbackID := extractSessionIDs(opts.Headers, opts.OriginalRequest, opts.Metadata)
	if primaryID == "" {
		entry.Debugf("session-affinity: no session ID extracted, falling back to default selector | provider=%s model=%s", provider, model)
		return s.fallback.Pick(ctx, provider, model, opts, auths)
	}

	now := time.Now()
	available, err := getAvailableAuths(auths, provider, model, now)
	if err != nil {
		return nil, err
	}

	cacheKey := provider + "::" + primaryID + "::" + model

	if cachedAuthID, ok := s.cache.GetAndRefresh(cacheKey); ok {
		for _, auth := range available {
			if auth.ID == cachedAuthID {
				entry.Infof("session-affinity: cache hit | session=%s auth=%s provider=%s model=%s", truncateSessionID(primaryID), auth.ID, provider, model)
				return auth, nil
			}
		}
		// Cached auth not available, reselect via fallback selector for even distribution
		auth, err := s.fallback.Pick(ctx, provider, model, opts, auths)
		if err != nil {
			return nil, err
		}
		s.cache.Set(cacheKey, auth.ID)
		entry.Infof("session-affinity: cache hit but auth unavailable, reselected | session=%s auth=%s provider=%s model=%s", truncateSessionID(primaryID), auth.ID, provider, model)
		return auth, nil
	}

	if fallbackID != "" && fallbackID != primaryID {
		fallbackKey := provider + "::" + fallbackID + "::" + model
		if cachedAuthID, ok := s.cache.Get(fallbackKey); ok {
			for _, auth := range available {
				if auth.ID == cachedAuthID {
					s.cache.Set(cacheKey, auth.ID)
					entry.Infof("session-affinity: fallback cache hit | session=%s fallback=%s auth=%s provider=%s model=%s", truncateSessionID(primaryID), truncateSessionID(fallbackID), auth.ID, provider, model)
					return auth, nil
				}
			}
		}
	}

	auth, err := s.fallback.Pick(ctx, provider, model, opts, auths)
	if err != nil {
		return nil, err
	}
	s.cache.Set(cacheKey, auth.ID)
	entry.Infof("session-affinity: cache miss, new binding | session=%s auth=%s provider=%s model=%s", truncateSessionID(primaryID), auth.ID, provider, model)
	return auth, nil
}

func selectorLogEntry(ctx context.Context) *log.Entry {
	if ctx == nil {
		return log.NewEntry(log.StandardLogger())
	}
	if reqID := logging.GetRequestID(ctx); reqID != "" {
		return log.WithField("request_id", reqID)
	}
	return log.NewEntry(log.StandardLogger())
}

// truncateSessionID shortens session ID for logging (first 8 chars + "...")
func truncateSessionID(id string) string {
	if len(id) <= 20 {
		return id
	}
	return id[:8] + "..."
}

// Stop releases resources held by the selector.
func (s *SessionAffinitySelector) Stop() {
	if s.cache != nil {
		s.cache.Stop()
	}
}

// InvalidateAuth removes all session bindings for a specific auth.
// Called when an auth becomes rate-limited or unavailable.
func (s *SessionAffinitySelector) InvalidateAuth(authID string) {
	if s.cache != nil {
		s.cache.InvalidateAuth(authID)
	}
}

// ExtractSessionID extracts session identifier from multiple sources.
// Priority order:
//  1. metadata.user_id (Claude Code format with _session_{uuid}) - highest priority for Claude Code clients
//  2. X-Session-ID header
//  3. Session_id header (Codex)
//  4. X-Client-Request-Id header (PI)
//  5. metadata.user_id (non-Claude Code format)
//  6. conversation_id field in request body
//  7. Stable hash from first few messages content (fallback)
func ExtractSessionID(headers http.Header, payload []byte, metadata map[string]any) string {
	primary, _ := extractSessionIDs(headers, payload, metadata)
	return primary
}

// extractSessionIDs returns (primaryID, fallbackID) for session affinity.
// primaryID: full hash including assistant response (stable after first turn)
// fallbackID: short hash without assistant (used to inherit binding from first turn)
func extractSessionIDs(headers http.Header, payload []byte, metadata map[string]any) (string, string) {
	// 1. metadata.user_id with Claude Code session format (highest priority)
	if len(payload) > 0 {
		userID := gjson.GetBytes(payload, "metadata.user_id").String()
		if userID != "" {
			// Old format: user_{hash}_account__session_{uuid}
			if matches := sessionPattern.FindStringSubmatch(userID); len(matches) >= 2 {
				id := "claude:" + matches[1]
				return id, ""
			}
			// New format: JSON object with session_id field
			// e.g. {"device_id":"...","account_uuid":"...","session_id":"uuid"}
			if len(userID) > 0 && userID[0] == '{' {
				if sid := gjson.Get(userID, "session_id").String(); sid != "" {
					return "claude:" + sid, ""
				}
			}
		}
	}

	// 2. X-Session-ID header
	if headers != nil {
		if sid := headers.Get("X-Session-ID"); sid != "" {
			return "header:" + sid, ""
		}
	}

	// 3. Session_id header (Codex)
	if headers != nil {
		if sid := headers.Get("Session-Id"); sid != "" {
			return "codex:" + sid, ""
		}
		if sid := headers.Get("Session_id"); sid != "" {
			return "codex:" + sid, ""
		}
	}

	// 4. X-Client-Request-Id header (PI)
	if headers != nil {
		if rid := headers.Get("X-Client-Request-Id"); rid != "" {
			return "clientreq:" + rid, ""
		}
	}

	if len(payload) == 0 {
		return "", ""
	}

	// 6. metadata.user_id (non-Claude Code format)
	userID := gjson.GetBytes(payload, "metadata.user_id").String()
	if userID != "" {
		return "user:" + userID, ""
	}

	// 7. conversation_id field
	if convID := gjson.GetBytes(payload, "conversation_id").String(); convID != "" {
		return "conv:" + convID, ""
	}

	// 8. Hash-based fallback from message content
	return extractMessageHashIDs(payload)
}

func extractMessageHashIDs(payload []byte) (primaryID, fallbackID string) {
	var systemPrompt, firstUserMsg, firstAssistantMsg string

	// OpenAI/Claude messages format
	messages := gjson.GetBytes(payload, "messages")
	if messages.Exists() && messages.IsArray() {
		messages.ForEach(func(_, msg gjson.Result) bool {
			role := msg.Get("role").String()
			content := extractMessageContent(msg.Get("content"))
			if content == "" {
				return true
			}

			switch role {
			case "system":
				if systemPrompt == "" {
					systemPrompt = truncateString(content, 100)
				}
			case "user":
				if firstUserMsg == "" {
					firstUserMsg = truncateString(content, 100)
				}
			case "assistant":
				if firstAssistantMsg == "" {
					firstAssistantMsg = truncateString(content, 100)
				}
			}

			if systemPrompt != "" && firstUserMsg != "" && firstAssistantMsg != "" {
				return false
			}
			return true
		})
	}

	// Claude API: top-level "system" field (array or string)
	if systemPrompt == "" {
		topSystem := gjson.GetBytes(payload, "system")
		if topSystem.Exists() {
			if topSystem.IsArray() {
				topSystem.ForEach(func(_, part gjson.Result) bool {
					if text := part.Get("text").String(); text != "" && systemPrompt == "" {
						systemPrompt = truncateString(text, 100)
						return false
					}
					return true
				})
			} else if topSystem.Type == gjson.String {
				systemPrompt = truncateString(topSystem.String(), 100)
			}
		}
	}

	// Gemini format
	if systemPrompt == "" && firstUserMsg == "" {
		sysInstr := gjson.GetBytes(payload, "systemInstruction.parts")
		if sysInstr.Exists() && sysInstr.IsArray() {
			sysInstr.ForEach(func(_, part gjson.Result) bool {
				if text := part.Get("text").String(); text != "" && systemPrompt == "" {
					systemPrompt = truncateString(text, 100)
					return false
				}
				return true
			})
		}

		contents := gjson.GetBytes(payload, "contents")
		if contents.Exists() && contents.IsArray() {
			contents.ForEach(func(_, msg gjson.Result) bool {
				role := msg.Get("role").String()
				msg.Get("parts").ForEach(func(_, part gjson.Result) bool {
					text := part.Get("text").String()
					if text == "" {
						return true
					}
					switch role {
					case "user":
						if firstUserMsg == "" {
							firstUserMsg = truncateString(text, 100)
						}
					case "model":
						if firstAssistantMsg == "" {
							firstAssistantMsg = truncateString(text, 100)
						}
					}
					return false
				})
				if firstUserMsg != "" && firstAssistantMsg != "" {
					return false
				}
				return true
			})
		}
	}

	// OpenAI Responses API format (v1/responses)
	if systemPrompt == "" && firstUserMsg == "" {
		if instr := gjson.GetBytes(payload, "instructions").String(); instr != "" {
			systemPrompt = truncateString(instr, 100)
		}

		input := gjson.GetBytes(payload, "input")
		if input.Exists() && input.IsArray() {
			input.ForEach(func(_, item gjson.Result) bool {
				itemType := item.Get("type").String()
				if itemType == "reasoning" {
					return true
				}
				// Skip non-message typed items (function_call, function_call_output, etc.)
				// but allow items with no type that have a role (inline message format).
				if itemType != "" && itemType != "message" {
					return true
				}

				role := item.Get("role").String()
				if itemType == "" && role == "" {
					return true
				}

				// Handle both string content and array content (multimodal).
				content := item.Get("content")
				var text string
				if content.Type == gjson.String {
					text = content.String()
				} else {
					text = extractResponsesAPIContent(content)
				}
				if text == "" {
					return true
				}

				switch role {
				case "developer", "system":
					if systemPrompt == "" {
						systemPrompt = truncateString(text, 100)
					}
				case "user":
					if firstUserMsg == "" {
						firstUserMsg = truncateString(text, 100)
					}
				case "assistant":
					if firstAssistantMsg == "" {
						firstAssistantMsg = truncateString(text, 100)
					}
				}

				if firstUserMsg != "" && firstAssistantMsg != "" {
					return false
				}
				return true
			})
		}
	}

	if systemPrompt == "" && firstUserMsg == "" {
		return "", ""
	}

	shortHash := computeSessionHash(systemPrompt, firstUserMsg, "")
	if firstAssistantMsg == "" {
		return shortHash, ""
	}

	fullHash := computeSessionHash(systemPrompt, firstUserMsg, firstAssistantMsg)
	return fullHash, shortHash
}

func computeSessionHash(systemPrompt, userMsg, assistantMsg string) string {
	h := fnv.New64a()
	if systemPrompt != "" {
		h.Write([]byte("sys:" + systemPrompt + "\n"))
	}
	if userMsg != "" {
		h.Write([]byte("usr:" + userMsg + "\n"))
	}
	if assistantMsg != "" {
		h.Write([]byte("ast:" + assistantMsg + "\n"))
	}
	return fmt.Sprintf("msg:%016x", h.Sum64())
}

func truncateString(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen]
	}
	return s
}

// extractMessageContent extracts text content from a message content field.
// Handles both string content and array content (multimodal messages).
// For array content, extracts text from all text-type elements.
func extractMessageContent(content gjson.Result) string {
	// String content: "Hello world"
	if content.Type == gjson.String {
		return content.String()
	}

	// Array content: [{"type":"text","text":"Hello"},{"type":"image",...}]
	if content.IsArray() {
		var texts []string
		content.ForEach(func(_, part gjson.Result) bool {
			// Handle Claude format: {"type":"text","text":"content"}
			if part.Get("type").String() == "text" {
				if text := part.Get("text").String(); text != "" {
					texts = append(texts, text)
				}
			}
			// Handle OpenAI format: {"type":"text","text":"content"}
			// Same structure as Claude, already handled above
			return true
		})
		if len(texts) > 0 {
			return strings.Join(texts, " ")
		}
	}

	return ""
}

func extractResponsesAPIContent(content gjson.Result) string {
	if !content.IsArray() {
		return ""
	}
	var texts []string
	content.ForEach(func(_, part gjson.Result) bool {
		partType := part.Get("type").String()
		if partType == "input_text" || partType == "output_text" || partType == "text" {
			if text := part.Get("text").String(); text != "" {
				texts = append(texts, text)
			}
		}
		return true
	})
	if len(texts) > 0 {
		return strings.Join(texts, " ")
	}
	return ""
}

// extractSessionID is kept for backward compatibility.
// Deprecated: Use ExtractSessionID instead.
func extractSessionID(payload []byte) string {
	return ExtractSessionID(nil, payload, nil)
}
