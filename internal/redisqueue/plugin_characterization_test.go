package redisqueue

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// TestLegacyUsagePayloadCharacterization freezes the current pre-export behavior of
// usageQueuePlugin.HandleUsage and the in-memory queue. These tests document the
// baseline the keeper-export/v1 contract must remain compatible with (semantic
// fields) and must replace (volatile delivery, raw secrets on the wire).
func TestLegacyUsagePayloadCharacterization(t *testing.T) {
	t.Run("EmitsEveryFieldKeeperDecoderExpectsWithoutDeliverySequence", func(t *testing.T) {
		withEnabledQueue(t, func() {
			ctx := internallogging.WithRequestID(context.Background(), "char-req-1")
			ctx = internallogging.WithEndpoint(ctx, "POST /v1/responses")
			ctx = internallogging.WithClientRequestMetadata(ctx, internallogging.ClientRequestMetadata{
				ClientIP:      "192.0.2.10",
				XForwardedFor: "203.0.113.5",
				UserAgent:     "char-client/1.0",
			})
			ctx = internallogging.WithResponseStatusHolder(ctx)
			internallogging.SetResponseStatus(ctx, http.StatusOK)

			headers := http.Header{}
			headers.Set("X-Codex-Primary-Used-Percent", "12")

			(&usageQueuePlugin{}).HandleUsage(ctx, coreusage.Record{
				Provider:            "openai",
				ExecutorType:        "CodexExecutor",
				Model:               "gpt-5.6",
				Alias:               "client-gpt",
				APIKey:              "sk-char-key",
				AuthIndex:           "auth-0",
				AuthType:            "apikey",
				Source:              "openai",
				ReasoningEffort:     "medium",
				ServiceTier:         "default",
				ResponseServiceTier: "default",
				RequestedAt:         time.Date(2026, 8, 3, 12, 34, 56, 789000000, time.UTC),
				Latency:             1500 * time.Millisecond,
				TTFT:                45 * time.Millisecond,
				Detail: coreusage.Detail{
					InputTokens:     10,
					OutputTokens:    4,
					ReasoningTokens: 1,
					TotalTokens:     15,
				},
				ResponseHeaders: headers.Clone(),
			})

			payload := popSinglePayload(t)
			// Every field the current Keeper decoder consumes must be present.
			for _, key := range []string{
				"accounting_version", "token_breakdown", "provider", "executor_type",
				"model", "alias", "endpoint", "auth_type", "api_key", "request_id",
				"reasoning_effort", "service_tier", "response_service_tier",
				"timestamp", "latency_ms", "ttft_ms", "source", "auth_index",
				"client_ip", "x_forwarded_for", "user_agent", "tokens", "failed",
				"generate", "fail", "response_headers",
			} {
				if _, ok := payload[key]; !ok {
					t.Fatalf("legacy payload missing field %q expected by Keeper decoder", key)
				}
			}
			// The legacy payload has no durable delivery coordinate whatsoever.
			for _, key := range []string{"sequence", "stream_id", "streamId", "protocolVersion", "instance_id", "instanceId"} {
				requireMissingField(t, payload, key)
			}
			requireStringField(t, payload, "request_id", "char-req-1")
		})
	})

	t.Run("DuplicateRequestIDsProduceTwoQueueItems", func(t *testing.T) {
		withEnabledQueue(t, func() {
			ctx := internallogging.WithRequestID(context.Background(), "dup-req-id")
			ctx = internallogging.WithResponseStatusHolder(ctx)
			internallogging.SetResponseStatus(ctx, http.StatusOK)

			record := coreusage.Record{
				Provider: "openai",
				Model:    "gpt-5.6",
				Detail:   coreusage.Detail{InputTokens: 1, TotalTokens: 1},
			}
			(&usageQueuePlugin{}).HandleUsage(ctx, record)
			(&usageQueuePlugin{}).HandleUsage(ctx, record)

			items := PopOldest(10)
			if len(items) != 2 {
				t.Fatalf("PopOldest() items = %d, want 2: duplicate request IDs must stay independent queue records", len(items))
			}
			for i, raw := range items {
				var payload map[string]json.RawMessage
				if err := json.Unmarshal(raw, &payload); err != nil {
					t.Fatalf("unmarshal payload %d: %v", i, err)
				}
				requireStringField(t, payload, "request_id", "dup-req-id")
			}
		})
	})

	t.Run("RawSecretsPresentInLegacyPayload", func(t *testing.T) {
		// Documents why the v1 projection must sanitize before any push append:
		// the legacy queue envelope carries the raw API key, the raw upstream
		// failure body, and arbitrary response headers.
		withEnabledQueue(t, func() {
			ctx := internallogging.WithRequestID(context.Background(), "secret-req")
			ctx = internallogging.WithResponseStatusHolder(ctx)
			internallogging.SetResponseStatus(ctx, http.StatusBadGateway)

			headers := http.Header{}
			headers.Set("Authorization", "Bearer upstream-secret")
			headers.Set("X-Upstream-Request-Id", "upstream-req-9")

			(&usageQueuePlugin{}).HandleUsage(ctx, coreusage.Record{
				Provider: "openai",
				Model:    "gpt-5.6",
				APIKey:   "sk-raw-client-secret",
				Fail: coreusage.Failure{
					StatusCode: http.StatusBadGateway,
					Body:       "raw upstream failure body with provider detail",
				},
				Detail:          coreusage.Detail{InputTokens: 1, TotalTokens: 1},
				ResponseHeaders: headers.Clone(),
			})

			payload := popSinglePayload(t)
			requireStringField(t, payload, "api_key", "sk-raw-client-secret")
			requireFailField(t, payload, http.StatusBadGateway, "raw upstream failure body with provider detail")
			requireHeaderField(t, payload, "response_headers", "Authorization", []string{"Bearer upstream-secret"})
		})
	})

	t.Run("RetentionIsVolatileAndCannotSatisfyDurableExport", func(t *testing.T) {
		t.Run("DisableClearsQueueLikeRestart", func(t *testing.T) {
			withEnabledQueue(t, func() {
				ctx := internallogging.WithResponseStatusHolder(context.Background())
				internallogging.SetResponseStatus(ctx, http.StatusOK)
				(&usageQueuePlugin{}).HandleUsage(ctx, coreusage.Record{
					Provider: "openai",
					Model:    "gpt-5.6",
					Detail:   coreusage.Detail{InputTokens: 1, TotalTokens: 1},
				})

				// Toggling the queue (config reload / process restart equivalent)
				// destroys everything retained in memory.
				SetEnabled(false)
				SetEnabled(true)
				SetUsageStatisticsEnabled(true)

				if items := PopOldest(10); len(items) != 0 {
					t.Fatalf("PopOldest() after disable = %d items, want 0: queue must be volatile", len(items))
				}
			})
		})

		t.Run("PopConsumesWithoutAckOrRedelivery", func(t *testing.T) {
			withEnabledQueue(t, func() {
				ctx := internallogging.WithResponseStatusHolder(context.Background())
				internallogging.SetResponseStatus(ctx, http.StatusOK)
				(&usageQueuePlugin{}).HandleUsage(ctx, coreusage.Record{
					Provider: "openai",
					Model:    "gpt-5.6",
					Detail:   coreusage.Detail{InputTokens: 1, TotalTokens: 1},
				})

				if items := PopOldest(1); len(items) != 1 {
					t.Fatalf("first PopOldest() = %d items, want 1", len(items))
				}
				// No ACK/compaction protocol exists: a crash after pop loses the item forever.
				if items := PopOldest(10); len(items) != 0 {
					t.Fatalf("second PopOldest() = %d items, want 0: no redelivery", len(items))
				}
			})
		})

		t.Run("SubscriberFanoutIsNotRetained", func(t *testing.T) {
			withEnabledQueue(t, func() {
				messages, unsubscribe := SubscribeUsage()
				defer unsubscribe()
				<-messages // consume the support-refresh handshake payload

				ctx := internallogging.WithResponseStatusHolder(context.Background())
				internallogging.SetResponseStatus(ctx, http.StatusOK)
				(&usageQueuePlugin{}).HandleUsage(ctx, coreusage.Record{
					Provider: "openai",
					Model:    "gpt-5.6",
					Detail:   coreusage.Detail{InputTokens: 1, TotalTokens: 1},
				})

				select {
				case <-messages:
				case <-time.After(2 * time.Second):
					t.Fatal("subscriber did not receive usage payload")
				}
				// Published payloads bypass retention entirely.
				if items := PopOldest(10); len(items) != 0 {
					t.Fatalf("PopOldest() with active subscriber = %d items, want 0", len(items))
				}
			})
		})
	})
}
