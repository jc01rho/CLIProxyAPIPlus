package auth

import "sync/atomic"

var transientErrorCooldownSeconds atomic.Int64

// SetTransientErrorCooldownSeconds configures cooldowns for 408/500/502/503/504.
// 0 keeps the legacy default; negative values disable transient error cooldowns.
func SetTransientErrorCooldownSeconds(seconds int) {
	transientErrorCooldownSeconds.Store(int64(seconds))
}
