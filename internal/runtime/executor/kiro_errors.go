package executor

// Kiro upstream error classification for credential failover.
//
// Ported behavior from kiro-lb's kiro/account_errors.py (classify_error) and
// kiro/kiro_errors.py (SUSPENSION_REASON / _SUSPENSION_MARKERS), AGPL-3.0,
// minpeter/jc01rho fork of jwadow/kiro-gateway. kiro-lb classifies an upstream
// refusal as either FATAL (return to the caller immediately: the request
// itself is bad and will fail on every credential) or RECOVERABLE (the
// problem is with this credential specifically: quota, token, or transient
// gateway trouble, so the next credential should be tried).

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// kiroErrorDecision mirrors kiro-lb's ErrorType enum.
type kiroErrorDecision int

const (
	// KiroErrorFatal means the request itself is malformed or otherwise
	// unserviceable; retrying with a different credential cannot help.
	KiroErrorFatal kiroErrorDecision = iota
	// KiroErrorRecoverable means the failure is specific to this credential
	// (quota, auth, or a transient gateway hiccup); the caller should try
	// the next available credential.
	KiroErrorRecoverable
)

const (
	kiroMonthlyQuotaCooldownFloor   = time.Hour
	kiroMonthlyQuotaCooldownCeiling = 7 * 24 * time.Hour
	kiroMonthlyQuotaResetMargin     = 5 * time.Minute
)

// kiroMonthlyQuotaCooldown waits for the reported monthly reset when one is
// available. Stale or malformed reset data cannot shorten the quarantine below
// one hour or extend it beyond seven days.
func kiroMonthlyQuotaCooldown(auth *cliproxyauth.Auth, body []byte, headers http.Header, now time.Time) time.Duration {
	resetAt, ok := kiroMonthlyQuotaResetAt(auth, body, headers)
	if !ok {
		return kiroMonthlyQuotaCooldownFloor
	}
	cooldown := resetAt.Add(kiroMonthlyQuotaResetMargin).Sub(now)
	if cooldown < kiroMonthlyQuotaCooldownFloor {
		return kiroMonthlyQuotaCooldownFloor
	}
	if cooldown > kiroMonthlyQuotaCooldownCeiling {
		return kiroMonthlyQuotaCooldownCeiling
	}
	return cooldown
}

func kiroMonthlyQuotaResetAt(auth *cliproxyauth.Auth, body []byte, headers http.Header) (time.Time, bool) {
	if resetAt, ok := kiroResetTimeFromJSON(body); ok {
		return resetAt, true
	}
	for key, values := range headers {
		if !kiroQuotaResetKey(key) {
			continue
		}
		for _, value := range values {
			if resetAt, ok := parseKiroResetTime(value); ok {
				return resetAt, true
			}
		}
	}
	if auth == nil {
		return time.Time{}, false
	}
	for key, value := range auth.Quota.Signals {
		if kiroQuotaResetKey(key) {
			if resetAt, ok := parseKiroResetTime(value); ok {
				return resetAt, true
			}
		}
	}
	for key, value := range auth.Metadata {
		if kiroQuotaResetKey(key) {
			if resetAt, ok := parseKiroResetTime(value); ok {
				return resetAt, true
			}
		}
	}
	return time.Time{}, false
}

func kiroResetTimeFromJSON(body []byte) (time.Time, bool) {
	if len(body) == 0 {
		return time.Time{}, false
	}
	var value any
	if json.Unmarshal(body, &value) != nil {
		return time.Time{}, false
	}
	return findKiroResetTime(value)
}

func findKiroResetTime(value any) (time.Time, bool) {
	switch typed := value.(type) {
	case map[string]any:
		for key, candidate := range typed {
			if kiroQuotaResetKey(key) {
				if resetAt, ok := parseKiroResetTime(candidate); ok {
					return resetAt, true
				}
			}
		}
		for _, candidate := range typed {
			if resetAt, ok := findKiroResetTime(candidate); ok {
				return resetAt, true
			}
		}
	case []any:
		for _, candidate := range typed {
			if resetAt, ok := findKiroResetTime(candidate); ok {
				return resetAt, true
			}
		}
	}
	return time.Time{}, false
}

func kiroQuotaResetKey(key string) bool {
	normalized := strings.NewReplacer("-", "", "_", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(key)))
	switch normalized {
	case "nextreset", "nextdatereset", "quotaresetat", "quotaresetsat", "monthlyquotaresetat", "monthlyquotaresetsat", "monthlyresetat", "monthlyresetsat", "resetat", "resetsat":
		return true
	default:
		return false
	}
}

func parseKiroResetTime(value any) (time.Time, bool) {
	switch typed := value.(type) {
	case string:
		raw := strings.TrimSpace(typed)
		if raw == "" {
			return time.Time{}, false
		}
		if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return parsed, true
		}
		if number, err := strconv.ParseFloat(raw, 64); err == nil {
			return kiroResetTimeFromEpoch(number)
		}
	case float64:
		return kiroResetTimeFromEpoch(typed)
	case float32:
		return kiroResetTimeFromEpoch(float64(typed))
	case int:
		return kiroResetTimeFromEpoch(float64(typed))
	case int64:
		return kiroResetTimeFromEpoch(float64(typed))
	case json.Number:
		if number, err := typed.Float64(); err == nil {
			return kiroResetTimeFromEpoch(number)
		}
	}
	return time.Time{}, false
}

func kiroResetTimeFromEpoch(value float64) (time.Time, bool) {
	if value <= 0 {
		return time.Time{}, false
	}
	if value >= 1e12 {
		return time.UnixMilli(int64(value)).UTC(), true
	}
	return time.Unix(int64(value), 0).UTC(), true
}

// kiroCooldownReasonMonthlyQuota is a Kiro-specific cooldown reason distinct
// from kiroauth.CooldownReason429: a monthly quota exhaustion is a different
// failure mode than a rate limit and should be labeled as such for
// diagnostics.
const kiroCooldownReasonMonthlyQuota = "monthly_quota"

// kiroErrorReasonFromBody extracts the upstream "reason" field for
// classification. kiro-lb reads reason from parsed JSON; this also falls
// back to a case-insensitive substring scan of the raw body for the known
// reason codes, since some upstream responses are not clean single-object
// JSON (e.g. wrapped/streamed error frames).
func kiroErrorReasonFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var parsed struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && strings.TrimSpace(parsed.Reason) != "" {
		return strings.ToUpper(strings.TrimSpace(parsed.Reason))
	}

	upper := strings.ToUpper(string(body))
	switch {
	case strings.Contains(upper, "MONTHLY_REQUEST_COUNT"):
		return "MONTHLY_REQUEST_COUNT"
	case strings.Contains(upper, "INVALID_MODEL_ID"):
		return "INVALID_MODEL_ID"
	case strings.Contains(upper, "CONTENT_LENGTH_EXCEEDS_THRESHOLD"):
		return "CONTENT_LENGTH_EXCEEDS_THRESHOLD"
	default:
		return ""
	}
}

// classifyKiroUpstreamError classifies an upstream Kiro API error for
// failover decisions, mirroring kiro-lb's classify_error table exactly:
//
// RECOVERABLE (try next credential):
//   - any 402 (payment required / monthly quota)
//   - any 403 (token expired/invalid)
//   - any 429 (rate limit)
//   - 400 + reason=MONTHLY_REQUEST_COUNT
//   - 400 + reason=INVALID_MODEL_ID
//   - 502 / 503 / 504 (transient upstream gateway failures)
//
// FATAL (return to caller immediately):
//   - 400 + reason=CONTENT_LENGTH_EXCEEDS_THRESHOLD (context overflow)
//   - 400 with any other/no reason (malformed request)
//   - 422 (validation error)
//   - other 5xx
//   - unknown status codes (default to FATAL to avoid wasted retries)
func classifyKiroUpstreamError(statusCode int, body []byte) kiroErrorDecision {
	switch statusCode {
	case 402, 403, 429:
		return KiroErrorRecoverable
	case 502, 503, 504:
		return KiroErrorRecoverable
	case 400:
		switch kiroErrorReasonFromBody(body) {
		case "MONTHLY_REQUEST_COUNT", "INVALID_MODEL_ID":
			return KiroErrorRecoverable
		default:
			return KiroErrorFatal
		}
	default:
		return KiroErrorFatal
	}
}

// kiroRetryAfterFromHeader parses a standard HTTP Retry-After header value
// (either delta-seconds or an HTTP-date), returning nil when absent or
// unparsable. Used to prefer the upstream's own cooldown hint over the
// locally computed exponential backoff when both are available.
func kiroRetryAfterFromHeader(raw string, now time.Time) *time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds >= 0 {
		delay := time.Duration(seconds) * time.Second
		return &delay
	}
	if deadline, err := http.ParseTime(raw); err == nil {
		delay := deadline.Sub(now)
		if delay < 0 {
			delay = 0
		}
		return &delay
	}
	return nil
}

// kiroSuspensionReason mirrors kiro-lb's SUSPENSION_REASON: the reason code
// the upstream sets on an authorization refusal that means the account is
// locked, distinct from an ordinary token refresh failure.
const kiroSuspensionReason = "TEMPORARILY_SUSPENDED"

// kiroSuspensionMarkers mirrors kiro-lb's _SUSPENSION_MARKERS: message
// substrings that indicate an account lock even when the reason field is
// absent (legacy hosts only send the wording).
var kiroSuspensionMarkers = []string{
	"temporarily suspended",
	"temporarily is suspended",
	"locked your account",
	"locked it as a",
}

// isKiroSuspendedBody reports whether an upstream 403 body indicates the
// account itself is locked (as opposed to an ordinary expired/invalid
// token). This centralizes and extends the previous inline "SUSPENDED"
// substring check: it still matches a bare "SUSPENDED" token for backward
// compatibility, and additionally recognizes kiro-lb's reason code and
// message markers.
func isKiroSuspendedBody(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	upper := strings.ToUpper(string(body))
	if strings.Contains(upper, "SUSPENDED") || strings.Contains(upper, kiroSuspensionReason) {
		return true
	}
	lower := strings.ToLower(string(body))
	for _, marker := range kiroSuspensionMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
