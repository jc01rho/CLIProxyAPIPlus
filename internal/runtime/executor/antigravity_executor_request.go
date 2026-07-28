package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	internalantigravity "github.com/router-for-me/CLIProxyAPI/v7/internal/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	antigravityNowMs   = func() int64 { return time.Now().UnixMilli() }
	antigravityNewUUID = uuid.NewString
)

type antigravityConversationKeyContextKey struct{}

func (e *AntigravityExecutor) buildRequest(ctx context.Context, auth *cliproxyauth.Auth, token, modelName string, payload []byte, stream bool, alt, baseURL string, derivedSessionIDs ...string) (*http.Request, error) {
	if token == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing access token"}
	}

	base := strings.TrimSuffix(baseURL, "/")
	if base == "" {
		base = buildBaseURL(auth)
	}
	path := antigravityGeneratePath
	if stream {
		path = antigravityStreamPath
	}
	var requestURL strings.Builder
	requestURL.WriteString(base)
	requestURL.WriteString(path)
	if stream {
		if alt != "" {
			requestURL.WriteString("?$alt=")
			requestURL.WriteString(url.QueryEscape(alt))
		} else {
			requestURL.WriteString("?alt=sse")
		}
	} else if alt != "" {
		requestURL.WriteString("?$alt=")
		requestURL.WriteString(url.QueryEscape(alt))
	}

	projectID, errProject := e.projectIDForRequest(ctx, auth, token)
	if errProject != nil {
		return nil, errProject
	}
	var conversationKey string
	payload, conversationKey = geminiToAntigravity(modelName, payload, projectID, true, derivedSessionIDs...)
	payload, errProject = antigravityApplyPackagePayloadTransforms(modelName, payload)
	if errProject != nil {
		return nil, errProject
	}
	payload = antigravityNormalizeFunctionDeclarationsForExecutor(payload)

	// Cap maxOutputTokens to model's max_completion_tokens from registry
	if maxOut := gjson.GetBytes(payload, "request.generationConfig.maxOutputTokens"); maxOut.Exists() && maxOut.Type == gjson.Number {
		if modelInfo := registry.LookupModelInfo(modelName, "antigravity"); modelInfo != nil && modelInfo.MaxCompletionTokens > 0 {
			if int(maxOut.Int()) > modelInfo.MaxCompletionTokens {
				payload, _ = sjson.SetBytes(payload, "request.generationConfig.maxOutputTokens", modelInfo.MaxCompletionTokens)
			}
		}
	}

	useAntigravitySchema := strings.Contains(modelName, "claude") || strings.Contains(modelName, "gemini-3-pro") || strings.Contains(modelName, "gemini-3.1-pro")
	var requestBody []byte
	if antigravityRequestNeedsSchemaSanitization(payload) {
		payloadStr := sanitizeAntigravityRequestSchemas(string(payload), useAntigravitySchema)

		if strings.Contains(modelName, "claude") {
			updated, _ := sjson.SetBytes([]byte(payloadStr), "request.toolConfig.functionCallingConfig.mode", "VALIDATED")
			payloadStr = string(updated)
		} else {
			payloadStr, _ = sjson.Delete(payloadStr, "request.generationConfig.maxOutputTokens")
		}

		requestBody = applyAntigravityNativeSignatureReplayIfNeeded(modelName, []byte(payloadStr))
	} else {
		if strings.Contains(modelName, "claude") {
			payload, _ = sjson.SetBytes(payload, "request.toolConfig.functionCallingConfig.mode", "VALIDATED")
		} else {
			payload, _ = sjson.DeleteBytes(payload, "request.generationConfig.maxOutputTokens")
		}

		requestBody = applyAntigravityNativeSignatureReplayIfNeeded(modelName, payload)
	}
	requestBody = []byte(internalantigravity.SignRequestBody(string(requestBody)))
	var payloadLog []byte
	if e.cfg != nil && e.cfg.RequestLog {
		payloadLog = append([]byte(nil), requestBody...)
	}
	bodyReader := bytes.NewReader(requestBody)

	// if useAntigravitySchema {
	// 	systemInstructionPartsResult := gjson.Get(payloadStr, "request.systemInstruction.parts")
	// 	payloadStr, _ = sjson.SetBytes([]byte(payloadStr), "request.systemInstruction.role", "user")
	// 	payloadStr, _ = sjson.SetBytes([]byte(payloadStr), "request.systemInstruction.parts.0.text", systemInstruction)
	// 	payloadStr, _ = sjson.SetBytes([]byte(payloadStr), "request.systemInstruction.parts.1.text", fmt.Sprintf("Please ignore following [ignore]%s[/ignore]", systemInstruction))

	// 	if systemInstructionPartsResult.Exists() && systemInstructionPartsResult.IsArray() {
	// 		for _, partResult := range systemInstructionPartsResult.Array() {
	// 			payloadStr, _ = sjson.SetRawBytes([]byte(payloadStr), "request.systemInstruction.parts.-1", []byte(partResult.Raw))
	// 		}
	// 	}
	// }

	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), bodyReader)
	if errReq != nil {
		return nil, errReq
	}
	httpReq.Close = true
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("User-Agent", resolveUserAgent(auth))
	if host := resolveHost(base); host != "" {
		httpReq.Host = host
	}
	if conversationKey != "" {
		httpReq = httpReq.WithContext(context.WithValue(httpReq.Context(), antigravityConversationKeyContextKey{}, conversationKey))
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       requestURL.String(),
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      payloadLog,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	return httpReq, nil
}

func antigravityTrackerAccountIndex(auth *cliproxyauth.Auth) int {
	if auth == nil {
		return 0
	}
	seed := auth.EnsureIndex()
	if seed == "" {
		seed = strings.TrimSpace(auth.ID)
	}
	if seed == "" {
		return 0
	}
	if len(seed) >= 8 {
		if parsed, err := strconv.ParseUint(seed[:8], 16, 32); err == nil {
			return int(parsed % 100000)
		}
	}
	sum := sha256.Sum256([]byte(seed))
	return int(binary.BigEndian.Uint32(sum[:4]) % 100000)
}

func antigravityEstimateRequestTokenCost(payload []byte) float64 {
	chars := 0
	for _, content := range gjson.GetBytes(payload, "request.contents").Array() {
		for _, part := range content.Get("parts").Array() {
			chars += len(part.Get("text").String())
		}
	}
	if chars == 0 {
		return 1
	}
	cost := chars / 4
	if cost < 1 {
		return 1
	}
	return float64(cost)
}

func antigravityEnsureRequestTokens(auth *cliproxyauth.Auth, payload []byte) error {
	cost := antigravityEstimateRequestTokenCost(payload)
	if !internalantigravity.GetTokenTracker().HasTokens(antigravityTrackerAccountIndex(auth), cost) {
		return statusErr{code: http.StatusTooManyRequests, msg: "antigravity local token bucket exhausted"}
	}
	return nil
}

func antigravityConsumeRequestTokens(auth *cliproxyauth.Auth, payload []byte) {
	internalantigravity.GetTokenTracker().Consume(antigravityTrackerAccountIndex(auth), antigravityEstimateRequestTokenCost(payload))
}

func antigravityRecordRequestOutcome(auth *cliproxyauth.Auth, statusCode int, err error) {
	tracker := internalantigravity.GetHealthTracker()
	accountIndex := antigravityTrackerAccountIndex(auth)
	if err == nil && statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices {
		tracker.RecordSuccess(accountIndex)
		return
	}
	if statusCode == http.StatusTooManyRequests {
		tracker.RecordRateLimit(accountIndex)
		return
	}
	if err != nil || statusCode != 0 {
		tracker.RecordFailure(accountIndex)
	}
}

func antigravityAttemptSessionRecovery(auth *cliproxyauth.Auth, body []byte) {
	if len(body) == 0 {
		return
	}
	var errorValue any
	if err := json.Unmarshal(body, &errorValue); err != nil {
		errorValue = string(body)
	}
	if !internalantigravity.IsRecoverableError(errorValue) || auth == nil {
		return
	}
	refresh := metaStringValue(auth.Metadata, "refresh_token")
	internalantigravity.InvalidateProjectContextCache(refresh)
	if auth.Metadata == nil {
		return
	}
	delete(auth.Metadata, "project_id")
	parts := internalantigravity.ParseRefreshParts(refresh)
	if parts.ManagedProjectID != "" {
		parts.ManagedProjectID = ""
		auth.Metadata["refresh_token"] = internalantigravity.FormatRefreshParts(parts)
	}
}

func antigravityNormalizeFunctionDeclarationsForExecutor(payload []byte) []byte {
	tools := gjson.GetBytes(payload, "request.tools")
	if !tools.Exists() || !tools.IsArray() {
		return payload
	}
	normalized := make([]any, 0, len(tools.Array()))
	for _, tool := range tools.Array() {
		var toolMap map[string]any
		if err := json.Unmarshal([]byte(tool.Raw), &toolMap); err != nil {
			continue
		}
		if decls, ok := toolMap["functionDeclarations"].([]any); ok && len(decls) > 0 {
			normalized = append(normalized, map[string]any{"function_declarations": decls})
			continue
		}
		if _, ok := toolMap["name"]; ok {
			normalized = append(normalized, map[string]any{"function_declarations": []any{toolMap}})
			continue
		}
		normalized = append(normalized, toolMap)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return payload
	}
	updated, err := sjson.SetRawBytes(payload, "request.tools", encoded)
	if err != nil {
		return payload
	}
	return updated
}

func antigravityApplyPackagePayloadTransforms(modelName string, payload []byte) ([]byte, error) {
	if result, err := internalantigravity.SanitizeCrossModelPayload(payload, internalantigravity.SanitizerOptions{TargetModel: modelName}); err != nil {
		return payload, err
	} else if result.Modified {
		payload = result.Payload
	}
	request := gjson.GetBytes(payload, "request")
	if !request.IsObject() {
		return payload, nil
	}
	requestPayload := []byte(request.Raw)
	if result, err := internalantigravity.SanitizeCrossModelPayload(requestPayload, internalantigravity.SanitizerOptions{TargetModel: modelName}); err != nil {
		return payload, err
	} else if result.Modified {
		requestPayload = result.Payload
	}
	switch internalantigravity.GetTransformModelFamily(modelName) {
	case internalantigravity.ModelFamilyClaude:
		result, err := internalantigravity.ApplyClaudeTransforms(requestPayload, internalantigravity.ClaudeTransformOptions{Model: modelName})
		if err != nil {
			return payload, err
		}
		requestPayload = result.Payload
	case internalantigravity.ModelFamilyGeminiPro, internalantigravity.ModelFamilyGeminiFlash:
		if !strings.Contains(request.Raw, "parametersJsonSchema") {
			result, err := internalantigravity.ApplyGeminiTransforms(requestPayload, internalantigravity.GeminiTransformOptions{Model: modelName})
			if err != nil {
				return payload, err
			}
			requestPayload = result.Payload
		}
	}
	return sjson.SetRawBytes(payload, "request", requestPayload)
}

func (e *AntigravityExecutor) doRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("antigravity executor: request is nil")
	}
	if !strings.EqualFold(req.URL.Scheme, "https") {
		return newAntigravityHTTPClient(ctx, e.cfg, auth, 0).Do(req)
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	headers := make(map[string]string, len(req.Header))
	for key, values := range req.Header {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}
	return internalantigravity.FetchWithAgyCLITransport(ctx, req.URL.String(), internalantigravity.AgyRequestInit{
		Method:  req.Method,
		Headers: headers,
		Body:    body,
	}, internalantigravity.AgyTransportOptions{})
}

// sanitizeAntigravityRequestSchemas cleans the JSON schemas carried by an Antigravity request.
//
// Cleaning is applied only to the payload locations that actually hold a JSON schema. The schema
// cleaner rewrites keys such as "title", "format", "default" and "const", which are also ordinary
// data keys inside functionCall arguments replayed from conversation history. Running it over the
// whole document silently mutated that history, so tools lost required argument fields and the
// model imitated the corrupted examples on later turns.
func sanitizeAntigravityRequestSchemas(payloadStr string, useAntigravitySchema bool) string {
	for _, base := range antigravityFunctionDeclarationPaths(payloadStr) {
		oldPath := base + ".parametersJsonSchema"
		if !gjson.Get(payloadStr, oldPath).Exists() {
			continue
		}
		renamed, errRename := util.RenameKey(payloadStr, oldPath, base+".parameters")
		if errRename != nil {
			log.Debugf("antigravity: failed to rename %s: %v", oldPath, errRename)
			continue
		}
		payloadStr = renamed
	}

	clean := util.CleanJSONSchemaForGemini
	if useAntigravitySchema {
		clean = util.CleanJSONSchemaForAntigravity
	}

	for _, schemaPath := range antigravitySchemaPaths(payloadStr) {
		schema := gjson.Get(payloadStr, schemaPath)
		if !schema.Exists() {
			continue
		}
		updated, errSet := sjson.SetRawBytes([]byte(payloadStr), schemaPath, []byte(cleanNestedSchema(clean, schema.Raw)))
		if errSet != nil {
			log.Debugf("antigravity: failed to write cleaned schema at %s: %v", schemaPath, errSet)
			continue
		}
		payloadStr = string(updated)
	}

	return payloadStr
}

// antigravitySchemaWrapperKey nests a schema during cleaning. It is never sent upstream.
const antigravitySchemaWrapperKey = "schema"

// cleanNestedSchema cleans a schema with it nested one level down, then unwraps it.
//
// The cleaner deliberately skips placeholder insertion for a top-level schema, but Claude's
// VALIDATED mode needs every tool schema to declare at least one required property. Whole-payload
// cleaning always saw tool schemas nested inside the request, so nesting is reproduced here to keep
// the emitted schema byte-identical to the previous behaviour.
func cleanNestedSchema(clean func(string) string, schemaRaw string) string {
	wrapped, errWrap := sjson.SetRaw("{}", antigravitySchemaWrapperKey, schemaRaw)
	if errWrap != nil {
		return clean(schemaRaw)
	}
	if unwrapped := gjson.Get(clean(wrapped), antigravitySchemaWrapperKey); unwrapped.Exists() {
		return unwrapped.Raw
	}
	return clean(schemaRaw)
}

// antigravityFunctionDeclarationPaths returns the path of every function declaration in the request.
// Both the camelCase and snake_case spellings are accepted because callers reach this executor
// through different translators.
func antigravityFunctionDeclarationPaths(payloadStr string) []string {
	tools := gjson.Get(payloadStr, "request.tools")
	if !tools.IsArray() {
		return nil
	}
	paths := make([]string, 0, len(tools.Array()))
	for i, tool := range tools.Array() {
		for _, declKey := range []string{"functionDeclarations", "function_declarations"} {
			decls := tool.Get(declKey)
			if !decls.IsArray() {
				continue
			}
			for j := range decls.Array() {
				paths = append(paths, fmt.Sprintf("request.tools.%d.%s.%d", i, declKey, j))
			}
		}
	}
	return paths
}

// antigravitySchemaPaths returns every payload path that holds a JSON schema document.
// A function declaration may carry a schema for its parameters and for its result, so all of
// them must be cleaned; anything omitted here reaches the upstream API uncleaned.
func antigravitySchemaPaths(payloadStr string) []string {
	paths := make([]string, 0, 12)
	for _, base := range antigravityFunctionDeclarationPaths(payloadStr) {
		for _, key := range antigravityDeclarationSchemaKeys {
			if gjson.Get(payloadStr, base+"."+key).IsObject() {
				paths = append(paths, base+"."+key)
			}
		}
	}
	for _, container := range antigravityGenerationConfigContainers {
		for _, key := range antigravityGenerationSchemaKeys {
			p := container + "." + key
			if gjson.Get(payloadStr, p).IsObject() {
				paths = append(paths, p)
			}
		}
	}
	return paths
}

// The upstream API is proto-JSON and accepts either spelling, and the Gemini translator forwards
// whichever one the client sent. Both are therefore cleaned where they sit rather than renamed:
// renaming would alter the body the client asked for, and only the unsupported keywords inside a
// schema cause upstream errors. The one exception is parametersJsonSchema, renamed onto parameters
// above because whole-payload cleaning did the same.
var (
	antigravityDeclarationSchemaKeys = []string{
		"parameters", "parametersJsonSchema", "parameters_json_schema",
		"response", "responseJsonSchema", "response_json_schema",
	}
	antigravityGenerationConfigContainers = []string{
		"request.generationConfig", "request.generation_config",
	}
	antigravityGenerationSchemaKeys = []string{
		"responseSchema", "responseJsonSchema", "response_schema", "response_json_schema",
	}
)

func antigravityRequestNeedsSchemaSanitization(payload []byte) bool {
	if gjson.GetBytes(payload, "request.tools.0").Exists() {
		return true
	}
	for _, container := range antigravityGenerationConfigContainers {
		for _, key := range antigravityGenerationSchemaKeys {
			if gjson.GetBytes(payload, container+"."+key).Exists() {
				return true
			}
		}
	}
	return false
}
func buildBaseURL(auth *cliproxyauth.Auth) string {
	if baseURLs := antigravityBaseURLFallbackOrder(auth); len(baseURLs) > 0 {
		return baseURLs[0]
	}
	return antigravityBaseURLDaily
}

func antigravityLoadCodeAssistBaseURL(auth *cliproxyauth.Auth) string {
	if base := resolveCustomAntigravityBaseURL(auth); base != "" {
		return base
	}
	return antigravityBaseURLProd
}

func resolveHost(base string) string {
	parsed, errParse := url.Parse(base)
	if errParse != nil {
		return ""
	}
	if parsed.Host != "" {
		return parsed.Host
	}
	return strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
}

func resolveUserAgent(auth *cliproxyauth.Auth) string {
	configured := antigravityConfiguredUserAgent(auth)
	if configured != "" {
		return misc.AntigravityRequestUserAgent(configured)
	}
	return misc.AntigravityRequestUserAgent("")
}

func resolveLoadCodeAssistUserAgent(auth *cliproxyauth.Auth) string {
	return misc.AntigravityLoadCodeAssistUserAgent(antigravityConfiguredUserAgent(auth))
}

func antigravityConfiguredUserAgent(auth *cliproxyauth.Auth) string {
	raw := ""
	if auth != nil {
		if auth.Attributes != nil {
			if ua := strings.TrimSpace(auth.Attributes["user_agent"]); ua != "" {
				raw = ua
			}
		}
		if raw == "" && auth.Metadata != nil {
			if ua, ok := auth.Metadata["user_agent"].(string); ok && strings.TrimSpace(ua) != "" {
				raw = strings.TrimSpace(ua)
			}
		}
	}
	return raw
}

var antigravityBaseURLFallbackOrder = func(auth *cliproxyauth.Auth) []string {
	if base := resolveCustomAntigravityBaseURL(auth); base != "" {
		return []string{base}
	}
	return []string{
		antigravityBaseURLDaily,
		antigravityBaseURLProd,
		// antigravitySandboxBaseURLDaily,
	}
}

func resolveCustomAntigravityBaseURL(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["base_url"]); v != "" {
			return strings.TrimSuffix(v, "/")
		}
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["base_url"].(string); ok {
			v = strings.TrimSpace(v)
			if v != "" {
				return strings.TrimSuffix(v, "/")
			}
		}
	}
	return ""
}

func geminiToAntigravity(modelName string, payload []byte, projectID string, useAgyCLIMetadata bool, derivedSessionIDs ...string) ([]byte, string) {
	template := payload
	template = helps.SetStringIfDifferent(template, "model", modelName)
	template = helps.SetStringIfDifferent(template, "userAgent", "antigravity")

	isImageModel := strings.Contains(modelName, "image")
	conversationKey := ""
	reqType := strings.TrimSpace(gjson.GetBytes(template, "requestType").String())
	if reqType == "" {
		if isImageModel {
			reqType = "image_gen"
		} else {
			reqType = "agent"
		}
		template, _ = sjson.SetBytes(template, "requestType", reqType)
	}

	if projectID != "" {
		template = helps.SetStringIfDifferent(template, "project", projectID)
	} else {
		template, _ = sjson.DeleteBytes(template, "project")
	}

	if isImageModel {
		template, _ = sjson.SetBytes(template, "requestId", generateImageGenRequestID())
	} else if reqType == "agent" {
		if len(derivedSessionIDs) > 0 {
			conversationKey = strings.TrimSpace(derivedSessionIDs[0])
		}
		if conversationKey == "" {
			conversationKey = generateStableSessionID(template)
		}
		if useAgyCLIMetadata {
			session, timestamp := internalantigravity.BeginAgyRequest(
				conversationKey,
				internalantigravity.Fnv1a64Signed(projectID),
				antigravityNowMs(),
				antigravityNewUUID,
			)
			template = internalantigravity.ApplyAgyAgentWireMetadata(template, session, modelName, timestamp)
		}
	}

	template, _ = sjson.DeleteBytes(template, "request.safetySettings")
	if toolConfig := gjson.GetBytes(template, "toolConfig"); toolConfig.Exists() && !gjson.GetBytes(template, "request.toolConfig").Exists() {
		template, _ = sjson.SetRawBytes(template, "request.toolConfig", []byte(toolConfig.Raw))
		template, _ = sjson.DeleteBytes(template, "toolConfig")
	}
	if reqType == "agent" && !isImageModel && useAgyCLIMetadata {
		if request := gjson.GetBytes(template, "request"); request.Exists() {
			template, _ = sjson.SetRawBytes(template, "request", internalantigravity.OrderAgyRequestPayload([]byte(request.Raw)))
		}
		template = internalantigravity.OrderAgyEnvelope(template)
	}
	return template, conversationKey
}

func generateImageGenRequestID() string {
	return fmt.Sprintf("image_gen/%d/%s/12", antigravityNowMs(), antigravityNewUUID())
}

func antigravityConversationKey(req *http.Request) string {
	if req == nil {
		return ""
	}
	key, _ := req.Context().Value(antigravityConversationKeyContextKey{}).(string)
	return key
}

func generateStableSessionID(payload []byte) string {
	contents := gjson.GetBytes(payload, "request.contents")
	if contents.IsArray() {
		for _, content := range contents.Array() {
			if content.Get("role").String() == "user" {
				text := content.Get("parts.0.text").String()
				if text != "" {
					return internalantigravity.Fnv1a64Signed(text)
				}
			}
		}
	}
	return internalantigravity.Fnv1a64Signed("")
}
