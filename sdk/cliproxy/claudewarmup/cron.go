// Package claudewarmup sends one scheduled minimal request per Claude OAuth
// credential so each account's 5-hour usage window starts at a fixed time.
package claudewarmup

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed five-field cron expression:
// minute hour day-of-month month day-of-week.
// Each field accepts "*", numbers, ranges "a-b", lists "a,b" and steps "*/n" or "a-b/n".
// Day-of-week uses 0-7 where both 0 and 7 mean Sunday. When both day fields are
// restricted, a time matches if either matches (standard cron semantics); a day
// field that starts with "*" (including "*/n") counts as unrestricted.
type Schedule struct {
	minute, hour, dom, month, dow uint64
	domAny, dowAny                bool
}

// maxSearch bounds Next so impossible expressions (e.g. "0 0 30 2 *") terminate.
const maxSearch = 5 * 366 * 24 * time.Hour

// ParseSchedule parses a five-field cron expression.
func ParseSchedule(expr string) (*Schedule, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron %q: expected 5 fields, got %d", expr, len(fields))
	}
	var s Schedule
	var err error
	if s.minute, _, err = parseField(fields[0], 0, 59); err != nil {
		return nil, fmt.Errorf("cron %q minute: %w", expr, err)
	}
	if s.hour, _, err = parseField(fields[1], 0, 23); err != nil {
		return nil, fmt.Errorf("cron %q hour: %w", expr, err)
	}
	if s.dom, s.domAny, err = parseField(fields[2], 1, 31); err != nil {
		return nil, fmt.Errorf("cron %q day-of-month: %w", expr, err)
	}
	if s.month, _, err = parseField(fields[3], 1, 12); err != nil {
		return nil, fmt.Errorf("cron %q month: %w", expr, err)
	}
	if s.dow, s.dowAny, err = parseField(fields[4], 0, 7); err != nil {
		return nil, fmt.Errorf("cron %q day-of-week: %w", expr, err)
	}
	if s.dow&(1<<7) != 0 {
		s.dow |= 1
	}
	return &s, nil
}

func parseField(field string, lo, hi int) (bits uint64, wildcard bool, err error) {
	wildcard = strings.HasPrefix(field, "*")
	for _, part := range strings.Split(field, ",") {
		if part == "" {
			return 0, false, fmt.Errorf("empty list item in %q", field)
		}
		rangePart, step := part, 1
		if idx := strings.IndexByte(part, '/'); idx >= 0 {
			rangePart = part[:idx]
			step, err = strconv.Atoi(part[idx+1:])
			if err != nil || step <= 0 {
				return 0, false, fmt.Errorf("invalid step in %q", part)
			}
		}
		start, end := lo, hi
		switch {
		case rangePart == "*":
		case strings.Contains(rangePart, "-"):
			bounds := strings.SplitN(rangePart, "-", 2)
			if start, err = strconv.Atoi(bounds[0]); err != nil {
				return 0, false, fmt.Errorf("invalid range start in %q", part)
			}
			if end, err = strconv.Atoi(bounds[1]); err != nil {
				return 0, false, fmt.Errorf("invalid range end in %q", part)
			}
		default:
			if start, err = strconv.Atoi(rangePart); err != nil {
				return 0, false, fmt.Errorf("invalid value %q", part)
			}
			end = start
			if step > 1 {
				end = hi
			}
		}
		if start < lo || end > hi || start > end {
			return 0, false, fmt.Errorf("value out of range [%d-%d] in %q", lo, hi, part)
		}
		for v := start; v <= end; v += step {
			bits |= 1 << uint(v)
		}
	}
	return bits, wildcard, nil
}

func (s *Schedule) dayMatches(t time.Time) bool {
	domOK := s.dom&(1<<uint(t.Day())) != 0
	dowOK := s.dow&(1<<uint(t.Weekday())) != 0
	if s.domAny || s.dowAny {
		return domOK && dowOK
	}
	return domOK || dowOK
}

// Next returns the first matching minute strictly after `after`, evaluated in
// after's location. It returns the zero time when nothing matches within five years.
func (s *Schedule) Next(after time.Time) time.Time {
	loc := after.Location()
	t := after.Truncate(time.Minute).Add(time.Minute)
	limit := after.Add(maxSearch)
	for t.Before(limit) {
		if s.month&(1<<uint(t.Month())) == 0 {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, loc)
			continue
		}
		if !s.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
			continue
		}
		if s.hour&(1<<uint(t.Hour())) == 0 {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, loc)
			continue
		}
		if s.minute&(1<<uint(t.Minute())) == 0 {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}
