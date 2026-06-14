package antigravity

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultTokenBucketMaxTokens is the maximum token balance per account.
	DefaultTokenBucketMaxTokens = 50
	// DefaultTokenBucketRegenerationRatePerMinute is the per-minute regeneration rate.
	DefaultTokenBucketRegenerationRatePerMinute = 6
	// DefaultTokenBucketInitialTokens is the starting token balance for new accounts.
	DefaultTokenBucketInitialTokens = 50
	// FetchTimeoutMS is the reference fetchAvailableModels timeout in milliseconds.
	FetchTimeoutMS = 10000
)

// FetchAvailableModelsEndpointFallbacks is the endpoint fallback order for quota fetches.
var FetchAvailableModelsEndpointFallbacks = []string{AntigravityEndpointDaily, "https://daily-cloudcode-pa.sandbox.googleapis.com", AntigravityEndpointProd}

// TokenBucketConfig controls the client-side token bucket used to avoid 429s.
type TokenBucketConfig struct{ MaxTokens, RegenerationRatePerMinute, InitialTokens float64 }

// DefaultTokenBucketConfig returns the reference Antigravity token-bucket configuration.
func DefaultTokenBucketConfig() TokenBucketConfig {
	return TokenBucketConfig{DefaultTokenBucketMaxTokens, DefaultTokenBucketRegenerationRatePerMinute, DefaultTokenBucketInitialTokens}
}

type tokenBucketState struct {
	Tokens      float64
	LastUpdated time.Time
}

// TokenBucketTracker implements client-side rate limiting with token regeneration.
type TokenBucketTracker struct {
	mu      sync.RWMutex
	buckets map[int]tokenBucketState
	config  TokenBucketConfig
}

// NewTokenBucketTracker creates a token bucket tracker; zero fields use defaults.
func NewTokenBucketTracker(config TokenBucketConfig) *TokenBucketTracker {
	cfg := DefaultTokenBucketConfig()
	if config.MaxTokens != 0 {
		cfg.MaxTokens = config.MaxTokens
	}
	if config.RegenerationRatePerMinute != 0 {
		cfg.RegenerationRatePerMinute = config.RegenerationRatePerMinute
	}
	if config.InitialTokens != 0 {
		cfg.InitialTokens = config.InitialTokens
	}
	return &TokenBucketTracker{buckets: map[int]tokenBucketState{}, config: cfg}
}

// GetTokens returns current tokens after minutesSinceUpdate*regenerationRatePerMinute recovery.
func (t *TokenBucketTracker) GetTokens(accountIndex int) float64 {
	if t == nil {
		return DefaultTokenBucketInitialTokens
	}
	t.mu.RLock()
	state, ok := t.buckets[accountIndex]
	cfg := t.config
	t.mu.RUnlock()
	if !ok {
		return cfg.InitialTokens
	}
	return math.Min(cfg.MaxTokens, state.Tokens+time.Since(state.LastUpdated).Minutes()*cfg.RegenerationRatePerMinute)
}

// HasTokens reports whether an account has enough tokens; cost defaults to 1.
func (t *TokenBucketTracker) HasTokens(accountIndex int, cost ...float64) bool {
	c := 1.0
	if len(cost) > 0 {
		c = cost[0]
	}
	return t.GetTokens(accountIndex) >= c
}

// Consume removes tokens and returns false if insufficient; cost defaults to 1.
func (t *TokenBucketTracker) Consume(accountIndex int, cost ...float64) bool {
	if t == nil {
		return false
	}
	c := 1.0
	if len(cost) > 0 {
		c = cost[0]
	}
	current := t.GetTokens(accountIndex)
	if current < c {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buckets[accountIndex] = tokenBucketState{Tokens: current - c, LastUpdated: time.Now()}
	return true
}

// Refund adds tokens back, capped at MaxTokens; amount defaults to 1.
func (t *TokenBucketTracker) Refund(accountIndex int, amount ...float64) {
	if t == nil {
		return
	}
	a := 1.0
	if len(amount) > 0 {
		a = amount[0]
	}
	current := t.GetTokens(accountIndex)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buckets[accountIndex] = tokenBucketState{Tokens: math.Min(t.config.MaxTokens, current+a), LastUpdated: time.Now()}
}

// GetMaxTokens returns the configured maximum token balance.
func (t *TokenBucketTracker) GetMaxTokens() float64 {
	if t == nil {
		return DefaultTokenBucketMaxTokens
	}
	return t.config.MaxTokens
}

var globalTokenTrackerMu sync.Mutex
var globalTokenTracker *TokenBucketTracker

// GetTokenTracker returns the global token bucket tracker.
func GetTokenTracker() *TokenBucketTracker {
	globalTokenTrackerMu.Lock()
	defer globalTokenTrackerMu.Unlock()
	if globalTokenTracker == nil {
		globalTokenTracker = NewTokenBucketTracker(TokenBucketConfig{})
	}
	return globalTokenTracker
}

// InitTokenTracker replaces the global token bucket tracker.
func InitTokenTracker(config TokenBucketConfig) *TokenBucketTracker {
	globalTokenTrackerMu.Lock()
	defer globalTokenTrackerMu.Unlock()
	globalTokenTracker = NewTokenBucketTracker(config)
	return globalTokenTracker
}

// QuotaGroup identifies an Antigravity quota family.
type QuotaGroup string

const (
	QuotaGroupClaude      QuotaGroup = "claude"
	QuotaGroupGeminiPro   QuotaGroup = "gemini-pro"
	QuotaGroupGeminiFlash QuotaGroup = "gemini-flash"
	QuotaGroupGPTOss      QuotaGroup = "gpt-oss"
)

// QuotaGroupSummary summarizes a model group's most restrictive quota.
type QuotaGroupSummary struct {
	RemainingFraction *float64 `json:"remainingFraction,omitempty"`
	ResetTime         string   `json:"resetTime,omitempty"`
	ModelCount        int      `json:"modelCount"`
}

// PerModelQuotaEntry preserves per-model quota details.
type PerModelQuotaEntry struct {
	ModelID           string      `json:"modelId"`
	DisplayName       string      `json:"displayName,omitempty"`
	Group             *QuotaGroup `json:"group,omitempty"`
	RemainingFraction float64     `json:"remainingFraction"`
	ResetTime         string      `json:"resetTime,omitempty"`
}

// QuotaSummary is the aggregated Antigravity quota response.
type QuotaSummary struct {
	Groups     map[QuotaGroup]QuotaGroupSummary `json:"groups"`
	PerModel   []PerModelQuotaEntry             `json:"perModel,omitempty"`
	ModelCount int                              `json:"modelCount"`
	Error      string                           `json:"error,omitempty"`
}

// GeminiCLIQuotaModel is a single Gemini CLI quota bucket.
type GeminiCLIQuotaModel struct {
	ModelID           string  `json:"modelId"`
	RemainingFraction float64 `json:"remainingFraction"`
	ResetTime         string  `json:"resetTime,omitempty"`
}

// GeminiCLIQuotaSummary is the filtered Gemini CLI quota summary.
type GeminiCLIQuotaSummary struct {
	Models []GeminiCLIQuotaModel `json:"models"`
	Error  string                `json:"error,omitempty"`
}

// RetrieveUserQuotaResponse is the retrieveUserQuota response.
type RetrieveUserQuotaResponse struct {
	Buckets []RetrieveUserQuotaBucket `json:"buckets,omitempty"`
}

// RetrieveUserQuotaBucket is one raw quota bucket.
type RetrieveUserQuotaBucket struct {
	RemainingAmount   string   `json:"remainingAmount,omitempty"`
	RemainingFraction *float64 `json:"remainingFraction,omitempty"`
	ResetTime         string   `json:"resetTime,omitempty"`
	TokenType         string   `json:"tokenType,omitempty"`
	ModelID           string   `json:"modelId,omitempty"`
}

// FetchAvailableModelsResponse is the fetchAvailableModels quota response.
type FetchAvailableModelsResponse struct {
	Models map[string]FetchAvailableModelEntry `json:"models,omitempty"`
}

// FetchAvailableModelEntry contains display and quota fields.
type FetchAvailableModelEntry struct {
	QuotaInfo   *FetchAvailableQuotaInfo `json:"quotaInfo,omitempty"`
	DisplayName string                   `json:"displayName,omitempty"`
	ModelName   string                   `json:"modelName,omitempty"`
}

// FetchAvailableQuotaInfo contains remainingFraction and resetTime.
type FetchAvailableQuotaInfo struct {
	RemainingFraction *float64 `json:"remainingFraction,omitempty"`
	ResetTime         string   `json:"resetTime,omitempty"`
}

// NormalizeRemainingFraction clamps invalid or missing values to 0 and valid values to [0,1].
func NormalizeRemainingFraction(value any) float64 {
	var f float64
	switch v := value.(type) {
	case nil:
		return 0
	case float64:
		f = v
	case float32:
		f = float64(v)
	case int:
		f = float64(v)
	case int64:
		f = float64(v)
	case json.Number:
		x, e := v.Float64()
		if e != nil {
			return 0
		}
		f = x
	case *float64:
		if v == nil {
			return 0
		}
		f = *v
	default:
		return 0
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

// ParseResetTime parses a reset time; empty or invalid values return nil.
func ParseResetTime(resetTime string) *time.Time {
	if resetTime == "" {
		return nil
	}
	if t, e := time.Parse(time.RFC3339Nano, resetTime); e == nil {
		return &t
	}
	if t, e := http.ParseTime(resetTime); e == nil {
		return &t
	}
	return nil
}

// QuotaGroupClassifier mirrors the registry-first getQuotaGroupForModel hook.
type QuotaGroupClassifier func(modelName string) (QuotaGroup, bool)

// RegistryQuotaGroupClassifier can be set by callers with a model registry.
var RegistryQuotaGroupClassifier QuotaGroupClassifier

// ClassifyQuotaGroup classifies a model using registry, Claude substring, then Gemini 3 family fallback.
func ClassifyQuotaGroup(modelName, displayName string) *QuotaGroup {
	if RegistryQuotaGroupClassifier != nil {
		if g, ok := RegistryQuotaGroupClassifier(modelName); ok {
			return &g
		}
	}
	combined := strings.ToLower(modelName + " " + displayName)
	if strings.Contains(combined, "claude") {
		g := QuotaGroupClaude
		return &g
	}
	if !(strings.Contains(combined, "gemini-3") || strings.Contains(combined, "gemini 3")) {
		return nil
	}
	if GetModelFamily(modelName) == QuotaGroupGeminiFlash {
		g := QuotaGroupGeminiFlash
		return &g
	}
	g := QuotaGroupGeminiPro
	return &g
}

// GetModelFamily returns gemini-flash when the model name contains flash, otherwise gemini-pro.
func GetModelFamily(modelName string) QuotaGroup {
	if strings.Contains(strings.ToLower(modelName), "flash") {
		return QuotaGroupGeminiFlash
	}
	return QuotaGroupGeminiPro
}

// AggregateQuota aggregates per-model data using minimum remainingFraction and earliest resetTime per group.
func AggregateQuota(models map[string]FetchAvailableModelEntry) QuotaSummary {
	groups := map[QuotaGroup]QuotaGroupSummary{}
	perModel := make([]PerModelQuotaEntry, 0, len(models))
	if models == nil {
		return QuotaSummary{Groups: groups, PerModel: perModel}
	}
	total := 0
	for modelName, entry := range models {
		display := entry.DisplayName
		if display == "" {
			display = entry.ModelName
		}
		group := ClassifyQuotaGroup(modelName, display)
		var remaining *float64
		reset := ""
		var resetTS *time.Time
		if entry.QuotaInfo != nil {
			if entry.QuotaInfo.RemainingFraction != nil {
				v := NormalizeRemainingFraction(entry.QuotaInfo.RemainingFraction)
				remaining = &v
			}
			reset = entry.QuotaInfo.ResetTime
			resetTS = ParseResetTime(reset)
		}
		total++
		pm := 0.0
		if remaining != nil {
			pm = *remaining
		}
		perModel = append(perModel, PerModelQuotaEntry{ModelID: modelName, DisplayName: display, Group: group, RemainingFraction: pm, ResetTime: reset})
		if group == nil {
			continue
		}
		existing := groups[*group]
		nextCount := existing.ModelCount + 1
		nextRemaining := existing.RemainingFraction
		if remaining != nil {
			if nextRemaining == nil {
				v := *remaining
				nextRemaining = &v
			} else {
				v := math.Min(*nextRemaining, *remaining)
				nextRemaining = &v
			}
		}
		nextReset := existing.ResetTime
		if resetTS != nil {
			if existing.ResetTime == "" {
				nextReset = reset
			} else {
				existingTS := ParseResetTime(existing.ResetTime)
				if existingTS == nil || resetTS.Before(*existingTS) {
					nextReset = reset
				}
			}
		}
		groups[*group] = QuotaGroupSummary{RemainingFraction: nextRemaining, ResetTime: nextReset, ModelCount: nextCount}
	}
	sort.Slice(perModel, func(i, j int) bool { return perModel[i].ModelID < perModel[j].ModelID })
	return QuotaSummary{Groups: groups, PerModel: perModel, ModelCount: total}
}

// FetchAvailableModels fetches Antigravity model availability and quota data with fallback endpoints.
func FetchAvailableModels(ctx context.Context, client *http.Client, accessToken, projectID string, endpoints ...string) (FetchAvailableModelsResponse, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if len(endpoints) == 0 {
		endpoints = FetchAvailableModelsEndpointFallbacks
	}
	ua := GetRandomizedHeaders(HeaderStyleAntigravity, "").UserAgent
	errs := []string{}
	for _, endpoint := range endpoints {
		endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
		if endpoint == "" {
			continue
		}
		r, status, snip, err := fetchAvailableModelsOnce(ctx, client, endpoint, accessToken, projectID, ua)
		if err == nil && status >= 200 && status < 300 {
			return r, nil
		}
		if status == http.StatusForbidden && projectID != "" {
			rr, rs, _, re := fetchAvailableModelsOnce(ctx, client, endpoint, accessToken, "", ua)
			if re == nil && rs >= 200 && rs < 300 {
				return rr, nil
			}
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("fetchAvailableModels network error at %s: %v", endpoint, err))
			continue
		}
		if status == http.StatusTooManyRequests || status >= 500 {
			errs = append(errs, fmt.Sprintf("fetchAvailableModels %d at %s%s", status, endpoint, formatSnippet(snip)))
			continue
		}
		errs = append(errs, fmt.Sprintf("fetchAvailableModels %d at %s%s", status, endpoint, formatSnippet(snip)))
		break
	}
	if len(errs) == 0 {
		return FetchAvailableModelsResponse{}, fmt.Errorf("fetchAvailableModels failed")
	}
	return FetchAvailableModelsResponse{}, fmt.Errorf("%s", strings.Join(errs, "; "))
}
func fetchAvailableModels(ctx context.Context, client *http.Client, accessToken, projectID string, endpoints ...string) (FetchAvailableModelsResponse, error) {
	return FetchAvailableModels(ctx, client, accessToken, projectID, endpoints...)
}
func fetchAvailableModelsOnce(ctx context.Context, client *http.Client, endpoint, accessToken, projectID, userAgent string) (FetchAvailableModelsResponse, int, string, error) {
	cctx, cancel := context.WithTimeout(ctx, time.Duration(FetchTimeoutMS)*time.Millisecond)
	defer cancel()
	bodyMap := map[string]string{}
	if projectID != "" {
		bodyMap["project"] = projectID
	}
	body, _ := json.Marshal(bodyMap)
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, endpoint+"/v1internal:fetchAvailableModels", bytes.NewReader(body))
	if err != nil {
		return FetchAvailableModelsResponse{}, 0, "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := client.Do(req)
	if err != nil {
		return FetchAvailableModelsResponse{}, 0, "", err
	}
	defer resp.Body.Close()
	b, err := readQuotaResponseBody(resp)
	if err != nil {
		return FetchAvailableModelsResponse{}, resp.StatusCode, "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return FetchAvailableModelsResponse{}, resp.StatusCode, snippet(b), nil
	}
	var parsed FetchAvailableModelsResponse
	if err := json.Unmarshal(b, &parsed); err != nil {
		return FetchAvailableModelsResponse{}, resp.StatusCode, "", err
	}
	return parsed, resp.StatusCode, "", nil
}

// FetchGeminiCLIQuota fetches Gemini CLI quota buckets using the Gemini CLI user-agent.
func FetchGeminiCLIQuota(ctx context.Context, client *http.Client, accessToken, projectID string, endpoints ...string) RetrieveUserQuotaResponse {
	if client == nil {
		client = http.DefaultClient
	}
	if len(endpoints) == 0 {
		endpoints = FetchAvailableModelsEndpointFallbacks
	}
	ua := BuildGeminiCLIUserAgent("")
	for _, endpoint := range endpoints {
		endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
		if endpoint == "" {
			continue
		}
		r, status, err := fetchGeminiCLIQuotaOnce(ctx, client, endpoint, accessToken, projectID, ua)
		if err == nil && status >= 200 && status < 300 {
			return r
		}
		if err != nil || status == http.StatusTooManyRequests || status >= 500 {
			continue
		}
		return RetrieveUserQuotaResponse{Buckets: []RetrieveUserQuotaBucket{}}
	}
	return RetrieveUserQuotaResponse{Buckets: []RetrieveUserQuotaBucket{}}
}
func fetchGeminiCLIQuotaOnce(ctx context.Context, client *http.Client, endpoint, accessToken, projectID, userAgent string) (RetrieveUserQuotaResponse, int, error) {
	cctx, cancel := context.WithTimeout(ctx, time.Duration(FetchTimeoutMS)*time.Millisecond)
	defer cancel()
	bodyMap := map[string]string{}
	if projectID != "" {
		bodyMap["project"] = projectID
	}
	body, _ := json.Marshal(bodyMap)
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, endpoint+"/v1internal:retrieveUserQuota", bytes.NewReader(body))
	if err != nil {
		return RetrieveUserQuotaResponse{}, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return RetrieveUserQuotaResponse{}, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return RetrieveUserQuotaResponse{}, resp.StatusCode, nil
	}
	b, err := readQuotaResponseBody(resp)
	if err != nil {
		return RetrieveUserQuotaResponse{}, resp.StatusCode, err
	}
	var parsed RetrieveUserQuotaResponse
	if err := json.Unmarshal(b, &parsed); err != nil {
		return RetrieveUserQuotaResponse{}, resp.StatusCode, err
	}
	return parsed, resp.StatusCode, nil
}

// AggregateGeminiCLIQuota filters premium Gemini CLI quota models and sorts by model ID.
func AggregateGeminiCLIQuota(response RetrieveUserQuotaResponse) GeminiCLIQuotaSummary {
	models := []GeminiCLIQuotaModel{}
	for _, bucket := range response.Buckets {
		if bucket.ModelID == "" {
			continue
		}
		id := bucket.ModelID
		if !(strings.HasPrefix(id, "gemini-3-") || strings.HasPrefix(id, "gemini-3.") || strings.HasPrefix(id, "gemini-2.5-")) {
			continue
		}
		models = append(models, GeminiCLIQuotaModel{ModelID: id, RemainingFraction: NormalizeRemainingFraction(bucket.RemainingFraction), ResetTime: bucket.ResetTime})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ModelID < models[j].ModelID })
	return GeminiCLIQuotaSummary{Models: models}
}
func readQuotaResponseBody(resp *http.Response) ([]byte, error) {
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		r, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return io.ReadAll(r)
	}
	return io.ReadAll(resp.Body)
}
func snippet(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 200 {
		return text[:200]
	}
	return text
}
func formatSnippet(value string) string {
	if value == "" {
		return ""
	}
	return ": " + value
}
