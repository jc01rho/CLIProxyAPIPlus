package auth

import (
	"context"
	"strconv"
	"strings"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

// modelTimeGateNow is a controllable clock for tests.
var modelTimeGateNow = time.Now

// modelTimeGatedByConfig reports whether the auth candidate is excluded by an
// active model time gate. The match order is provider, then auth ID, then
// route model: a commandCode credential serving the same model ID is never
// gated by a deepseek-scoped rule because its provider differs.
func modelTimeGatedByConfig(rules []internalconfig.ModelTimeGate, auth *Auth, routeModel string) (string, bool) {
	if len(rules) == 0 || auth == nil {
		return "", false
	}
	for i := range rules {
		rule := &rules[i]
		if rule.Enabled != nil && !*rule.Enabled {
			continue
		}
		if provider := strings.ToLower(strings.TrimSpace(rule.Provider)); provider != "" &&
			provider != strings.ToLower(strings.TrimSpace(auth.Provider)) {
			continue
		}
		if authID := strings.TrimSpace(rule.AuthID); authID != "" && authID != auth.ID {
			continue
		}
		if !modelTimeGateMatchesModel(rule.Models, routeModel) {
			continue
		}
		if modelTimeGateActiveAt(rule.Schedule, rule.Duration, modelTimeGateNow()) {
			name := strings.TrimSpace(rule.Name)
			if name == "" {
				name = "unnamed"
			}
			return name, true
		}
	}
	return "", false
}

// modelTimeGateMatchesModel reports whether the route model matches the
// rule's glob list. Empty matches everything.
func modelTimeGateMatchesModel(patterns []string, routeModel string) bool {
	if len(patterns) == 0 {
		return true
	}
	routeModel = strings.ToLower(strings.TrimSpace(routeModel))
	if routeModel == "" {
		return false
	}
	for _, pattern := range patterns {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		if ok, err := matchGlob(pattern, routeModel); err == nil && ok {
			return true
		}
	}
	return false
}

// matchGlob matches "*" (any run) and "?" (single rune) wildcards.
func matchGlob(pattern, value string) (bool, error) {
	px, vx := []rune(pattern), []rune(value)
	var pi, vi, star, match int
	star = -1
	for vi < len(vx) {
		if pi < len(px) && (px[pi] == '?' || px[pi] == vx[vi]) {
			pi++
			vi++
			continue
		}
		if pi < len(px) && px[pi] == '*' {
			star = pi
			match = vi
			pi++
			continue
		}
		if star != -1 {
			pi = star + 1
			match++
			vi = match
			continue
		}
		return false, nil
	}
	for pi < len(px) && px[pi] == '*' {
		pi++
	}
	return pi == len(px), nil
}

// modelTimeGateActiveAt reports whether the cron schedule window covers t in
// UTC. Only minute, hour, and day-of-week fields are evaluated (day-of-month
// and month must be "*" to match); the window runs [start, start+duration).
func modelTimeGateActiveAt(schedule, duration string, t time.Time) bool {
	startMinute, startHour, weekdays, ok := parseCronStart(schedule)
	if !ok {
		return false
	}
	dur, err := time.ParseDuration(strings.TrimSpace(duration))
	if err != nil || dur <= 0 {
		return false
	}
	t = t.UTC()
	wd := int(t.Weekday())
	matched := false
	for _, w := range weekdays {
		if w == wd {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	start := time.Date(t.Year(), t.Month(), t.Day(), startHour, startMinute, 0, 0, time.UTC)
	if t.Before(start) {
		return false
	}
	// Half-open window [start, start+duration): the tick at exactly
	// start+duration belongs to the next period.
	return t.Before(start.Add(dur))
}

// parseCronStart parses "minute hour dom month dow" and returns the start
// minute/hour plus matching weekdays (0=Sunday). Minute and hour accept a
// single value or "*"; dow accepts "*", single values, ranges, steps, and
// comma lists with Sunday as 0 or 7.
func parseCronStart(schedule string) (minute, hour int, weekdays []int, ok bool) {
	fields := strings.Fields(schedule)
	if len(fields) != 5 {
		return 0, 0, nil, false
	}
	minute, ok = parseCronValue(fields[0], 0, 59)
	if !ok {
		return 0, 0, nil, false
	}
	hour, ok = parseCronValue(fields[1], 0, 23)
	if !ok {
		return 0, 0, nil, false
	}
	if strings.TrimSpace(fields[2]) != "*" || strings.TrimSpace(fields[3]) != "*" {
		return 0, 0, nil, false
	}
	weekdays, ok = parseCronWeekdays(fields[4])
	if !ok || len(weekdays) == 0 {
		return 0, 0, nil, false
	}
	return minute, hour, weekdays, true
}

// parseCronValue parses a single integer or "*".
func parseCronValue(field string, min, max int) (int, bool) {
	field = strings.TrimSpace(field)
	if field == "*" {
		return min, true
	}
	v, err := strconv.Atoi(field)
	if err != nil || v < min || v > max {
		return 0, false
	}
	return v, true
}

// parseCronWeekdays parses the day-of-week field.
func parseCronWeekdays(field string) ([]int, bool) {
	field = strings.TrimSpace(field)
	if field == "*" {
		return []int{0, 1, 2, 3, 4, 5, 6}, true
	}
	seen := map[int]bool{}
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		step := 1
		if idx := strings.Index(part, "/"); idx >= 0 {
			var err error
			step, err = strconv.Atoi(strings.TrimSpace(part[idx+1:]))
			if err != nil || step <= 0 {
				return nil, false
			}
			part = strings.TrimSpace(part[:idx])
		}
		var lo, hi int
		if part == "*" || part == "" {
			lo, hi = 0, 6
		} else if idx := strings.Index(part, "-"); idx >= 0 {
			var err error
			lo, err = parseCronWeekday(strings.TrimSpace(part[:idx]))
			if err != nil {
				return nil, false
			}
			hi, err = parseCronWeekday(strings.TrimSpace(part[idx+1:]))
			if err != nil {
				return nil, false
			}
			if lo > hi {
				return nil, false
			}
		} else {
			var err error
			lo, err = parseCronWeekday(part)
			if err != nil {
				return nil, false
			}
			hi = lo
		}
		for v := lo; v <= hi; v += step {
			seen[v] = true
		}
	}
	out := make([]int, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	return out, true
}

// parseCronWeekday parses one weekday value (0-7, Sunday is 0 or 7).
func parseCronWeekday(field string) (int, error) {
	v, err := strconv.Atoi(strings.TrimSpace(field))
	if err != nil {
		return 0, err
	}
	if v == 7 {
		v = 0
	}
	if v < 0 || v > 6 {
		return 0, strconv.ErrRange
	}
	return v, nil
}

// warnLogTimeGateExcluded records an excluded candidate with the rule name,
// auth, provider, and route model for traceability.
func (m *Manager) warnLogTimeGateExcluded(ctx context.Context, provider, routeModel string, auth *Auth, ruleName string) {
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	entry := log.WithField("rule", ruleName).
		WithField("provider", provider).
		WithField("auth_id", authID).
		WithField("model", routeModel)
	if reqID, ok := ctx.Value("request_id").(string); ok && reqID != "" {
		entry = entry.WithField("request_id", reqID)
	}
	entry.Info("auth excluded by model time gate")
}

// authMatchesTimeGate is the Manager-facing wrapper: it reads the live
// routing config (same runtimeConfig source as threshold routing) and
// reports the blocking rule name.
func (m *Manager) authMatchesTimeGate(auth *Auth, routeModel string, _ cliproxyexecutor.Options) (string, bool) {
	if m == nil {
		return "", false
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		return "", false
	}
	return modelTimeGatedByConfig(cfg.Routing.ModelTimeGates, auth, routeModel)
}
