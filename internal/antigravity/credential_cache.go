package antigravity

import "sync"

// credentialCacheMaxEntries caps the in-process credential cache size.
// Single-account extension: keep the map tiny.
const credentialCacheMaxEntries = 4

// credentialCache is an in-process LRU bridge for the packed refresh string.
//
// pi's provider framework extracts the bearer token via oauth.getApiKey and
// passes only that access token to streamSimple. The packed refresh
// (refreshToken|projectId|managedProjectID) that login/refresh resolved is
// therefore not visible to the stream. This cache bridges that gap so the
// stream can recover the managed-project context and avoid a redundant
// loadCodeAssist round-trip on every turn.
type credentialCache struct {
	mu    sync.Mutex
	items map[string]string
	order []string
}

var globalCredentialCache = &credentialCache{
	items: make(map[string]string, credentialCacheMaxEntries),
	order: make([]string, 0, credentialCacheMaxEntries),
}

// RememberPackedRefresh stores the packed refresh string keyed by the access
// token, evicting the oldest entry when the cache exceeds credentialCacheMaxEntries.
func RememberPackedRefresh(accessToken string, packedRefresh string) {
	if accessToken == "" {
		return
	}
	globalCredentialCache.mu.Lock()
	defer globalCredentialCache.mu.Unlock()
	if _, exists := globalCredentialCache.items[accessToken]; exists {
		globalCredentialCache.removeFromOrder(accessToken)
	} else if len(globalCredentialCache.items) >= credentialCacheMaxEntries && len(globalCredentialCache.order) > 0 {
		oldest := globalCredentialCache.order[0]
		globalCredentialCache.removeFromOrder(oldest)
		delete(globalCredentialCache.items, oldest)
	}
	globalCredentialCache.items[accessToken] = packedRefresh
	globalCredentialCache.order = append(globalCredentialCache.order, accessToken)
}

// GetPackedRefresh returns the cached packed refresh string for an access token,
// or empty string if not present.
func GetPackedRefresh(accessToken string) string {
	if accessToken == "" {
		return ""
	}
	globalCredentialCache.mu.Lock()
	defer globalCredentialCache.mu.Unlock()
	return globalCredentialCache.items[accessToken]
}

// ClearCredentialCache empties the credential cache (useful for tests or
// forced re-resolution).
func ClearCredentialCache() {
	globalCredentialCache.mu.Lock()
	defer globalCredentialCache.mu.Unlock()
	globalCredentialCache.items = make(map[string]string, credentialCacheMaxEntries)
	globalCredentialCache.order = make([]string, 0, credentialCacheMaxEntries)
}

func (c *credentialCache) removeFromOrder(key string) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}
