package claudewarmup

import (
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	return loc
}

func TestScheduleNextDefaultWeekdaysInSeoul(t *testing.T) {
	seoul := mustLoc(t, "Asia/Seoul")
	s, err := ParseSchedule("50 7 * * 1-5")
	if err != nil {
		t.Fatalf("ParseSchedule: %v", err)
	}
	cases := []struct {
		name  string
		after time.Time
		want  time.Time
	}{
		// 2026-10-29 is a Thursday.
		{"before fire same day", time.Date(2026, 10, 29, 7, 0, 0, 0, seoul), time.Date(2026, 10, 29, 7, 50, 0, 0, seoul)},
		{"exactly at fire is exclusive", time.Date(2026, 10, 29, 7, 50, 0, 0, seoul), time.Date(2026, 10, 30, 7, 50, 0, 0, seoul)},
		{"friday after fire skips weekend", time.Date(2026, 10, 30, 8, 0, 0, 0, seoul), time.Date(2026, 11, 2, 7, 50, 0, 0, seoul)},
		{"saturday", time.Date(2026, 10, 31, 6, 0, 0, 0, seoul), time.Date(2026, 11, 2, 7, 50, 0, 0, seoul)},
		{"month rollover", time.Date(2026, 12, 31, 9, 0, 0, 0, seoul), time.Date(2027, 1, 1, 7, 50, 0, 0, seoul)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.Next(tc.after); !got.Equal(tc.want) {
				t.Fatalf("Next(%s) = %s, want %s", tc.after, got, tc.want)
			}
		})
	}
}

func TestScheduleNextEvaluatesInGivenTimezone(t *testing.T) {
	seoul := mustLoc(t, "Asia/Seoul")
	s, err := ParseSchedule("50 7 * * 1-5")
	if err != nil {
		t.Fatalf("ParseSchedule: %v", err)
	}
	// Thursday 2026-10-29 22:00 UTC is Friday 07:00 in Seoul, so the next fire is
	// Friday 07:50 KST (22:50 UTC), not Friday 07:50 UTC.
	afterUTC := time.Date(2026, 10, 29, 22, 0, 0, 0, time.UTC)
	got := s.Next(afterUTC.In(seoul))
	want := time.Date(2026, 10, 29, 22, 50, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("Next = %s, want %s", got.UTC(), want)
	}
	// In UTC the same instant is Thursday 22:00, so the fire is Friday 07:50 UTC.
	if gotUTC := s.Next(afterUTC); !gotUTC.Equal(time.Date(2026, 10, 30, 7, 50, 0, 0, time.UTC)) {
		t.Fatalf("Next(UTC) = %s", gotUTC)
	}
}

func TestScheduleFieldSyntax(t *testing.T) {
	cases := []struct {
		expr  string
		after time.Time
		want  time.Time
	}{
		{"*/15 * * * *", time.Date(2026, 1, 1, 10, 7, 30, 0, time.UTC), time.Date(2026, 1, 1, 10, 15, 0, 0, time.UTC)},
		{"0 9,18 * * *", time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC), time.Date(2026, 1, 1, 18, 0, 0, 0, time.UTC)},
		// Day-of-week 7 means Sunday; 2026-01-04 is a Sunday.
		{"0 0 * * 7", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC)},
		// Both day fields restricted: either matches (1st of month or Sunday).
		{"0 0 1 * 0", time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC)},
		{"30 6 * 3 *", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 1, 6, 30, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		s, err := ParseSchedule(tc.expr)
		if err != nil {
			t.Fatalf("ParseSchedule(%q): %v", tc.expr, err)
		}
		if got := s.Next(tc.after); !got.Equal(tc.want) {
			t.Errorf("%q Next(%s) = %s, want %s", tc.expr, tc.after, got, tc.want)
		}
	}
}

func TestParseScheduleRejectsInvalid(t *testing.T) {
	for _, expr := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 8", "a * * * *", "*/0 * * * *", "5-1 * * * *", "1,,2 * * * *"} {
		if _, err := ParseSchedule(expr); err == nil {
			t.Errorf("ParseSchedule(%q) succeeded, want error", expr)
		}
	}
}

func TestScheduleNextImpossibleReturnsZero(t *testing.T) {
	s, err := ParseSchedule("0 0 30 2 *")
	if err != nil {
		t.Fatalf("ParseSchedule: %v", err)
	}
	if got := s.Next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); !got.IsZero() {
		t.Fatalf("Next = %s, want zero", got)
	}
}
