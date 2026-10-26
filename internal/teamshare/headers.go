package teamshare

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// WindowObservation is one rate-limit window as reported by the upstream.
type WindowObservation struct {
	Utilization float64
	Reset       time.Time
	Status      string
	HasUtil     bool
}

// Observation carries both Claude rate-limit windows from a single response.
type Observation struct {
	FiveHour WindowObservation
	Weekly   WindowObservation
}

// Any reports whether the response carried at least one window signal.
func (o Observation) Any() bool {
	return o.FiveHour.HasUtil || !o.FiveHour.Reset.IsZero() || o.Weekly.HasUtil || !o.Weekly.Reset.IsZero()
}

// ParseObservation reads the Anthropic-Ratelimit-Unified-{5h,7d}-* headers.
func ParseObservation(h http.Header) Observation {
	return Observation{
		FiveHour: parseWindow(h, "5h"),
		Weekly:   parseWindow(h, "7d"),
	}
}

func parseWindow(h http.Header, window string) WindowObservation {
	prefix := "Anthropic-Ratelimit-Unified-" + window + "-"
	out := WindowObservation{Status: strings.ToLower(strings.TrimSpace(h.Get(prefix + "Status")))}
	if raw := strings.TrimSpace(h.Get(prefix + "Utilization")); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v >= 0 {
			// Anthropic reports a 0-1 fraction; tolerate a percentage.
			if v > 1.5 {
				v /= 100
			}
			out.Utilization = v
			out.HasUtil = true
		}
	}
	if raw := strings.TrimSpace(h.Get(prefix + "Reset")); raw != "" {
		out.Reset = parseResetTime(raw)
	}
	return out
}

func parseResetTime(raw string) time.Time {
	if secs, err := strconv.ParseFloat(raw, 64); err == nil && secs > 0 {
		return time.Unix(int64(secs), 0)
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t
	}
	return time.Time{}
}
