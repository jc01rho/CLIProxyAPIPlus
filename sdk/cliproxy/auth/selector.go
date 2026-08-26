package auth

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/credentialweight"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
)

// RoundRobinSelector provides a simple provider scoped round-robin selection strategy.
//
// Rotation continues from the identity of the previous pick rather than from a numeric
// index. Candidate slices shrink whenever a retry excludes already tried credentials or a
// credential enters cooldown, and indexing a monotonic counter into a shrinking slice
// silently re-seats the rotation, which starves some credentials and hammers others.
type RoundRobinSelector struct {
	mu         sync.Mutex
	lastPicked map[string]string
	maxKeys    int
}

// WeightedRoundRobinSelector provides smooth weighted round-robin selection.
type WeightedRoundRobinSelector struct {
	mu      sync.Mutex
	states  map[string]*smoothWeightedState
	maxKeys int
}

type smoothWeightedState struct {
	current map[string]int64
	weights map[string]int64
}

type weightedSelectorStateModelKey struct{}

func withWeightedSelectorStateModel(ctx context.Context, selector Selector, routeModel string) context.Context {
	if _, ok := selector.(*WeightedRoundRobinSelector); !ok || strings.TrimSpace(routeModel) == "" {
		return ctx
	}
	return context.WithValue(ctx, weightedSelectorStateModelKey{}, routeModel)
}

func weightedSelectorStateModel(ctx context.Context, availabilityModel string) string {
	if ctx != nil {
		if routeModel, ok := ctx.Value(weightedSelectorStateModelKey{}).(string); ok && strings.TrimSpace(routeModel) != "" {
			return routeModel
		}
	}
	return availabilityModel
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
	cycle       []*Auth             // shuffled cycle, length = normalized totalWeight
	head        int                 // pop position (front of queue)
	totalWeight int                 // total weight when cycle was built (GCD-normalized)
	gcd         int                 // GCD used to normalize totalWeight; 0 if cycle is empty
	weightHash  uint64              // FNV hash of auth IDs × weights when cycle was built
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
	primaryPriorityBonus                 = 1_000_000
	prefilteredAuthCandidatesMetadataKey = "__cliproxy_prefiltered_auth_candidates"
)

func authCandidatesPrefiltered(opts cliproxyexecutor.Options) bool {
	if len(opts.Metadata) == 0 {
		return false
	}
	value, ok := opts.Metadata[prefilteredAuthCandidatesMetadataKey].(bool)
	return ok && value
}

func markAuthCandidatesPrefiltered(opts cliproxyexecutor.Options) cliproxyexecutor.Options {
	meta := make(map[string]any, len(opts.Metadata)+1)
	for key, value := range opts.Metadata {
		meta[key] = value
	}
	meta[prefilteredAuthCandidatesMetadataKey] = true
	opts.Metadata = meta
	return opts
}

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

func authWeight(auth *Auth) int64 {
	if auth == nil {
		return credentialweight.Default
	}
	if rawWeight, ok := auth.Attributes[AttributeWeight]; ok && strings.TrimSpace(rawWeight) != "" {
		weight, errParse := credentialweight.ParseString(rawWeight)
		if errParse != nil {
			return 0
		}
		return weight
	}
	if rawWeight, ok := auth.Metadata[AttributeWeight]; ok {
		weight, errParse := credentialweight.ParseValue(rawWeight)
		if errParse != nil {
			return 0
		}
		return weight
	}
	return credentialweight.Default
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
	return getAvailableAuthsWithPriorityMode(auths, provider, model, now, false)
}

func getAvailableAuthsAcrossPriorities(auths []*Auth, provider, model string, now time.Time) ([]*Auth, error) {
	return getAvailableAuthsWithPriorityMode(auths, provider, model, now, true)
}

func getAvailableAuthsWithPriorityMode(auths []*Auth, provider, model string, now time.Time, allPriorities bool) ([]*Auth, error) {
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

	return availableAuthsFromPriorityBuckets(availableByPriority, allPriorities), nil
}

// availableAuthsFromPriorityBuckets flattens availability buckets into a stable, ID-sorted slice.
// When allPriorities is false only the highest available priority tier is returned.
// When allPriorities is true every tier is merged, so the result carries no priority ordering:
// use it for membership checks or feed it to highestPriorityAuths, never as a priority-ordered
// selection order.
func availableAuthsFromPriorityBuckets(availableByPriority map[int][]*Auth, allPriorities bool) []*Auth {
	var candidates []*Auth
	if allPriorities {
		total := 0
		for _, bucket := range availableByPriority {
			total += len(bucket)
		}
		candidates = make([]*Auth, 0, total)
		for _, bucket := range availableByPriority {
			candidates = append(candidates, bucket...)
		}
	} else {
		bestPriority := 0
		found := false
		for priority := range availableByPriority {
			if !found || priority > bestPriority {
				bestPriority = priority
				found = true
			}
		}
		bucket := availableByPriority[bestPriority]
		candidates = make([]*Auth, 0, len(bucket))
		candidates = append(candidates, bucket...)
	}
	if len(candidates) > 1 {
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	}
	return candidates
}

// highestPriorityAuths narrows an availability slice to its highest priority tier while
// preserving the input order. The input slice is returned unchanged when every candidate
// already shares the highest priority, so the common single-tier case allocates nothing.
func highestPriorityAuths(auths []*Auth) []*Auth {
	if len(auths) <= 1 {
		return auths
	}
	bestPriority := 0
	bestCount := 0
	for _, auth := range auths {
		priority := authPriority(auth)
		switch {
		case bestCount == 0 || priority > bestPriority:
			bestPriority = priority
			bestCount = 1
		case priority == bestPriority:
			bestCount++
		}
	}
	if bestCount == len(auths) {
		return auths
	}
	highest := make([]*Auth, 0, bestCount)
	for _, auth := range auths {
		if authPriority(auth) == bestPriority {
			highest = append(highest, auth)
		}
	}
	return highest
}

func getPrefilteredAvailableAuths(auths []*Auth) ([]*Auth, error) {
	if len(auths) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth candidates"}
	}
	available := make([]*Auth, 0, len(auths))
	for _, auth := range auths {
		if auth != nil {
			available = append(available, auth)
		}
	}
	if len(available) == 0 {
		return nil, &Error{Code: "auth_unavailable", Message: "no auth available"}
	}
	return available, nil
}

func availableAuthsForSelector(auths []*Auth, provider, model string, opts cliproxyexecutor.Options, now time.Time) ([]*Auth, error) {
	if authCandidatesPrefiltered(opts) {
		return getPrefilteredAvailableAuths(auths)
	}
	return getAvailableAuths(auths, provider, model, now)
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
	now := time.Now()
	available, err := availableAuthsForSelector(auths, provider, model, opts, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	key := provider + ":" + canonicalModelKey(model)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastPicked == nil {
		s.lastPicked = make(map[string]string)
	}
	limit := s.maxKeys
	if limit <= 0 {
		limit = 4096
	}

	s.ensureRotationKey(key, limit)
	picked := available[successorIndex(available, s.lastPicked[key])]
	s.lastPicked[key] = picked.ID
	return picked, nil
}

// successorIndex returns the index of the first candidate ordered after lastID, wrapping to
// the start of the ring. Candidates arrive sorted by ID, so this resumes the rotation at the
// credential that follows the previous pick even when candidates were filtered out in
// between. An empty lastID starts at the head.
func successorIndex(available []*Auth, lastID string) int {
	if lastID == "" {
		return 0
	}
	index := sort.Search(len(available), func(i int) bool { return available[i].ID > lastID })
	if index >= len(available) {
		return 0
	}
	return index
}

// ensureRotationKey ensures the rotation map has capacity for the given key.
// Must be called with s.mu held.
func (s *RoundRobinSelector) ensureRotationKey(key string, limit int) {
	if _, ok := s.lastPicked[key]; !ok && len(s.lastPicked) >= limit {
		s.lastPicked = make(map[string]string)
	}
}

func positiveWeightAuths(auths []*Auth) []*Auth {
	weightedCandidates := make([]*Auth, 0, len(auths))
	for _, auth := range auths {
		if authWeight(auth) > 0 {
			weightedCandidates = append(weightedCandidates, auth)
		}
	}
	return weightedCandidates
}

// Pick selects the next available auth using smooth weighted round-robin.
func (s *WeightedRoundRobinSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = opts
	available, errAvailable := getAvailableAuths(positiveWeightAuths(auths), provider, model, time.Now())
	if errAvailable != nil {
		return nil, errAvailable
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	stateModel := weightedSelectorStateModel(ctx, model)
	key := provider + ":" + canonicalModelKey(stateModel)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.states == nil {
		s.states = make(map[string]*smoothWeightedState)
	}
	limit := s.maxKeys
	if limit <= 0 {
		limit = 4096
	}
	if _, ok := s.states[key]; !ok && len(s.states) >= limit {
		s.states = make(map[string]*smoothWeightedState)
	}
	state := s.states[key]
	if state == nil {
		state = &smoothWeightedState{}
		s.states[key] = state
	}
	weights := authWeightVector(available)
	state.prepare(weights)
	picked := pickSmoothWeightedAuth(available, state.current)
	if picked == nil {
		return nil, &Error{Code: "auth_unavailable", Message: "no auth available with positive weight"}
	}
	return picked, nil
}

// maxSmoothWeightedStateEntries bounds a single accumulator map so credentials that are
// removed permanently cannot leak entries. Real pools stay far below this bound, so the
// transient subsets produced by retry exclusions and cooldowns are never pruned.
const maxSmoothWeightedStateEntries = 1024

// prepare syncs the configured weights into the state without discarding accumulated
// credits. Credits are reset only when a credential's configured weight actually changes,
// never when the candidate set shrinks temporarily (retry exclusions, cooldowns, session
// affinity), because discarding credits there would collapse selection onto the first
// candidate in slice order.
func (s *smoothWeightedState) prepare(weights map[string]int64) {
	if s.current == nil || weightsConfigChanged(s.weights, weights) {
		s.current = make(map[string]int64, len(weights))
	}
	if s.weights == nil {
		s.weights = make(map[string]int64, len(weights))
	}
	for authID, weight := range weights {
		s.weights[authID] = weight
	}
	s.pruneStale(weights)
}

// pruneStale drops entries for credentials outside the current candidate set, but only
// once a map exceeds the safety bound, so ordinary transient exclusions keep their credits.
func (s *smoothWeightedState) pruneStale(weights map[string]int64) {
	if len(s.current) <= maxSmoothWeightedStateEntries && len(s.weights) <= maxSmoothWeightedStateEntries {
		return
	}
	for authID := range s.current {
		if _, ok := weights[authID]; !ok {
			delete(s.current, authID)
		}
	}
	for authID := range s.weights {
		if _, ok := weights[authID]; !ok {
			delete(s.weights, authID)
		}
	}
}

// weightsConfigChanged reports whether any credential present in both vectors has a
// different configured weight. Credentials that are merely missing from one side are
// ignored, since a candidate subset is not a configuration change.
func weightsConfigChanged(left, right map[string]int64) bool {
	if len(left) == 0 {
		return false
	}
	for authID, weight := range right {
		if previous, ok := left[authID]; ok && previous != weight {
			return true
		}
	}
	return false
}

func authWeightVector(auths []*Auth) map[string]int64 {
	weights := make(map[string]int64, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		if weight := authWeight(auth); weight > 0 {
			weights[auth.ID] = weight
		}
	}
	return weights
}

func pickSmoothWeightedAuth(auths []*Auth, current map[string]int64) *Auth {
	var picked *Auth
	var pickedCurrent int64
	var totalWeight int64
	for _, auth := range auths {
		weight := authWeight(auth)
		if auth == nil || weight <= 0 {
			continue
		}
		current[auth.ID] = saturatingAddInt64(current[auth.ID], weight)
		totalWeight = saturatingAddInt64(totalWeight, weight)
		if picked == nil || current[auth.ID] > pickedCurrent {
			picked = auth
			pickedCurrent = current[auth.ID]
		}
	}
	if picked == nil {
		return nil
	}
	current[picked.ID] = saturatingAddInt64(current[picked.ID], -totalWeight)
	return picked
}

func saturatingAddInt64(value, delta int64) int64 {
	if delta > 0 && value > math.MaxInt64-delta {
		return math.MaxInt64
	}
	if delta < 0 && value < math.MinInt64-delta {
		return math.MinInt64
	}
	return value + delta
}

// Pick selects the first available auth for the provider in a deterministic manner.
func (s *FillFirstSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	now := time.Now()
	available, err := availableAuthsForSelector(auths, provider, model, opts, now)
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
	if authCandidatesPrefiltered(opts) {
		var errAvailable error
		available, errAvailable = getPrefilteredAvailableAuths(auths)
		if errAvailable != nil {
			return nil, errAvailable
		}
	}
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

func legacyAuthWeight(a *Auth) int {
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
		w := legacyAuthWeight(a)
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
		total += legacyAuthWeight(a)
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
		binary.LittleEndian.PutUint64(buf[:], uint64(legacyAuthWeight(a)))
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
	Position    int      `json:"position"` // Position in cycle (-1 if not in cycle)
	InCycle     bool     `json:"inCycle"`  // Whether this auth is currently in the active cycle
	Available   bool     `json:"available"`
	PickedCount uint64   `json:"pickedCount"`      // total picks served by this auth since process start
	Models      []string `json:"models,omitempty"` // Models/aliases this auth supports (existing only)
}

// QueueStateSnapshot represents the current state of the weight-robin queue.
type QueueStateSnapshot struct {
	Entries      []QueueStateEntry       `json:"entries"`
	Cycle        []CycleEntry            `json:"cycle"`
	AliasCycles  map[string][]CycleEntry `json:"aliasCycles,omitempty"` // per-alias/model independent cycles
	CurrentIdx   int                     `json:"currentIdx"`
	TotalWeight  int                     `json:"totalWeight"` // sum(weight / GCD) of the active cycle
	GCD          int                     `json:"gcd"`         // GCD used to normalize TotalWeight; 0 if cycle is empty
	CycleLength  int                     `json:"cycleLength"`
	LastPicked   string                  `json:"lastPicked,omitempty"`
	LastPickedAt *time.Time              `json:"lastPickedAt,omitempty"` // timestamp of the most recent successful Pick()
	TotalPicks   uint64                  `json:"totalPicks"`             // total Pick() selections served by this selector
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
				Weight:      legacyAuthWeight(a),
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
			Weight:      legacyAuthWeight(a),
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

// UnwrapWeightedRobin extracts a WeightedRobinSelector whether it is the
// top-level selector or wrapped inside a SessionAffinitySelector.
func UnwrapWeightedRobin(selector Selector) (*WeightedRobinSelector, bool) {
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
		w := legacyAuthWeight(a) / gcd
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
	if auth.Quota.Exceeded && auth.Quota.Reason == "credential_quota" && auth.Quota.NextRecoverAt.After(now) {
		return true, blockReasonCooldown, auth.Quota.NextRecoverAt
	}
	if model != "" {
		if len(auth.ModelStates) > 0 {
			modelKey := canonicalModelKey(model)
			matched := false
			blocked := false
			blockedReason := blockReasonNone
			nextRetry := time.Time{}
			for stateModel, state := range auth.ModelStates {
				if state == nil || canonicalModelKey(stateModel) != modelKey {
					continue
				}
				matched = true
				if state.Status == StatusDisabled {
					return true, blockReasonDisabled, time.Time{}
				}
				stateBlocked, reason, next := availabilityBlock(state.Unavailable, state.Quota.Exceeded, state.NextRetryAfter, state.Quota.NextRecoverAt, now)
				if !stateBlocked {
					continue
				}
				if next.IsZero() {
					return true, reason, time.Time{}
				}
				if !blocked || next.After(nextRetry) || (next.Equal(nextRetry) && reason == blockReasonCooldown) {
					blocked = true
					blockedReason = reason
					nextRetry = next
				}
			}
			if matched {
				return blocked, blockedReason, nextRetry
			}
			return false, blockReasonNone, time.Time{}
		}
		return availabilityBlock(auth.Unavailable, auth.Quota.Exceeded, auth.NextRetryAfter, auth.Quota.NextRecoverAt, now)
	}
	return availabilityBlock(auth.Unavailable, auth.Quota.Exceeded, auth.NextRetryAfter, auth.Quota.NextRecoverAt, now)
}

func availabilityBlock(unavailable, quotaExceeded bool, nextRetryAfter, nextRecoverAt, now time.Time) (bool, blockReason, time.Time) {
	if !unavailable && !quotaExceeded {
		return false, blockReasonNone, time.Time{}
	}

	hasRecoveryTime := !nextRetryAfter.IsZero() || !nextRecoverAt.IsZero()
	var next time.Time
	for _, candidate := range []time.Time{nextRetryAfter, nextRecoverAt} {
		if candidate.After(now) && (next.IsZero() || candidate.After(next)) {
			next = candidate
		}
	}
	if !next.IsZero() {
		if quotaExceeded {
			return true, blockReasonCooldown, next
		}
		return true, blockReasonOther, next
	}
	if hasRecoveryTime {
		return false, blockReasonNone, time.Time{}
	}
	return true, blockReasonOther, time.Time{}
}

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
// Explicit Claude Code, Codex, OpenCode, pi, and request-body session signals
// precede execution metadata, stable derived identity, and the legacy hash fallback.
//
// An established binding outranks credential priority: a bound credential that is still
// available is reused even when a higher-priority credential recovers. Credential priority
// applies to cold bindings, requests without a session, and genuine bound-credential
// failover, so the fallback selector only ever receives the highest available priority tier.
//
// Note: The cache key includes provider, session ID, and model to handle cases where
// a session uses multiple models (e.g., gemini-2.5-pro and gemini-3-flash-preview)
// that may be supported by different auth credentials, and to avoid cross-provider conflicts.
func (s *SessionAffinitySelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	entry := selectorLogEntry(ctx)
	if opts.Metadata == nil {
		opts.Metadata = make(map[string]any)
	}
	opts.Metadata[cliproxyexecutor.SessionAffinityProviderMetadataKey] = provider
	opts.Metadata[cliproxyexecutor.SessionAffinityModelMetadataKey] = model
	primaryID, fallbackID := extractSessionIDs(opts.Headers, opts.OriginalRequest, opts.Metadata)
	now := time.Now()
	availabilityCandidates := auths
	if _, weighted := s.fallback.(*WeightedRoundRobinSelector); weighted {
		availabilityCandidates = positiveWeightAuths(auths)
	}
	// Local: prefilter-aware availability pass so the session-affinity wrapper can
	// sit around the weighted-robin selector without re-filtering the prefiltered
	// candidate set or losing priority semantics. The membership check must see
	// every available auth across priority tiers (not just the highest tier) so a
	// bound auth at any priority stays sticky while higher-priority auths recover.
	var available []*Auth
	var err error
	if authCandidatesPrefiltered(opts) {
		available, err = getPrefilteredAvailableAuths(availabilityCandidates)
	} else {
		available, err = getAvailableAuthsWithPriorityMode(availabilityCandidates, provider, model, now, true)
	}
	if err != nil {
		return nil, err
	}
	// Upstream: no session ID to bind to, defer immediately to the fallback selector.
	if primaryID == "" {
		fallbackAuths := highestPriorityAuths(available)
		entry.Debugf("session-affinity: no session ID extracted, falling back to default selector | provider=%s model=%s", provider, model)
		return s.fallback.Pick(ctx, provider, model, opts, fallbackAuths)
	}
	fallbackAuths := highestPriorityAuths(available)

	modelKey := canonicalModelKey(model)
	cacheKey := provider + "::" + primaryID + "::" + modelKey
	fallbackKey := ""
	if fallbackID != "" && fallbackID != primaryID {
		fallbackKey = provider + "::" + fallbackID + "::" + modelKey
	}
	bind := func(authID string) {
		if fallbackKey != "" {
			s.cache.SetAliases(authID, cacheKey, fallbackKey)
			return
		}
		s.cache.Set(cacheKey, authID)
	}

	if cachedAuthID, ok := s.cache.GetAndRefresh(cacheKey); ok {
		for _, auth := range available {
			if auth.ID == cachedAuthID {
				bind(auth.ID)
				entry.Infof("session-affinity: cache hit | session=%s auth=%s provider=%s model=%s", truncateSessionID(primaryID), auth.ID, provider, model)
				return auth, nil
			}
		}
		// Cached auth not available, reselect via fallback selector for even distribution
		auth, err := s.fallback.Pick(ctx, provider, model, opts, fallbackAuths)
		if err != nil {
			return nil, err
		}
		bind(auth.ID)
		entry.Infof("session-affinity: cache hit but auth unavailable, reselected | session=%s auth=%s provider=%s model=%s", truncateSessionID(primaryID), auth.ID, provider, model)
		return auth, nil
	}

	if fallbackKey != "" {
		if cachedAuthID, ok := s.cache.Get(fallbackKey); ok {
			for _, auth := range available {
				if auth.ID == cachedAuthID {
					bind(auth.ID)
					entry.Infof("session-affinity: fallback cache hit | session=%s fallback=%s auth=%s provider=%s model=%s", truncateSessionID(primaryID), truncateSessionID(fallbackID), auth.ID, provider, model)
					return auth, nil
				}
			}
		}
	}

	auth, err := s.fallback.Pick(ctx, provider, model, opts, fallbackAuths)
	if err != nil {
		return nil, err
	}
	bind(auth.ID)
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

// OnResult handles session affinity binding or release based on execution outcome.
func (s *SessionAffinitySelector) OnResult(res Result) {
	if s == nil || s.cache == nil || res.AuthID == "" {
		return
	}
	primaryID, fallbackID := extractSessionIDs(res.Options.Headers, res.Options.OriginalRequest, res.Options.Metadata)
	if primaryID == "" && fallbackID == "" {
		return
	}

	ns := res.Provider
	if raw, ok := res.Options.Metadata[cliproxyexecutor.SessionAffinityProviderMetadataKey].(string); ok && raw != "" {
		ns = raw
	}
	nsModel := canonicalModelKey(res.Model)
	if raw, ok := res.Options.Metadata[cliproxyexecutor.SessionAffinityModelMetadataKey].(string); ok && raw != "" {
		nsModel = canonicalModelKey(raw)
	}

	cacheKey := ns + "::" + primaryID + "::" + nsModel
	var fallbackKey string
	if fallbackID != "" && fallbackID != primaryID {
		fallbackKey = ns + "::" + fallbackID + "::" + nsModel
	}
	if res.Success {
		s.cache.Touch(cacheKey, res.AuthID)
		if fallbackKey != "" {
			s.cache.Touch(fallbackKey, res.AuthID)
		}
		return
	}

	if res.Error != nil && shouldSkipCredentialCooldown(res.Error) {
		return
	}

	s.cache.CompareAndDelete(cacheKey, res.AuthID)
	if fallbackKey != "" {
		s.cache.CompareAndDelete(fallbackKey, res.AuthID)
	}
}

// normalizedSessionCandidate validates an explicit client-provided session signal.
// It keeps opaque printable IDs intact while rejecting values that are unsafe or
// implausibly large for routing keys and logs.
func normalizedSessionCandidate(raw string) string {
	return cliproxysession.NormalizeExplicitID(raw)
}

func sessionHeaderValue(headers http.Header, name string) string {
	if headers == nil {
		return ""
	}
	if value := normalizedSessionCandidate(headers.Get(name)); value != "" {
		return value
	}
	for key, values := range headers {
		if !strings.EqualFold(key, name) {
			continue
		}
		for _, raw := range values {
			if value := normalizedSessionCandidate(raw); value != "" {
				return value
			}
		}
	}
	return ""
}

// ExtractSessionID extracts a session identifier from explicit client signals,
// then falls back to execution metadata, derived identity, and message history.
// Priority order:
//  1. X-Claude-Code-Session-Id
//  2. Claude Code metadata.user_id session
//  3. Session-Id / Session_id (Codex and compatible clients)
//  4. X-Session-ID
//  5. X-Session-Affinity (OpenCode)
//  6. X-Client-Request-Id (pi Responses)
//  7. session_id / sessionId
//  8. prompt_cache_key, with conversation / conversation.id as an alias
//  9. metadata.user_id and conversation_id legacy body fields
//  10. explicit execution session metadata
//  11. stable context-derived session identity
//  12. stable hash from initial message content
func ExtractSessionID(headers http.Header, payload []byte, metadata map[string]any) string {
	primary, _ := extractSessionIDs(headers, payload, metadata)
	return primary
}

// extractSessionIDs returns (primaryID, fallbackID) for session affinity.
// fallbackID preserves an earlier binding when a stronger body identifier appears
// later, and lets callers bind both identifiers when both are present.
func extractSessionIDs(headers http.Header, payload []byte, metadata map[string]any) (string, string) {
	if sid := sessionHeaderValue(headers, "X-Claude-Code-Session-Id"); sid != "" {
		return "claude:" + sid, ""
	}
	if sid := cliproxysession.ClaudeMetadataSessionID(payload); sid != "" {
		return "claude:" + sid, ""
	}
	if sid := sessionHeaderValue(headers, "Session-Id"); sid != "" {
		return "codex:" + sid, ""
	}
	if sid := sessionHeaderValue(headers, "Session_id"); sid != "" {
		return "codex:" + sid, ""
	}
	if sid := sessionHeaderValue(headers, "X-Session-ID"); sid != "" {
		return "header:" + sid, ""
	}
	if sid := sessionHeaderValue(headers, "X-Session-Affinity"); sid != "" {
		return "affinity:" + sid, ""
	}
	if sid := sessionHeaderValue(headers, "X-Client-Request-Id"); sid != "" {
		return "clientreq:" + sid, ""
	}

	if len(payload) > 0 {
		for _, path := range []string{"session_id", "sessionId"} {
			if sid := normalizedSessionCandidate(gjson.GetBytes(payload, path).String()); sid != "" {
				return "session:" + sid, ""
			}
		}

		conversationID := ""
		conversation := gjson.GetBytes(payload, "conversation")
		if sid := normalizedSessionCandidate(conversation.Get("id").String()); sid != "" {
			conversationID = "conv:" + sid
		} else if conversation.Type == gjson.String {
			if sid := normalizedSessionCandidate(conversation.String()); sid != "" {
				conversationID = "conv:" + sid
			}
		}
		if sid := normalizedSessionCandidate(gjson.GetBytes(payload, "prompt_cache_key").String()); sid != "" {
			return "pck:" + sid, conversationID
		}
		if conversationID != "" {
			return conversationID, ""
		}

		if userID := normalizedSessionCandidate(gjson.GetBytes(payload, "metadata.user_id").String()); userID != "" {
			return "user:" + userID, ""
		}
		if conversationID := normalizedSessionCandidate(gjson.GetBytes(payload, "conversation_id").String()); conversationID != "" {
			return "conv:" + conversationID, ""
		}
	}

	if executionID, ok := metadata[cliproxyexecutor.ExecutionSessionMetadataKey].(string); ok {
		if executionID = normalizedSessionCandidate(executionID); executionID != "" {
			return "execution:" + executionID, ""
		}
	}
	if derivedID := normalizedSessionCandidate(cliproxysession.DerivedID(metadata)); derivedID != "" {
		return "derived:" + derivedID, ""
	}
	if len(payload) == 0 {
		return "", ""
	}
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
