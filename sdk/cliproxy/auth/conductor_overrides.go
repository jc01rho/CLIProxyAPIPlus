package auth

import (
	"sync/atomic"
	"time"
)

var transientErrorCooldownSeconds atomic.Int64

// SetTransientErrorCooldownSeconds configures cooldowns for 408/500/502/503/504.
// 0 keeps the legacy default; negative values disable transient error cooldowns.
func SetTransientErrorCooldownSeconds(seconds int) {
	transientErrorCooldownSeconds.Store(int64(seconds))
}

// nextTransientErrorRetryAfter returns the time to wait before retrying after a
// transient error (e.g. 408/500/502/503/504). 0 keeps the legacy default of
// one minute; negative values disable transient error cooldowns.
func nextTransientErrorRetryAfter(now time.Time) time.Time {
	seconds := transientErrorCooldownSeconds.Load()
	if seconds < 0 {
		return time.Time{}
	}
	if seconds == 0 {
		return now.Add(time.Minute)
	}
	return now.Add(time.Duration(seconds) * time.Second)
}
