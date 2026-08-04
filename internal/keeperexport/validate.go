package keeperexport

import (
	"regexp"
	"time"
)

var (
	uuidv7RE      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	timestampRE   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)
	envVarNameRE  = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)
	fingerprintRE = regexp.MustCompile(`^akf1_[0-9a-f]{64}$`)
)

// isUUIDv7 reports whether s is a canonical lowercase UUIDv7 string.
func isUUIDv7(s string) bool { return uuidv7RE.MatchString(s) }

// isTimestamp reports whether s is a UTC RFC3339 timestamp with exactly
// millisecond precision, as required by contract section 2.1.
func isTimestamp(s string) bool {
	if !timestampRE.MatchString(s) {
		return false
	}
	_, err := time.Parse("2006-01-02T15:04:05.000Z07:00", s)
	return err == nil
}

// isFingerprint reports whether s is a valid akf1_ API-key fingerprint.
func isFingerprint(s string) bool { return fingerprintRE.MatchString(s) }

func stringLenInRange(s string, min, max int) bool {
	n := len(s)
	return n >= min && n <= max
}

// stringPtrLenInRange validates a nullable bounded string: nil is allowed,
// non-nil values must be within [min, max] bytes.
func stringPtrLenInRange(s *string, min, max int) bool {
	if s == nil {
		return true
	}
	return stringLenInRange(*s, min, max)
}
