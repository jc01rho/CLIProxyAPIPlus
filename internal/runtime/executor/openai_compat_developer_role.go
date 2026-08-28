package executor

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

const (
	openAICompatDeveloperRoleCacheTTL = 15 * time.Minute
	openAICompatDeveloperRoleCacheMax = 256
)

type openAICompatDeveloperRoleCapabilityKey struct {
	identity       string
	baseURL        string
	model          string
	endpointFamily string
}

type openAICompatDeveloperRoleCacheEntry struct {
	expiresAt time.Time
	order     uint64
}

type openAICompatDeveloperRoleCache struct {
	mu      sync.Mutex
	entries map[openAICompatDeveloperRoleCapabilityKey]openAICompatDeveloperRoleCacheEntry
	now     func() time.Time
	next    uint64
}

func newOpenAICompatDeveloperRoleCache(now func() time.Time) *openAICompatDeveloperRoleCache {
	if now == nil {
		now = time.Now
	}
	return &openAICompatDeveloperRoleCache{
		entries: make(map[openAICompatDeveloperRoleCapabilityKey]openAICompatDeveloperRoleCacheEntry),
		now:     now,
	}
}

func (c *openAICompatDeveloperRoleCache) contains(key openAICompatDeveloperRoleCapabilityKey) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	entry, ok := c.entries[key]
	if !ok {
		return false
	}
	if !entry.expiresAt.After(now) {
		delete(c.entries, key)
		return false
	}
	return true
}

func (c *openAICompatDeveloperRoleCache) add(key openAICompatDeveloperRoleCapabilityKey) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for existingKey, entry := range c.entries {
		if !entry.expiresAt.After(now) {
			delete(c.entries, existingKey)
		}
	}
	if _, exists := c.entries[key]; !exists && len(c.entries) >= openAICompatDeveloperRoleCacheMax {
		var oldestKey openAICompatDeveloperRoleCapabilityKey
		var oldestOrder uint64
		for existingKey, entry := range c.entries {
			if oldestOrder == 0 || entry.order < oldestOrder {
				oldestKey = existingKey
				oldestOrder = entry.order
			}
		}
		delete(c.entries, oldestKey)
	}
	c.next++
	c.entries[key] = openAICompatDeveloperRoleCacheEntry{
		expiresAt: now.Add(openAICompatDeveloperRoleCacheTTL),
		order:     c.next,
	}
}

func (e *OpenAICompatExecutor) developerRoleCacheInstance() *openAICompatDeveloperRoleCache {
	if e == nil {
		return nil
	}
	e.developerRoleCacheMu.Lock()
	defer e.developerRoleCacheMu.Unlock()
	if e.developerRoleCache == nil {
		e.developerRoleCache = newOpenAICompatDeveloperRoleCache(e.now)
	}
	return e.developerRoleCache
}

func (e *OpenAICompatExecutor) developerRoleCapabilityKey(auth *cliproxyauth.Auth, baseURL, model, endpointFamily string) openAICompatDeveloperRoleCapabilityKey {
	identities := []string{strings.TrimSpace(e.provider)}
	if compat := e.resolveCompatConfig(auth); compat != nil && strings.TrimSpace(compat.Name) != "" {
		identities = append(identities, compat.Name)
	} else if auth != nil {
		if auth.Attributes != nil {
			if value := strings.TrimSpace(auth.Attributes["compat_name"]); value != "" {
				identities = append(identities, value)
			} else if value = strings.TrimSpace(auth.Attributes["provider_key"]); value != "" {
				identities = append(identities, value)
			}
		}
		if value := strings.TrimSpace(auth.Provider); value != "" {
			identities = append(identities, value)
		}
	}
	for index := range identities {
		identities[index] = strings.ToLower(strings.TrimSpace(identities[index]))
	}
	return openAICompatDeveloperRoleCapabilityKey{
		identity:       strings.Join(identities, "|"),
		baseURL:        normalizeOpenAICompatCapabilityBaseURL(baseURL),
		model:          strings.ToLower(strings.TrimSpace(model)),
		endpointFamily: strings.ToLower(strings.TrimSpace(endpointFamily)),
	}
}

func normalizeOpenAICompatCapabilityBaseURL(baseURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return strings.TrimRight(strings.TrimSpace(baseURL), "/")
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String()
}

func (e *OpenAICompatExecutor) applyKnownDeveloperRoleFallback(key openAICompatDeveloperRoleCapabilityKey, payload []byte) []byte {
	if !e.developerRoleCacheInstance().contains(key) {
		return payload
	}
	normalized, result, err := translatorcommon.NormalizeLeadingOpenAIInstructions(payload)
	if err != nil || !result.Changed || !result.RemovedAllDeveloper {
		return payload
	}
	return normalized
}

func (e *OpenAICompatExecutor) developerRoleRetryPayload(key openAICompatDeveloperRoleCapabilityKey, payload, responseBody []byte, statusCode int) ([]byte, bool) {
	if key.endpointFamily != "/chat/completions" || !openAICompatRejectsDeveloperRole(statusCode, responseBody) {
		return payload, false
	}
	normalized, result, err := translatorcommon.NormalizeLeadingOpenAIInstructions(payload)
	if err != nil || !result.HadDeveloper || !result.Changed || !result.RemovedAllDeveloper {
		return payload, false
	}
	return normalized, true
}

func (e *OpenAICompatExecutor) rememberDeveloperRoleFallback(key openAICompatDeveloperRoleCapabilityKey) {
	e.developerRoleCacheInstance().add(key)
}

func openAICompatRejectsDeveloperRole(statusCode int, responseBody []byte) bool {
	if (statusCode != http.StatusBadRequest && statusCode != http.StatusOK) || len(responseBody) == 0 {
		return false
	}
	payload := bytes.TrimSpace(responseBody)
	if bytes.HasPrefix(payload, []byte("data:")) {
		payload = bytes.TrimSpace(payload[len("data:"):])
	}
	if !gjson.ValidBytes(payload) {
		return false
	}
	root := gjson.ParseBytes(payload)
	message := firstOpenAICompatErrorString(root, "error.message", "message", "error")
	code := firstOpenAICompatErrorString(root, "error.code", "code")
	typeName := firstOpenAICompatErrorString(root, "error.type", "type")
	if strings.TrimSpace(code) == "1214" && strings.EqualFold(strings.TrimSpace(message), "Incorrect role information") {
		return true
	}
	combined := strings.ToLower(strings.Join([]string{message, code, typeName}, " "))
	if !strings.Contains(combined, "developer") {
		return false
	}
	return strings.Contains(combined, "unsupported role") ||
		strings.Contains(combined, "invalid role") ||
		strings.Contains(combined, "not one of") ||
		strings.Contains(combined, "role must be one of") ||
		strings.Contains(combined, "is not allowed") ||
		strings.Contains(combined, "role is not supported")
}

func openAICompatHasStructuredError(responseBody []byte) bool {
	payload := openAICompatJSONPayload(responseBody)
	if len(payload) == 0 {
		return false
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(payload, &root); err != nil {
		return false
	}
	errorValue, exists := root["error"]
	if !exists {
		return false
	}
	trimmed := bytes.TrimSpace(errorValue)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func openAICompatUsableSuccessBody(endpoint string, responseBody []byte) bool {
	payload := openAICompatJSONPayload(responseBody)
	if len(payload) == 0 {
		return false
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(payload, &root); err != nil {
		return false
	}
	if errorValue, exists := root["error"]; exists {
		trimmed := bytes.TrimSpace(errorValue)
		if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
			return false
		}
	}
	if endpoint == "/chat/completions" {
		return openAICompatJSONArray(root["choices"])
	}
	if openAICompatResponseShape(root) {
		return true
	}
	var response map[string]json.RawMessage
	return json.Unmarshal(root["response"], &response) == nil && openAICompatResponseShape(response)
}

func openAICompatUsableStreamData(data []byte) bool {
	payload := bytes.TrimSpace(data)
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) || openAICompatHasStructuredError(payload) {
		return false
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(payload, &root); err != nil {
		return false
	}
	return openAICompatJSONArray(root["choices"]) || openAICompatResponseShape(root)
}

func openAICompatJSONPayload(responseBody []byte) []byte {
	payload := bytes.TrimSpace(responseBody)
	if bytes.HasPrefix(payload, []byte("data:")) {
		payload = bytes.TrimSpace(payload[len("data:"):])
	}
	return payload
}

func openAICompatJSONArray(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var values []json.RawMessage
	return json.Unmarshal(raw, &values) == nil
}

func openAICompatResponseShape(root map[string]json.RawMessage) bool {
	var id string
	if json.Unmarshal(root["id"], &id) != nil || strings.TrimSpace(id) == "" {
		return false
	}
	return openAICompatJSONArray(root["output"])
}

func firstOpenAICompatErrorString(root gjson.Result, paths ...string) string {
	for _, path := range paths {
		value := root.Get(path)
		if value.Type == gjson.String || value.Type == gjson.Number {
			if text := strings.TrimSpace(value.String()); text != "" {
				return text
			}
		}
	}
	return ""
}

func cloneOpenAICompatRequestWithBody(req *http.Request, body []byte) *http.Request {
	retryReq := req.Clone(req.Context())
	retryReq.Body = http.NoBody
	if len(body) > 0 {
		retryReq.Body = ioNopCloserBytes(body)
	}
	retryReq.ContentLength = int64(len(body))
	retryReq.GetBody = func() (io.ReadCloser, error) {
		return ioNopCloserBytes(body), nil
	}
	return retryReq
}

func ioNopCloserBytes(body []byte) io.ReadCloser {
	return io.NopCloser(bytes.NewReader(body))
}
