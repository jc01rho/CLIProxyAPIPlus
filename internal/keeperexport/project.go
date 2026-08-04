package keeperexport

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// SnapshotInput is copied from the live service under its own locks.
type SnapshotInput struct {
	Config config.Config
	Auths  []*coreauth.Auth
}

func ProjectUsage(ctx context.Context, record coreusage.Record, secret []byte, privacy config.UsageExportPrivacyConfig) ([]byte, error) {
	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}
	detail := coreusage.EnsureTokenBreakdownForProvider(record.Detail, record.Provider, record.ExecutorType)
	metadata := internallogging.GetClientRequestMetadata(ctx)
	// Record.Source is a legacy display/grouping value and may fall back to a
	// raw provider or client API key. The Keeper wire boundary must derive its
	// source only from existing non-secret CPA-local decision identifiers; the
	// fingerprint below is the sole API-key identity exported.
	source := bounded(firstNonblank(record.AuthIndex, record.Provider, record.ExecutorType, record.AuthType), 128)
	payload := usagePayloadWire{
		Timestamp:         timestamp.UTC().Format("2006-01-02T15:04:05.000Z"),
		LatencyMs:         clampDurationMillis(record.Latency),
		Source:            source,
		AuthIndex:         bounded(strings.TrimSpace(record.AuthIndex), 256),
		Failed:            record.Failed || responseFailed(ctx),
		Generate:          coreusage.GenerateEnabled(record.Generate),
		AccountingVersion: coreusage.TokenAccountingSchemaVersion,
		Provider:          requiredUnknown(record.Provider),
		ExecutorType:      requiredUnknown(record.ExecutorType),
		Model:             requiredUnknown(record.Model),
		Alias:             requiredUnknown(firstNonblank(record.Alias, record.Model)),
		Endpoint:          requiredUnknown(internallogging.GetEndpoint(ctx)),
		AuthType:          requiredUnknown(record.AuthType),
		RequestID:         bounded(strings.TrimSpace(internallogging.GetRequestID(ctx)), 256),
		ReasoningEffort:   bounded(firstNonblank(record.ReasoningEffort, coreusage.ReasoningEffortFromContext(ctx)), 64),
		ServiceTier:       bounded(firstNonblank(record.ServiceTier, record.RequestServiceTier, coreusage.ServiceTierFromContext(ctx)), 64),
		ResponseHeaders:   filterResponseHeaders(record.ResponseHeaders),
	}
	if payload.RequestID == "" {
		return nil, fmt.Errorf("keeper usage projection requires request ID")
	}
	if record.TTFT > 0 {
		value := clampDurationMillis(record.TTFT)
		payload.TTFTMs = &value
	}
	if privacy.IncludeClientIP {
		payload.ClientIP = optionalBounded(metadata.ClientIP, 64)
	}
	if privacy.IncludeForwardedFor {
		payload.XForwardedFor = optionalBounded(metadata.XForwardedFor, 512)
	}
	if privacy.IncludeUserAgent {
		payload.UserAgent = optionalBounded(metadata.UserAgent, 1024)
	}
	payload.APIKeyFingerprint = APIKeyFingerprint(secret, []byte(record.APIKey))
	record.APIKey = ""
	payload.Tokens = tokenStatsWire{detail.InputTokens, detail.OutputTokens, detail.ReasoningTokens, detail.CachedTokens, detail.CacheReadTokens, true, detail.CacheCreationTokens, detail.TotalTokens}
	payload.TokenBreakdown = tokenBreakdownWire{detail.InputTokens, detail.CachedTokens, detail.CacheReadTokens, detail.CacheCreationTokens, detail.ReasoningTokens, detail.OutputTokens}
	payload.Fail.StatusCode = int64(record.Fail.StatusCode)
	if !payload.Failed {
		payload.Fail.StatusCode = http.StatusOK
	} else {
		if payload.Fail.StatusCode <= 0 {
			payload.Fail.StatusCode = int64(internallogging.GetResponseStatus(ctx))
		}
		if payload.Fail.StatusCode <= 0 {
			payload.Fail.StatusCode = http.StatusInternalServerError
		}
		code := classifyFailure(ctx, record)
		payload.Fail.Code = &code
	}
	if tier := strings.TrimSpace(record.ResponseServiceTier); tier != "" {
		payload.ResponseServiceTier = optionalBounded(tier, 64)
	}
	body, err := json.Marshal(payload)
	if err != nil || len(body) > MaxPayloadBytes {
		return nil, fmt.Errorf("marshal keeper usage projection: %w", err)
	}
	if _, perr := decodeUsagePayload(body); perr != nil {
		return nil, perr
	}
	return body, nil
}

func classifyFailure(ctx context.Context, record coreusage.Record) string {
	if ctx != nil && ctx.Err() != nil {
		return "client_cancelled"
	}
	if record.Fail.StatusCode > 0 || internallogging.GetResponseStatus(ctx) > 0 {
		return "upstream_http_error"
	}
	return "upstream_transport_error"
}

func responseFailed(ctx context.Context) bool {
	status := internallogging.GetResponseStatus(ctx)
	return status >= 400
}

func filterResponseHeaders(headers http.Header) map[string][]string {
	if len(headers) == 0 {
		return nil
	}
	filtered := make(map[string][]string)
	for rawKey, values := range headers {
		key := strings.ToLower(strings.TrimSpace(rawKey))
		if _, ok := allowedResponseHeaders[key]; !ok {
			continue
		}
		for _, value := range values {
			value = bounded(strings.TrimSpace(value), 64)
			if value != "" && len(filtered[key]) < 4 {
				filtered[key] = append(filtered[key], value)
			}
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func ProjectMetadata(input SnapshotInput, secret []byte, category MetadataCategory, revision int64, now time.Time) ([]byte, []byte, error) {
	var items any
	switch category {
	case CategoryAPIKeys:
		projected := make([]apiKeyItemWire, 0, len(input.Config.APIKeys))
		for _, raw := range input.Config.APIKeys {
			if fingerprint := APIKeyFingerprint(secret, []byte(raw)); fingerprint != nil {
				projected = append(projected, apiKeyItemWire{Fingerprint: *fingerprint, DisplayKey: maskKey(raw), Alias: ""})
			}
		}
		sort.Slice(projected, func(i, j int) bool { return projected[i].Fingerprint < projected[j].Fingerprint })
		items = projected
	case CategoryAuthFiles:
		projected := make([]authFileItemWire, 0)
		for _, auth := range input.Auths {
			if auth == nil || strings.TrimSpace(auth.FileName) == "" || strings.EqualFold(auth.Attributes["auth_kind"], "apikey") {
				continue
			}
			projected = append(projected, projectAuthFile(auth))
		}
		sort.Slice(projected, func(i, j int) bool { return projected[i].AuthIndex < projected[j].AuthIndex })
		items = projected
	case CategoryProviderIdentities:
		projected := make([]providerIdentityItemWire, 0)
		for _, auth := range input.Auths {
			if auth == nil {
				continue
			}
			projected = append(projected, projectProvider(auth, secret))
		}
		sort.Slice(projected, func(i, j int) bool {
			if projected[i].ProviderType == projected[j].ProviderType {
				return projected[i].AuthIndex < projected[j].AuthIndex
			}
			return projected[i].ProviderType < projected[j].ProviderType
		})
		items = projected
	default:
		return nil, nil, fmt.Errorf("unsupported metadata category")
	}
	itemBytes, err := json.Marshal(items)
	if err != nil {
		return nil, nil, err
	}
	digest := sha256.Sum256(itemBytes)
	envelope := struct {
		ProtocolVersion string `json:"protocolVersion"`
		Revision        int64  `json:"revision"`
		Complete        bool   `json:"complete"`
		GeneratedAt     string `json:"generatedAt"`
		Items           any    `json:"items"`
	}{ProtocolVersion: ProtocolVersion, Revision: revision, Complete: true, GeneratedAt: now.UTC().Format("2006-01-02T15:04:05.000Z"), Items: items}
	body, err := json.Marshal(envelope)
	if err != nil || len(body) > MaxBodyBytes {
		return nil, nil, fmt.Errorf("marshal metadata snapshot: %w", err)
	}
	if _, perr := DecodeMetadataSnapshot(body, category); perr != nil {
		return nil, nil, perr
	}
	return body, digest[:], nil
}

func projectAuthFile(auth *coreauth.Auth) authFileItemWire {
	priority := optionalInt(auth.Attributes["priority"])
	disabled := auth.Disabled
	item := authFileItemWire{AuthIndex: stableAuthIndex(auth), Name: bounded(filepath.Base(auth.FileName), 256), DisplayName: bounded(firstNonblank(auth.Label, metadataString(auth, "email")), 256), Type: bounded(firstNonblank(metadataString(auth, "type"), auth.Provider), 128), Provider: bounded(auth.Provider, 256), Prefix: bounded(auth.Prefix, 256), Priority: priority, Disabled: &disabled, Note: optionalBounded(auth.Attributes["note"], 1024), AccountID: optionalBounded(metadataString(auth, "account_id"), 256), ProjectID: optionalBounded(metadataString(auth, "project_id"), 256), XAIUserID: optionalBounded(metadataString(auth, "xai_user_id"), 256), PlanType: optionalBounded(firstNonblank(auth.Attributes["plan_type"], metadataString(auth, "plan_type")), 256)}
	return item
}

func projectProvider(auth *coreauth.Auth, secret []byte) providerIdentityItemWire {
	disabled := auth.Disabled
	baseURL := safeHTTPSURL(auth.Attributes["base_url"])
	return providerIdentityItemWire{AuthIndex: stableAuthIndex(auth), ProviderType: requiredUnknown(auth.Provider), DisplayName: bounded(firstNonblank(auth.Label, auth.Provider), 256), Prefix: bounded(auth.Prefix, 256), BaseURL: baseURL, Priority: optionalInt(auth.Attributes["priority"]), Disabled: &disabled, Note: optionalBounded(auth.Attributes["note"], 1024), APIKeyFingerprint: APIKeyFingerprint(secret, []byte(auth.Attributes["api_key"]))}
}

func safeHTTPSURL(raw string) *string {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil
	}
	return &raw
}
func stableAuthIndex(auth *coreauth.Auth) string {
	return bounded(firstNonblank(auth.Index, auth.ID), 256)
}
func metadataString(auth *coreauth.Auth, key string) string {
	if auth == nil {
		return ""
	}
	value, _ := auth.Metadata[key].(string)
	return strings.TrimSpace(value)
}
func optionalInt(raw string) *int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value < -1000000 || value > 1000000 {
		return nil
	}
	return &value
}
func maskKey(raw string) string {
	r := []rune(raw)
	if len(r) <= 8 {
		return "****"
	}
	return string(r[:4]) + "..." + string(r[len(r)-4:])
}
func clampDurationMillis(value time.Duration) int64 {
	ms := value.Milliseconds()
	if ms < 0 {
		return 0
	}
	if ms > 86400000 {
		return 86400000
	}
	return ms
}

// boundedTrim trims whitespace and returns a UTF-8-safe view of value whose
// byte length does not exceed max. If the cap falls inside a multi-byte rune,
// the slice is truncated at the last valid code-point boundary that fits. The
// returned string is always valid UTF-8 and never exceeds max bytes.
func boundedTrim(value string, max int) string {
	value = strings.TrimSpace(value)
	if max <= 0 || len(value) <= max {
		return value
	}
	// Walk back from max bytes to the nearest utf8.RuneStart boundary so we
	// never split a multi-byte code point. The contract also requires the
	// result to be valid UTF-8, so validate the suffix before returning.
	cut := max
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	if cut == 0 {
		return ""
	}
	return value[:cut]
}

// bounded is the legacy alias kept for the existing call sites; it now defers
// to boundedTrim so byte-slicing can no longer split a multi-byte code point.
func bounded(value string, max int) string {
	return boundedTrim(value, max)
}
func optionalBounded(value string, max int) *string {
	value = bounded(value, max)
	if value == "" {
		return nil
	}
	return &value
}
func requiredUnknown(value string) string {
	value = bounded(value, 128)
	if value == "" {
		return "unknown"
	}
	return value
}
func firstNonblank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
