package helps

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ClaudeCodeIdentity represents the device/session identity used by Claude Code.
// It is used to construct the metadata.user_id JSON string.
type ClaudeCodeIdentity struct {
	DeviceID     string
	AccountUUID  string
	SessionID    string
}

// Global identity cache to ensure stable identities for a given seed.
var (
	claudeCodeIdentityCache            = make(map[string]*ClaudeCodeIdentity)
	claudeCodeIdentityCacheMu          sync.RWMutex
	claudeCodeIdentityCacheCleanupOnce sync.Once
	claudeCodeIdentityCacheSizeLimit   = 1000
)

// claudeCodeBootstrapCache caches bootstrap results (account_uuid lookups).
// Negative results are cached for 5 minutes, positive for 24 hours.
var (
	claudeCodeBootstrapResults = make(map[string]*bootstrapResult)
	claudeCodeBootstrapMu      sync.RWMutex
)

type bootstrapResult struct {
	accountUUID string
	expiresAt   int64
}

const (
	claudeCodeBootstrapTTL          = 24 * time.Hour
	claudeCodeBootstrapNegativeTTL = 5 * time.Minute
)

// GetClaudeCodeIdentity returns a stable identity for the given seed (typically the access token).
func GetClaudeCodeIdentity(seed string) *ClaudeCodeIdentity {
	if seed == "" {
		seed = "anonymous"
	}

	claudeCodeIdentityCacheMu.Lock()
	defer claudeCodeIdentityCacheMu.Unlock()

	if cached, ok := claudeCodeIdentityCache[seed]; ok {
		// Return a copy to prevent mutation
		return &ClaudeCodeIdentity{
			DeviceID:     cached.DeviceID,
			AccountUUID:  cached.AccountUUID,
			SessionID:    cached.SessionID,
		}
	}

	// Generate new identity
	identity := &ClaudeCodeIdentity{
		DeviceID:  generateRandomHex(32),
		SessionID: uuid.New().String(),
	}

	// Ensure cache size limit (LRU-like eviction)
	if len(claudeCodeIdentityCache) >= claudeCodeIdentityCacheSizeLimit {
		// Remove oldest entry
		var oldest string
		for k := range claudeCodeIdentityCache {
			oldest = k
			break
		}
		delete(claudeCodeIdentityCache, oldest)
	}

	claudeCodeIdentityCache[seed] = identity
	return &ClaudeCodeIdentity{
		DeviceID:     identity.DeviceID,
		AccountUUID:  identity.AccountUUID,
		SessionID:    identity.SessionID,
	}
}

// ResolveClaudeCodeIdentity resolves the full identity including account_uuid from the Claude bootstrap API.
// If accessToken does not start with "sk-ant-oat", returns identity without account_uuid.
func ResolveClaudeCodeIdentity(accessToken string, model string) *ClaudeCodeIdentity {
	identity := GetClaudeCodeIdentity(accessToken)

	// Only OAuth tokens can be resolved
	if accessToken == "" || len(accessToken) < 8 || accessToken[:8] != "sk-ant-o" {
		return identity
	}

	// Check bootstrap cache
	claudeCodeBootstrapMu.RLock()
	if cached, ok := claudeCodeBootstrapResults[accessToken]; ok {
		if time.Now().UnixMilli() < cached.expiresAt {
			if cached.accountUUID != "" {
				identity.AccountUUID = cached.accountUUID
			}
			claudeCodeBootstrapMu.RUnlock()
			return identity
		}
	}
	claudeCodeBootstrapMu.RUnlock()

	// Fetch account_uuid from bootstrap API (this is a placeholder - actual HTTP call would be in executor)
	// The executor calls this and provides the result back
	return identity
}

// SetBootstrapAccountUUID sets the account UUID for an access token (called by executor after HTTP lookup).
func SetBootstrapAccountUUID(accessToken, accountUUID string) {
	claudeCodeBootstrapMu.Lock()
	defer claudeCodeBootstrapMu.Unlock()

	ttl := claudeCodeBootstrapNegativeTTL
	if accountUUID != "" {
		ttl = claudeCodeBootstrapTTL
	}

	claudeCodeBootstrapResults[accessToken] = &bootstrapResult{
		accountUUID: accountUUID,
		expiresAt:   time.Now().Add(ttl).UnixMilli(),
	}
}

// BuildClaudeCodeMetadataUserID creates the JSON user_id string for metadata.user_id.
// Returns empty string if accountUUID is empty.
func BuildClaudeCodeMetadataUserID(identity *ClaudeCodeIdentity) string {
	if identity == nil || identity.AccountUUID == "" {
		return ""
	}
	data := map[string]string{
		"device_id":    identity.DeviceID,
		"account_uuid": identity.AccountUUID,
		"session_id":   identity.SessionID,
	}
	b, _ := json.Marshal(data)
	return string(b)
}

// ApplyClaudeCodeMetadata injects the metadata.user_id into the request body.
func ApplyClaudeCodeMetadata(body []byte, identity *ClaudeCodeIdentity) ([]byte, bool) {
	if identity == nil {
		return body, false
	}
	userID := BuildClaudeCodeMetadataUserID(identity)
	if userID == "" {
		return body, false
	}
	result, err := sjson.SetBytes(body, "metadata.user_id", userID)
	if err != nil {
		return body, false
	}
	return result, true
}

func generateRandomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// OrderClaudeCodeBody reorders the JSON body fields to match Claude Code's expected field order.
func OrderClaudeCodeBody(body []byte) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}

	// Parse the body as a JSON object
	obj := gjson.ParseBytes(body)
	if !obj.IsObject() {
		return body, nil
	}

	// Target field order per anthropic-auth
	fieldOrder := []string{
		"model",
		"messages",
		"system",
		"tools",
		"tool_choice",
		"metadata",
		"max_tokens",
		"temperature",
		"thinking",
		"context_management",
		"output_config",
		"diagnostics",
		"stream",
		"speed",
	}

	// Build ordered object
	result := make(map[string]any)
	for _, field := range fieldOrder {
		if gjson.GetBytes(body, field).Exists() {
			result[field] = gjson.GetBytes(body, field).Value()
		}
	}
	// Add any remaining fields not in the order list
	rawObj := gjson.ParseBytes(body)
	for key, val := range rawObj.Map() {
		if _, exists := result[key]; !exists && val.Exists() {
			result[key] = val.Value()
		}
	}

	// Convert back to JSON
	return json.Marshal(result)
}