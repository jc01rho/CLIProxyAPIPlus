package management

import (
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

const (
	defaultAPIKeyIPBlacklistFailureThreshold = 3
	defaultAPIKeyIPBlacklistFailureWindow    = 10 * time.Minute
	defaultAPIKeyIPBlacklistBlockDuration    = 24 * time.Hour
	ipBlacklistCleanupInterval               = 1 * time.Hour
)

type APIKeyIPBlacklistPolicy struct {
	FailureThreshold int           `json:"failure-threshold"`
	FailureWindow    time.Duration `json:"-"`
	FailureWindowRaw string        `json:"failure-window"`
	BlockDuration    time.Duration `json:"-"`
	BlockDurationRaw string        `json:"block-duration"`
}

type APIKeyIPBlacklistEntry struct {
	IP                    string `json:"ip"`
	FailureCount          int    `json:"failure-count"`
	LastFailureAt         string `json:"last-failure-at,omitempty"`
	BlockedUntil          string `json:"blocked-until,omitempty"`
	RemainingBlockSeconds int64  `json:"remaining-block-seconds"`
}

type apiKeyIPBlacklistState struct {
	failures    []time.Time
	blockedUntil time.Time
	lastFailure time.Time
	lastSeen    time.Time
}

type APIKeyIPBlacklistStore struct {
	mu     sync.Mutex
	policy APIKeyIPBlacklistPolicy
	states map[string]*apiKeyIPBlacklistState
	nowFn  func() time.Time
}

func DefaultAPIKeyIPBlacklistPolicy() APIKeyIPBlacklistPolicy {
	return APIKeyIPBlacklistPolicy{
		FailureThreshold: defaultAPIKeyIPBlacklistFailureThreshold,
		FailureWindow:    defaultAPIKeyIPBlacklistFailureWindow,
		FailureWindowRaw: "10m",
		BlockDuration:    defaultAPIKeyIPBlacklistBlockDuration,
		BlockDurationRaw: "24h",
	}
}

func APIKeyIPBlacklistPolicyFromConfig(cfg *config.Config) APIKeyIPBlacklistPolicy {
	policy := DefaultAPIKeyIPBlacklistPolicy()
	if cfg == nil {
		return policy
	}
	if cfg.APIKeyIPBlacklist.FailureThreshold > 0 {
		policy.FailureThreshold = cfg.APIKeyIPBlacklist.FailureThreshold
	}
	if parsed, raw, ok := parsePositiveDuration(cfg.APIKeyIPBlacklist.FailureWindow); ok {
		policy.FailureWindow = parsed
		policy.FailureWindowRaw = raw
	}
	if parsed, raw, ok := parsePositiveDuration(cfg.APIKeyIPBlacklist.BlockDuration); ok {
		policy.BlockDuration = parsed
		policy.BlockDurationRaw = raw
	}
	return policy
}

func NewAPIKeyIPBlacklistStore(policy APIKeyIPBlacklistPolicy) *APIKeyIPBlacklistStore {
	store := &APIKeyIPBlacklistStore{
		policy: policy,
		states: make(map[string]*apiKeyIPBlacklistState),
		nowFn:  time.Now,
	}
	store.startCleanup()
	return store
}

func (s *APIKeyIPBlacklistStore) startCleanup() {
	if s == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(ipBlacklistCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			s.cleanup()
		}
	}()
}

func (s *APIKeyIPBlacklistStore) cleanup() {
	if s == nil {
		return
	}
	now := s.currentTime()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(now)
}

func (s *APIKeyIPBlacklistStore) currentTime() time.Time {
	if s == nil || s.nowFn == nil {
		return time.Now()
	}
	return s.nowFn()
}

func (s *APIKeyIPBlacklistStore) UpdatePolicy(policy APIKeyIPBlacklistPolicy) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.policy = policy
	s.cleanupLocked(s.currentTime())
	s.mu.Unlock()
}

func (s *APIKeyIPBlacklistStore) Policy() APIKeyIPBlacklistPolicy {
	if s == nil {
		return DefaultAPIKeyIPBlacklistPolicy()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.policy
}

func (s *APIKeyIPBlacklistStore) IsBlocked(ip string) (APIKeyIPBlacklistEntry, bool) {
	if s == nil {
		return APIKeyIPBlacklistEntry{}, false
	}
	normalizedIP := strings.TrimSpace(ip)
	if normalizedIP == "" {
		return APIKeyIPBlacklistEntry{}, false
	}
	now := s.currentTime()
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.getActiveStateLocked(normalizedIP, now)
	if state == nil || state.blockedUntil.IsZero() || !state.blockedUntil.After(now) {
		return APIKeyIPBlacklistEntry{}, false
	}
	state.lastSeen = now
	return buildAPIKeyIPBlacklistEntry(normalizedIP, state, now), true
}

func (s *APIKeyIPBlacklistStore) RecordFailure(ip string) (APIKeyIPBlacklistEntry, bool) {
	if s == nil {
		return APIKeyIPBlacklistEntry{}, false
	}
	normalizedIP := strings.TrimSpace(ip)
	if normalizedIP == "" {
		return APIKeyIPBlacklistEntry{}, false
	}
	now := s.currentTime()
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.getOrCreateStateLocked(normalizedIP)
	policy := s.policy
	state.lastSeen = now
	state.lastFailure = now
	state.failures = pruneFailures(state.failures, now, policy.FailureWindow)
	state.failures = append(state.failures, now)
	blockedNow := false
	if len(state.failures) >= policy.FailureThreshold {
		state.blockedUntil = now.Add(policy.BlockDuration)
		blockedNow = true
	}
	return buildAPIKeyIPBlacklistEntry(normalizedIP, state, now), blockedNow
}

func (s *APIKeyIPBlacklistStore) ResetFailures(ip string) {
	if s == nil {
		return
	}
	normalizedIP := strings.TrimSpace(ip)
	if normalizedIP == "" {
		return
	}
	now := s.currentTime()
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.getActiveStateLocked(normalizedIP, now)
	if state == nil {
		return
	}
	if !state.blockedUntil.After(now) {
		state.failures = nil
		state.lastFailure = time.Time{}
		state.lastSeen = now
	}
	s.cleanupLocked(now)
}

func (s *APIKeyIPBlacklistStore) ListBlocked() []APIKeyIPBlacklistEntry {
	if s == nil {
		return []APIKeyIPBlacklistEntry{}
	}
	now := s.currentTime()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(now)
	entries := make([]APIKeyIPBlacklistEntry, 0)
	for ip, state := range s.states {
		if state == nil || !state.blockedUntil.After(now) {
			continue
		}
		entries = append(entries, buildAPIKeyIPBlacklistEntry(ip, state, now))
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].BlockedUntil == entries[j].BlockedUntil {
			return entries[i].IP < entries[j].IP
		}
		return entries[i].BlockedUntil > entries[j].BlockedUntil
	})
	return entries
}

func (s *APIKeyIPBlacklistStore) Unban(ip string) bool {
	if s == nil {
		return false
	}
	normalizedIP := strings.TrimSpace(ip)
	if normalizedIP == "" {
		return false
	}
	now := s.currentTime()
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.states[normalizedIP]
	if !ok || state == nil {
		return false
	}
	state.blockedUntil = time.Time{}
	state.failures = nil
	state.lastFailure = time.Time{}
	state.lastSeen = now
	s.cleanupLocked(now)
	return true
}

func (s *APIKeyIPBlacklistStore) ManualBan(ip string) (APIKeyIPBlacklistEntry, bool) {
	if s == nil {
		return APIKeyIPBlacklistEntry{}, false
	}
	normalizedIP := strings.TrimSpace(ip)
	if normalizedIP == "" {
		return APIKeyIPBlacklistEntry{}, false
	}
	now := s.currentTime()
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.getOrCreateStateLocked(normalizedIP)
	state.lastSeen = now
	state.blockedUntil = now.Add(s.policy.BlockDuration)
	return buildAPIKeyIPBlacklistEntry(normalizedIP, state, now), true
}

func (s *APIKeyIPBlacklistStore) getOrCreateStateLocked(ip string) *apiKeyIPBlacklistState {
	state, ok := s.states[ip]
	if !ok || state == nil {
		state = &apiKeyIPBlacklistState{}
		s.states[ip] = state
	}
	return state
}

func (s *APIKeyIPBlacklistStore) getActiveStateLocked(ip string, now time.Time) *apiKeyIPBlacklistState {
	state, ok := s.states[ip]
	if !ok || state == nil {
		return nil
	}
	state.failures = pruneFailures(state.failures, now, s.policy.FailureWindow)
	if !state.blockedUntil.IsZero() && !state.blockedUntil.After(now) {
		state.blockedUntil = time.Time{}
		state.failures = nil
		state.lastFailure = time.Time{}
	}
	if len(state.failures) == 0 && state.blockedUntil.IsZero() {
		delete(s.states, ip)
		return nil
	}
	return state
}

func (s *APIKeyIPBlacklistStore) cleanupLocked(now time.Time) {
	idleTTL := s.policy.BlockDuration
	if idleTTL < s.policy.FailureWindow {
		idleTTL = s.policy.FailureWindow
	}
	if idleTTL < time.Hour {
		idleTTL = time.Hour
	}
	for ip, state := range s.states {
		if state == nil {
			delete(s.states, ip)
			continue
		}
		state.failures = pruneFailures(state.failures, now, s.policy.FailureWindow)
		if !state.blockedUntil.IsZero() && !state.blockedUntil.After(now) {
			state.blockedUntil = time.Time{}
			state.failures = nil
			state.lastFailure = time.Time{}
		}
		if state.blockedUntil.IsZero() && len(state.failures) == 0 && !state.lastSeen.IsZero() && now.Sub(state.lastSeen) >= idleTTL {
			delete(s.states, ip)
		}
	}
}

func pruneFailures(failures []time.Time, now time.Time, window time.Duration) []time.Time {
	if len(failures) == 0 {
		return nil
	}
	cutoff := now.Add(-window)
	pruned := failures[:0]
	for _, failureTime := range failures {
		if failureTime.After(cutoff) {
			pruned = append(pruned, failureTime)
		}
	}
	return pruned
}

func buildAPIKeyIPBlacklistEntry(ip string, state *apiKeyIPBlacklistState, now time.Time) APIKeyIPBlacklistEntry {
	entry := APIKeyIPBlacklistEntry{
		IP:           ip,
		FailureCount: len(state.failures),
	}
	if !state.lastFailure.IsZero() {
		entry.LastFailureAt = state.lastFailure.UTC().Format(time.RFC3339)
	}
	if !state.blockedUntil.IsZero() {
		entry.BlockedUntil = state.blockedUntil.UTC().Format(time.RFC3339)
		if state.blockedUntil.After(now) {
			entry.RemainingBlockSeconds = int64(time.Until(state.blockedUntil).Round(time.Second) / time.Second)
		}
	}
	return entry
}

func parsePositiveDuration(raw string) (time.Duration, string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, "", false
	}
	parsed, err := time.ParseDuration(trimmed)
	if err != nil || parsed <= 0 {
		return 0, "", false
	}
	return parsed, trimmed, true
}

func (h *Handler) APIKeyIPBlacklistStore() *APIKeyIPBlacklistStore {
	if h == nil {
		return nil
	}
	return h.apiKeyIPBlacklist
}

func (h *Handler) GetAPIKeyIPBlacklist(c *gin.Context) {
	if h == nil || h.apiKeyIPBlacklist == nil {
		c.JSON(http.StatusOK, gin.H{"blocked-ips": []APIKeyIPBlacklistEntry{}, "policy": DefaultAPIKeyIPBlacklistPolicy()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"blocked-ips": h.apiKeyIPBlacklist.ListBlocked(),
		"policy":      h.apiKeyIPBlacklist.Policy(),
	})
}

func (h *Handler) DeleteAPIKeyIPBlacklist(c *gin.Context) {
	if h == nil || h.apiKeyIPBlacklist == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "ip not found"})
		return
	}
	ip := strings.TrimSpace(c.Query("ip"))
	if ip == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing ip"})
		return
	}
	if !h.apiKeyIPBlacklist.Unban(ip) {
		c.JSON(http.StatusNotFound, gin.H{"error": "ip not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "ip": ip})
}

type apiKeyIPBlacklistManualBanRequest struct {
	IP string `json:"ip"`
}

func (h *Handler) PostAPIKeyIPBlacklist(c *gin.Context) {
	if h == nil || h.apiKeyIPBlacklist == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ip blacklist unavailable"})
		return
	}
	var req apiKeyIPBlacklistManualBanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	ip := strings.TrimSpace(req.IP)
	if ip == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing ip"})
		return
	}
	if parsedIP := net.ParseIP(ip); parsedIP == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid ip"})
		return
	}
	entry, ok := h.apiKeyIPBlacklist.ManualBan(ip)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unable to ban ip"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "entry": entry})
}
