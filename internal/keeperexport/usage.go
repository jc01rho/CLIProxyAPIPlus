package keeperexport

import (
	"encoding/json"
)

// TokenStats is the secret-free token counter block of a usage payload
// (contract section 6.2).
type TokenStats struct {
	InputTokens            int64
	OutputTokens           int64
	ReasoningTokens        int64
	CachedTokens           int64
	CacheReadTokens        int64
	CacheReadTokensPresent bool
	CacheCreationTokens    int64
	TotalTokens            int64
}

// FailInfo carries the stable failure classification; the raw upstream failure
// body never crosses the wire.
type FailInfo struct {
	StatusCode int64
	Code       *string
}

// TokenBreakdown mirrors the legacy accounting breakdown fields.
type TokenBreakdown struct {
	Input         int64
	Cached        int64
	CacheRead     int64
	CacheCreation int64
	Reasoning     int64
	Output        int64
}

// UsagePayload is the typed secret-free usage payload (contract section 6.2).
type UsagePayload struct {
	Timestamp           string
	LatencyMs           int64
	TTFTMs              *int64
	Source              string
	AuthIndex           string
	ClientIP            *string
	XForwardedFor       *string
	UserAgent           *string
	Tokens              TokenStats
	Failed              bool
	Generate            bool
	Fail                FailInfo
	AccountingVersion   int64
	TokenBreakdown      TokenBreakdown
	Provider            string
	ExecutorType        string
	Model               string
	Alias               string
	Endpoint            string
	AuthType            string
	APIKeyFingerprint   *string
	RequestID           string
	ReasoningEffort     string
	ServiceTier         string
	ResponseServiceTier *string
	ResponseHeaders     map[string][]string
}

// UsageEvent is one sequenced event; RawPayload preserves the exact payload
// bytes for the contract's replay-equality digest.
type UsageEvent struct {
	Sequence   int64
	Payload    UsagePayload
	RawPayload []byte
}

// UsageBatch is a typed usage batch request (contract section 6.1).
type UsageBatch struct {
	StreamID string
	Events   []UsageEvent
}

// UsageAck is a typed contiguous-ACK response (contract section 6.4).
type UsageAck struct {
	StreamID             string
	AcknowledgedThrough  int64
	NextExpectedSequence int64
	AcceptedCount        int64
	ReplayedCount        int64
}

type usageBatchWire struct {
	ProtocolVersion string           `json:"protocolVersion"`
	StreamID        string           `json:"streamId"`
	Events          []usageEventWire `json:"events"`
}

type usageEventWire struct {
	Sequence int64           `json:"sequence"`
	Payload  json.RawMessage `json:"payload"`
}

type tokenStatsWire struct {
	InputTokens            int64 `json:"input_tokens"`
	OutputTokens           int64 `json:"output_tokens"`
	ReasoningTokens        int64 `json:"reasoning_tokens"`
	CachedTokens           int64 `json:"cached_tokens"`
	CacheReadTokens        int64 `json:"cache_read_tokens"`
	CacheReadTokensPresent bool  `json:"cache_read_tokens_present"`
	CacheCreationTokens    int64 `json:"cache_creation_tokens"`
	TotalTokens            int64 `json:"total_tokens"`
}

type failInfoWire struct {
	StatusCode int64   `json:"status_code"`
	Code       *string `json:"code"`
}

type tokenBreakdownWire struct {
	Input         int64 `json:"input"`
	Cached        int64 `json:"cached"`
	CacheRead     int64 `json:"cache_read"`
	CacheCreation int64 `json:"cache_creation"`
	Reasoning     int64 `json:"reasoning"`
	Output        int64 `json:"output"`
}

type usagePayloadWire struct {
	Timestamp           string              `json:"timestamp"`
	LatencyMs           int64               `json:"latency_ms"`
	TTFTMs              *int64              `json:"ttft_ms"`
	Source              string              `json:"source"`
	AuthIndex           string              `json:"auth_index"`
	ClientIP            *string             `json:"client_ip"`
	XForwardedFor       *string             `json:"x_forwarded_for"`
	UserAgent           *string             `json:"user_agent"`
	Tokens              tokenStatsWire      `json:"tokens"`
	Failed              bool                `json:"failed"`
	Generate            bool                `json:"generate"`
	Fail                failInfoWire        `json:"fail"`
	AccountingVersion   int64               `json:"accounting_version"`
	TokenBreakdown      tokenBreakdownWire  `json:"token_breakdown"`
	Provider            string              `json:"provider"`
	ExecutorType        string              `json:"executor_type"`
	Model               string              `json:"model"`
	Alias               string              `json:"alias"`
	Endpoint            string              `json:"endpoint"`
	AuthType            string              `json:"auth_type"`
	APIKeyFingerprint   *string             `json:"api_key_fingerprint"`
	RequestID           string              `json:"request_id"`
	ReasoningEffort     string              `json:"reasoning_effort"`
	ServiceTier         string              `json:"service_tier"`
	ResponseServiceTier *string             `json:"response_service_tier"`
	ResponseHeaders     map[string][]string `json:"response_headers"`
}

type usageAckWire struct {
	ProtocolVersion      string `json:"protocolVersion"`
	StreamID             string `json:"streamId"`
	AcknowledgedThrough  int64  `json:"acknowledgedThrough"`
	NextExpectedSequence int64  `json:"nextExpectedSequence"`
	AcceptedCount        int64  `json:"acceptedCount"`
	ReplayedCount        int64  `json:"replayedCount"`
}

// allowedResponseHeaders is the exact response-header allowlist from contract
// section 6.2. Authorization, cookies, request IDs, and arbitrary provider
// headers are forbidden.
var allowedResponseHeaders = map[string]struct{}{
	"x-codex-primary-used-percent":          {},
	"x-codex-primary-window-minutes":        {},
	"x-codex-primary-reset-after-seconds":   {},
	"x-codex-secondary-used-percent":        {},
	"x-codex-secondary-window-minutes":      {},
	"x-codex-secondary-reset-after-seconds": {},
}

// failCodes is the exact stable failure-code enum from contract section 6.2.
var failCodes = map[string]struct{}{
	"upstream_http_error":      {},
	"upstream_timeout":         {},
	"upstream_transport_error": {},
	"client_cancelled":         {},
	"internal_error":           {},
}

var usagePayloadRequiredKeys = []string{
	"timestamp", "latency_ms", "ttft_ms", "source", "auth_index", "client_ip",
	"x_forwarded_for", "user_agent", "tokens", "failed", "generate", "fail",
	"accounting_version", "token_breakdown", "provider", "executor_type",
	"model", "alias", "endpoint", "auth_type", "api_key_fingerprint",
	"request_id", "reasoning_effort", "service_tier", "response_service_tier",
	"response_headers",
}

// DecodeUsageBatch strictly decodes and validates a usage batch request body
// per contract section 6. Forward gaps are legal; only non-increasing array
// order is rejected.
func DecodeUsageBatch(data []byte) (*UsageBatch, *Error) {
	if perr := requestPrecheck(data); perr != nil {
		return nil, perr
	}
	var wire usageBatchWire
	if perr := decodeTyped(data, &wire); perr != nil {
		return nil, perr
	}
	if len(wire.Events) == 0 {
		return nil, protocolError("invalid_field")
	}
	if len(wire.Events) > MaxBatchEvents {
		return nil, protocolError("batch_limit_exceeded")
	}
	if !isUUIDv7(wire.StreamID) {
		return nil, protocolError("invalid_field")
	}
	batch := &UsageBatch{StreamID: wire.StreamID, Events: make([]UsageEvent, 0, len(wire.Events))}
	previous := int64(0)
	for i, event := range wire.Events {
		if event.Sequence < 1 || event.Sequence > MaxSafeInteger {
			return nil, protocolError("invalid_field")
		}
		if i > 0 && event.Sequence <= previous {
			return nil, protocolError("invalid_sequence_order")
		}
		previous = event.Sequence
		// The raw payload-byte limit is enforced before any field-level
		// payload validation, per contract fixture 22.
		if len(event.Payload) > MaxPayloadBytes {
			return nil, protocolError("batch_limit_exceeded")
		}
		payload, perr := decodeUsagePayload(event.Payload)
		if perr != nil {
			return nil, perr
		}
		batch.Events = append(batch.Events, UsageEvent{
			Sequence:   event.Sequence,
			Payload:    *payload,
			RawPayload: append([]byte(nil), event.Payload...),
		})
	}
	return batch, nil
}

func decodeUsagePayload(raw json.RawMessage) (*UsagePayload, *Error) {
	if perr := requireKeys(raw, usagePayloadRequiredKeys...); perr != nil {
		return nil, perr
	}
	var wire usagePayloadWire
	if perr := decodeTyped(raw, &wire); perr != nil {
		return nil, perr
	}
	if perr := validateUsagePayload(&wire); perr != nil {
		return nil, perr
	}
	payload := &UsagePayload{
		Timestamp:           wire.Timestamp,
		LatencyMs:           wire.LatencyMs,
		TTFTMs:              wire.TTFTMs,
		Source:              wire.Source,
		AuthIndex:           wire.AuthIndex,
		ClientIP:            wire.ClientIP,
		XForwardedFor:       wire.XForwardedFor,
		UserAgent:           wire.UserAgent,
		Failed:              wire.Failed,
		Generate:            wire.Generate,
		Provider:            wire.Provider,
		ExecutorType:        wire.ExecutorType,
		Model:               wire.Model,
		Alias:               wire.Alias,
		Endpoint:            wire.Endpoint,
		AuthType:            wire.AuthType,
		RequestID:           wire.RequestID,
		ReasoningEffort:     wire.ReasoningEffort,
		ServiceTier:         wire.ServiceTier,
		AccountingVersion:   wire.AccountingVersion,
		APIKeyFingerprint:   wire.APIKeyFingerprint,
		ResponseServiceTier: wire.ResponseServiceTier,
		ResponseHeaders:     wire.ResponseHeaders,
	}
	payload.Tokens = TokenStats{
		InputTokens:            wire.Tokens.InputTokens,
		OutputTokens:           wire.Tokens.OutputTokens,
		ReasoningTokens:        wire.Tokens.ReasoningTokens,
		CachedTokens:           wire.Tokens.CachedTokens,
		CacheReadTokens:        wire.Tokens.CacheReadTokens,
		CacheReadTokensPresent: wire.Tokens.CacheReadTokensPresent,
		CacheCreationTokens:    wire.Tokens.CacheCreationTokens,
		TotalTokens:            wire.Tokens.TotalTokens,
	}
	payload.Fail = FailInfo{StatusCode: wire.Fail.StatusCode, Code: wire.Fail.Code}
	payload.TokenBreakdown = TokenBreakdown{
		Input:         wire.TokenBreakdown.Input,
		Cached:        wire.TokenBreakdown.Cached,
		CacheRead:     wire.TokenBreakdown.CacheRead,
		CacheCreation: wire.TokenBreakdown.CacheCreation,
		Reasoning:     wire.TokenBreakdown.Reasoning,
		Output:        wire.TokenBreakdown.Output,
	}
	return payload, nil
}

func validateUsagePayload(wire *usagePayloadWire) *Error {
	invalid := func() *Error { return protocolError("invalid_field") }
	if !isTimestamp(wire.Timestamp) {
		return invalid()
	}
	if wire.LatencyMs < 0 || wire.LatencyMs > 86400000 {
		return invalid()
	}
	if wire.TTFTMs != nil && (*wire.TTFTMs < 0 || *wire.TTFTMs > 86400000) {
		return invalid()
	}
	if !stringLenInRange(wire.Source, 0, 128) || !stringLenInRange(wire.AuthIndex, 0, 256) {
		return invalid()
	}
	if !stringPtrLenInRange(wire.ClientIP, 1, 64) ||
		!stringPtrLenInRange(wire.XForwardedFor, 1, 512) ||
		!stringPtrLenInRange(wire.UserAgent, 1, 1024) {
		return invalid()
	}
	for _, v := range []int64{
		wire.Tokens.InputTokens, wire.Tokens.OutputTokens, wire.Tokens.ReasoningTokens,
		wire.Tokens.CachedTokens, wire.Tokens.CacheReadTokens, wire.Tokens.CacheCreationTokens,
		wire.Tokens.TotalTokens,
	} {
		if v < 0 || v > MaxSafeInteger {
			return invalid()
		}
	}
	if wire.Fail.StatusCode < 0 || wire.Fail.StatusCode > 599 {
		return invalid()
	}
	if wire.Fail.Code != nil {
		if _, ok := failCodes[*wire.Fail.Code]; !ok {
			return invalid()
		}
	}
	if wire.AccountingVersion < 1 || wire.AccountingVersion > 2147483647 {
		return invalid()
	}
	for _, v := range []int64{
		wire.TokenBreakdown.Input, wire.TokenBreakdown.Cached, wire.TokenBreakdown.CacheRead,
		wire.TokenBreakdown.CacheCreation, wire.TokenBreakdown.Reasoning, wire.TokenBreakdown.Output,
	} {
		if v < 0 || v > MaxSafeInteger {
			return invalid()
		}
	}
	if !stringLenInRange(wire.Provider, 1, 128) ||
		!stringLenInRange(wire.ExecutorType, 1, 128) ||
		!stringLenInRange(wire.Model, 1, 128) ||
		!stringLenInRange(wire.Alias, 1, 128) ||
		!stringLenInRange(wire.AuthType, 1, 128) {
		return invalid()
	}
	if !stringLenInRange(wire.Endpoint, 1, 512) {
		return invalid()
	}
	if wire.APIKeyFingerprint != nil && !isFingerprint(*wire.APIKeyFingerprint) {
		return invalid()
	}
	if !stringLenInRange(wire.RequestID, 1, 256) {
		return invalid()
	}
	if !stringLenInRange(wire.ReasoningEffort, 0, 64) || !stringLenInRange(wire.ServiceTier, 0, 64) {
		return invalid()
	}
	if !stringPtrLenInRange(wire.ResponseServiceTier, 1, 64) {
		return invalid()
	}
	for key, values := range wire.ResponseHeaders {
		if _, ok := allowedResponseHeaders[key]; !ok {
			return invalid()
		}
		if len(values) < 1 || len(values) > 4 {
			return invalid()
		}
		for _, value := range values {
			if !stringLenInRange(value, 1, 64) {
				return invalid()
			}
		}
	}
	return nil
}

// DecodeUsageAck decodes a contiguous-ACK response. Any decode, version, or
// structural failure is normalized to keeper_invalid_response per contract
// section 9: a successful HTTP status with an invalid body must never compact.
func DecodeUsageAck(data []byte) (*UsageAck, *Error) {
	ack, perr := decodeUsageAck(data)
	if perr != nil {
		return nil, protocolError("keeper_invalid_response")
	}
	return ack, nil
}

func decodeUsageAck(data []byte) (*UsageAck, *Error) {
	if perr := responsePrecheck(data); perr != nil {
		return nil, perr
	}
	if perr := requireKeys(data, "protocolVersion", "streamId", "acknowledgedThrough", "nextExpectedSequence", "acceptedCount", "replayedCount"); perr != nil {
		return nil, perr
	}
	var wire usageAckWire
	if perr := decodeTyped(data, &wire); perr != nil {
		return nil, perr
	}
	for _, v := range []int64{wire.AcknowledgedThrough, wire.NextExpectedSequence, wire.AcceptedCount, wire.ReplayedCount} {
		if v < 0 || v > MaxSafeInteger {
			return nil, protocolError("invalid_field")
		}
	}
	if !isUUIDv7(wire.StreamID) {
		return nil, protocolError("invalid_field")
	}
	return &UsageAck{
		StreamID:             wire.StreamID,
		AcknowledgedThrough:  wire.AcknowledgedThrough,
		NextExpectedSequence: wire.NextExpectedSequence,
		AcceptedCount:        wire.AcceptedCount,
		ReplayedCount:        wire.ReplayedCount,
	}, nil
}

// ValidateUsageAck enforces the client-side compaction invariants from
// contract section 6.3: matching stream, monotonicity against the last
// validated ACK, acknowledgedThrough below the next local sequence, contiguous
// nextExpectedSequence, and counts describing exactly the sent batch. Any
// violation is keeper_invalid_response and compaction is forbidden.
func ValidateUsageAck(ack *UsageAck, streamID string, lastAcknowledgedThrough, nextSequence, sentEvents int64) *Error {
	invalid := protocolError("keeper_invalid_response")
	if ack == nil {
		return invalid
	}
	if ack.StreamID != streamID {
		return invalid
	}
	if ack.NextExpectedSequence != ack.AcknowledgedThrough+1 {
		return invalid
	}
	if ack.AcknowledgedThrough < lastAcknowledgedThrough {
		return invalid
	}
	if ack.AcknowledgedThrough >= nextSequence {
		return invalid
	}
	if ack.AcceptedCount+ack.ReplayedCount != sentEvents {
		return invalid
	}
	return nil
}
