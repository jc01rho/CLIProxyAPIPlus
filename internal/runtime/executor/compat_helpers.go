package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tiktoken-go/tokenizer"
)

func newProxyAwareHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	return helps.NewProxyAwareHTTPClient(ctx, cfg, auth, timeout)
}

func parseOpenAIUsage(data []byte) usage.Detail {
	return helps.ParseOpenAIUsage(data)
}

func parseOpenAIStreamUsage(line []byte) (usage.Detail, bool) {
	return helps.ParseOpenAIStreamUsage(line)
}

func parseOpenAIResponsesUsage(data []byte) usage.Detail {
	return helps.ParseOpenAIUsage(data)
}

func parseOpenAIResponsesStreamUsage(line []byte) (usage.Detail, bool) {
	return helps.ParseOpenAIStreamUsage(line)
}

func parseGeminiUsage(data []byte) usage.Detail {
	return helps.ParseGeminiUsage(data)
}

func parseGeminiStreamUsage(line []byte) (usage.Detail, bool) {
	return helps.ParseGeminiStreamUsage(line)
}

func parseGeminiCLIUsage(data []byte) usage.Detail {
	return helps.ParseGeminiCLIUsage(data)
}

func parseGeminiCLIStreamUsage(line []byte) (usage.Detail, bool) {
	return helps.ParseGeminiCLIStreamUsage(line)
}

func parseClaudeUsage(data []byte) usage.Detail {
	return helps.ParseClaudeUsage(data)
}

func parseClaudeStreamUsage(line []byte) (usage.Detail, bool) {
	return helps.ParseClaudeStreamUsage(line)
}

func getTokenizer(model string) (tokenizer.Codec, error) {
	return helps.TokenizerForModel(model)
}

func countOpenAIChatTokens(enc tokenizer.Codec, payload []byte) (int64, error) {
	return helps.CountOpenAIChatTokens(enc, payload)
}

func countClaudeChatTokens(enc tokenizer.Codec, payload []byte) (int64, error) {
	return helps.CountClaudeChatTokens(enc, payload)
}

func buildOpenAIUsageJSON(count int64) []byte {
	return helps.BuildOpenAIUsageJSON(count)
}

type upstreamRequestLog = helps.UpstreamRequestLog

func recordAPIRequest(ctx context.Context, cfg *config.Config, info upstreamRequestLog) {
	helps.RecordAPIRequest(ctx, cfg, info)
}

func recordAPIResponseMetadata(ctx context.Context, cfg *config.Config, status int, headers http.Header) {
	helps.RecordAPIResponseMetadata(ctx, cfg, status, headers)
}

func recordAPIResponseError(ctx context.Context, cfg *config.Config, err error) {
	helps.RecordAPIResponseError(ctx, cfg, err)
}

func appendAPIResponseChunk(ctx context.Context, cfg *config.Config, chunk []byte) {
	helps.AppendAPIResponseChunk(ctx, cfg, chunk)
}

func payloadRequestedModel(opts cliproxyexecutor.Options, fallback string) string {
	return helps.PayloadRequestedModel(opts, fallback)
}

func applyPayloadConfigWithRoot(cfg *config.Config, model, protocol, root string, payload, original []byte, requestedModel string) []byte {
	return helps.ApplyPayloadConfigWithRoot(cfg, model, protocol, root, payload, original, requestedModel)
}

func summarizeErrorBody(contentType string, body []byte) string {
	return helps.SummarizeErrorBody(contentType, body)
}

func FilterSSEUsageMetadata(payload []byte) []byte {
	return helps.FilterSSEUsageMetadata(payload)
}

func logWithRequestID(ctx context.Context) *log.Entry {
	return helps.LogWithRequestID(ctx)
}

func logDetailedAPIError(ctx context.Context, provider string, model string, url string, statusCode int, contentType string, body []byte) {
	entry := logWithRequestID(ctx)
	logFn := entry.Warnf
	if statusCode >= 500 {
		logFn = entry.Errorf
	}

	bodyStr := string(body)
	if len(bodyStr) > 4096 {
		bodyStr = bodyStr[:4096] + "...[truncated]"
	}

	providerDisplay := provider
	if ctxProvider, authID, authLabel := cliproxyauth.GetProviderAuthFromContext(ctx); ctxProvider != "" {
		displayAuth := authLabel
		if displayAuth == "" {
			displayAuth = authID
		}
		if displayAuth != "" {
			providerDisplay = fmt.Sprintf("%s:%s", provider, displayAuth)
		}
	}
	model = strings.TrimSpace(model)
	if model != "" {
		providerDisplay = fmt.Sprintf("%s model=%s", providerDisplay, model)
	}

	logFn("[%s] API error - URL: %s, Status: %d, Content-Type: %s, Response: %s",
		providerDisplay, url, statusCode, contentType, bodyStr)
}

func jsonPayload(line []byte) []byte {
	return helps.JSONPayload(line)
}

func cachedUserID(apiKey string) string {
	return helps.CachedUserID(apiKey)
}

func generateFakeUserID() string {
	return helps.GenerateFakeUserID()
}

func isValidUserID(userID string) bool {
	return helps.IsValidUserID(userID)
}

func shouldCloak(cloakMode string, userAgent string) bool {
	return helps.ShouldCloak(cloakMode, userAgent)
}

type claudeDeviceProfile = helps.ClaudeDeviceProfile

func claudeDeviceProfileStabilizationEnabled(cfg *config.Config) bool {
	return helps.ClaudeDeviceProfileStabilizationEnabled(cfg)
}

func resolveClaudeDeviceProfile(auth *cliproxyauth.Auth, apiKey string, headers http.Header, cfg *config.Config) claudeDeviceProfile {
	return helps.ResolveClaudeDeviceProfile(auth, apiKey, headers, cfg)
}

func applyClaudeDeviceProfileHeaders(r *http.Request, profile claudeDeviceProfile) {
	helps.ApplyClaudeDeviceProfileHeaders(r, profile)
}

func applyClaudeLegacyDeviceHeaders(r *http.Request, ginHeaders http.Header, cfg *config.Config) {
	helps.ApplyClaudeLegacyDeviceHeaders(r, ginHeaders, cfg)
}

func buildSensitiveWordMatcher(words []string) *helps.SensitiveWordMatcher {
	return helps.BuildSensitiveWordMatcher(words)
}

func obfuscateSensitiveWords(payload []byte, matcher *helps.SensitiveWordMatcher) []byte {
	return helps.ObfuscateSensitiveWords(payload, matcher)
}

func apiKeyFromContext(ctx context.Context) string {
	return helps.APIKeyFromContext(ctx)
}

func tokenizerForModel(model string) (tokenizer.Codec, error) {
	return helps.TokenizerForModel(model)
}

func collectOpenAIContent(content gjson.Result, segments *[]string) {
	helps.CollectOpenAIContent(content, segments)
}

type usageReporter struct {
	reporter *helps.UsageReporter
}

func newUsageReporter(ctx context.Context, provider, model string, auth *cliproxyauth.Auth) *usageReporter {
	return &usageReporter{reporter: helps.NewUsageReporter(ctx, provider, model, auth)}
}

func (r *usageReporter) publish(ctx context.Context, detail usage.Detail) {
	if r == nil || r.reporter == nil {
		return
	}
	r.reporter.Publish(ctx, detail)
}

func (r *usageReporter) publishFailure(ctx context.Context) {
	if r == nil || r.reporter == nil {
		return
	}
	r.reporter.PublishFailure(ctx)
}

func (r *usageReporter) trackFailure(ctx context.Context, errPtr *error) {
	if r == nil || r.reporter == nil {
		return
	}
	r.reporter.TrackFailure(ctx, errPtr)
}

func (r *usageReporter) ensurePublished(ctx context.Context) {
	if r == nil || r.reporter == nil {
		return
	}
	r.reporter.EnsurePublished(ctx)
}
