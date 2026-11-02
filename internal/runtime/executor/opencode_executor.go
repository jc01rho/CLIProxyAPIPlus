package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	// openCodeDefaultBaseURL is the OpenCode Zen gateway base. OpenCode Go
	// (the lite subscription tier) lives at .../zen/go/v1 and is selected by
	// configuring base-url on the credential entry.
	openCodeDefaultBaseURL = "https://opencode.ai/zen/v1"
	openCodeChatEndpoint   = "/chat/completions"
	openCodeModelsEndpoint = "/models"
	// openCodeSystemOneEndpoint is the System One evaluation route. OpenCode
	// sends its jev-* evaluation models here (packages/ai SystemOne.model);
	// the gateway answers ModelProtocolUnsupported for them on /chat/completions.
	openCodeSystemOneEndpoint = "/systemone"

	// openCodeVersion is the client version mirrored in the User-Agent. The
	// free-tier gate only matches the "opencode/" prefix, but the proxy keeps
	// a real release version so the request stays indistinguishable.
	openCodeVersion = "2.0.27"
	// openCodeChannel mirrors OPENCODE_CHANNEL of release builds.
	openCodeChannel = "latest"
	// openCodeClient mirrors OPENCODE_CLIENT, which the CLI defaults to "cli".
	openCodeClient = "cli"
	// openCodeProject mirrors the project id the CLI reports for an instance
	// outside a repository ("global").
	openCodeProject = "global"
	// openCodePublicKey is the anonymous credential the CLI sends when no
	// OpenCode account is connected.
	openCodePublicKey = "public"

	// openCodeMaxErrorBody caps upstream error bodies surfaced to clients.
	openCodeMaxErrorBody = 1 << 20
)

// OpenCodeExecutor forwards OpenAI-compatible requests to the OpenCode Zen /
// OpenCode Go gateway.
//
// The gateway gates its free tier on the caller looking like the OpenCode
// client: the request must carry the x-opencode-* identity headers plus a
// User-Agent prefixed with "opencode/", and the tool list must contain the
// OpenCode "bash" and "read" tools. Requests that fail the check are rejected
// with `FreeTierError: OpenCode's free tier can only be used from within
// OpenCode`. The executor mirrors the CLI identity and tops up the tool list
// for free-tier models, so proxy traffic stays eligible.
type OpenCodeExecutor struct {
	provider string
	cfg      *config.Config
}

// NewOpenCodeExecutor creates a new OpenCode executor instance.
func NewOpenCodeExecutor(cfg *config.Config) *OpenCodeExecutor {
	return &OpenCodeExecutor{provider: "opencode", cfg: cfg}
}

// Identifier returns the provider key handled by this executor.
func (e *OpenCodeExecutor) Identifier() string { return e.provider }

// HttpRequest injects OpenCode credentials and executes the request.
func (e *OpenCodeExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("opencode executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	applyOpenCodeHeaders(httpReq, openCodeAPIKey(auth), true, openCodeAffinitySession(ctx))
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// Execute performs a non-streaming request against the OpenCode gateway.
func (e *OpenCodeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if opts.Alt == openAICompatSystemOneAlt {
		return e.executeSystemOne(ctx, auth, req)
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	baseModel = resolveOpenCodeModelName(e.cfg, auth, baseModel)

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), false)
	freeTier := isOpenCodeFreeModel(baseModel)
	translated = ensureOpenCodeFreeTools(translated, baseModel)
	// The free tier answers only over SSE: a request with stream:false is
	// rejected with the same free-tier gate error as a foreign client. Ask the
	// gateway to stream and fold the chunks back into one completion below.
	translated = forceOpenCodeFreeStream(translated, baseModel)

	url := openCodeChatURL(auth)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if err != nil {
		return resp, err
	}
	applyOpenCodeHeaders(httpReq, openCodeAPIKey(auth), false, openCodeAffinitySession(ctx, opts.Metadata, req.Metadata))
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
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      translated,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer httpResp.Body.Close()

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(httpResp.Body, openCodeMaxErrorBody))
		appendAPIResponseChunk(ctx, e.cfg, b)
		return resp, statusErr{code: httpResp.StatusCode, msg: string(b)}
	}

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	appendAPIResponseChunk(ctx, e.cfg, body)

	if freeTier {
		aggregatedBody, usageDetail, errAggregate := aggregateOpenAIChatCompletionStream(body)
		if errAggregate != nil {
			// A gateway that already answered with a buffered completion body
			// needs no folding; anything else is a real failure.
			if !gjson.ValidBytes(body) || !gjson.GetBytes(body, "choices").IsArray() {
				recordAPIResponseError(ctx, e.cfg, errAggregate)
				return resp, errAggregate
			}
			aggregatedBody = body
			usageDetail = parseOpenAIUsage(body)
		}
		reporter.publish(ctx, usageDetail)
		reporter.ensurePublished(ctx)
		var param any
		out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, aggregatedBody, &param)
		resp = cliproxyexecutor.Response{Payload: []byte(out), Headers: httpResp.Header.Clone()}
		return resp, nil
	}

	reporter.publish(ctx, parseOpenAIUsage(body))
	reporter.ensurePublished(ctx)

	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, body, &param)
	resp = cliproxyexecutor.Response{Payload: []byte(out)}
	return resp, nil
}

// ExecuteStream performs a streaming request against the OpenCode gateway.
func (e *OpenCodeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	baseModel = resolveOpenCodeModelName(e.cfg, auth, baseModel)

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), true)
	translated = ensureOpenCodeFreeTools(translated, baseModel)

	url := openCodeChatURL(auth)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if err != nil {
		return nil, err
	}
	applyOpenCodeHeaders(httpReq, openCodeAPIKey(auth), true, openCodeAffinitySession(ctx, opts.Metadata, req.Metadata))
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
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      translated,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(httpResp.Body, openCodeMaxErrorBody))
		appendAPIResponseChunk(ctx, e.cfg, b)
		httpResp.Body.Close()
		return nil, statusErr{code: httpResp.StatusCode, msg: string(b)}
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer httpResp.Body.Close()

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800)
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			appendAPIResponseChunk(ctx, e.cfg, line)
			if detail, ok := parseOpenAIStreamUsage(line); ok {
				reporter.publish(ctx, detail)
			}
			if len(line) == 0 {
				continue
			}
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, bytes.Clone(line), &param)
			for i := range chunks {
				if !sendStreamChunk(ctx, out, cliproxyexecutor.StreamChunk{Payload: []byte(chunks[i])}) {
					return
				}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			recordAPIResponseError(ctx, e.cfg, errScan)
			reporter.publishFailure(ctx)
			if !sendStreamChunk(ctx, out, cliproxyexecutor.StreamChunk{Err: errScan}) {
				return
			}
		}
		reporter.ensurePublished(ctx)
	}()

	return &cliproxyexecutor.StreamResult{
		Headers: httpResp.Header.Clone(),
		Chunks:  out,
	}, nil
}

// executeSystemOne forwards a native System One evaluation payload to
// POST <baseURL>/systemone, the route OpenCode uses for its jev-* models, and
// returns the upstream JSON verbatim. Only the model is rewritten to the
// configured upstream name.
func (e *OpenCodeExecutor) executeSystemOne(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	baseModel = resolveOpenCodeModelName(e.cfg, auth, baseModel)

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	payload := bytes.Clone(req.Payload)
	if gjson.GetBytes(payload, "model").Exists() {
		if next, errSet := sjson.SetBytes(payload, "model", baseModel); errSet == nil {
			payload = next
		}
	}

	url := openCodeBaseURL(auth) + openCodeSystemOneEndpoint
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return resp, err
	}
	applyOpenCodeHeaders(httpReq, openCodeAPIKey(auth), false, openCodeAffinitySession(ctx))
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
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      payload,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer httpResp.Body.Close()

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(httpResp.Body, openCodeMaxErrorBody))
		appendAPIResponseChunk(ctx, e.cfg, b)
		return resp, statusErr{code: httpResp.StatusCode, msg: string(b)}
	}

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	appendAPIResponseChunk(ctx, e.cfg, body)

	reporter.publish(ctx, parseOpenAIUsage(body))
	reporter.ensurePublished(ctx)
	return cliproxyexecutor.Response{Payload: body, Headers: httpResp.Header.Clone()}, nil
}

// Refresh returns the auth unchanged. OpenCode keys are long-lived; the Go
// tier subscription key has no refresh endpoint.
func (e *OpenCodeExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("missing auth")
	}
	return auth, nil
}

// CountTokens is not supported by the OpenCode gateway.
func (e *OpenCodeExecutor) CountTokens(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("opencode: count tokens not supported")
}

// openCodeAPIKey extracts the credential from the auth record. Entries without
// a key fall back to the anonymous credential the CLI uses, which is what makes
// the free tier reachable.
func openCodeAPIKey(auth *cliproxyauth.Auth) string {
	if auth != nil && auth.Attributes != nil {
		for _, keyName := range []string{"api_key", "apiKey", "key", "opencode", "access"} {
			if key := strings.TrimSpace(auth.Attributes[keyName]); key != "" {
				return key
			}
		}
	}
	return openCodePublicKey
}

// openCodeBaseURL resolves the gateway base for an auth entry, defaulting to
// the Zen endpoint. Point base-url at https://opencode.ai/zen/go/v1 to use the
// OpenCode Go (lite) tier.
func openCodeBaseURL(auth *cliproxyauth.Auth) string {
	if auth != nil && auth.Attributes != nil {
		if base := strings.TrimSpace(auth.Attributes["base_url"]); base != "" {
			return strings.TrimRight(base, "/")
		}
	}
	return openCodeDefaultBaseURL
}

func openCodeChatURL(auth *cliproxyauth.Auth) string {
	return openCodeBaseURL(auth) + openCodeChatEndpoint
}

// applyOpenCodeHeaders mirrors the headers the OpenCode client sends to its own
// gateway (packages/core/src/session/model-request.ts). The free-tier gate
// rejects requests that do not carry the identity headers, and the CLI always
// streams, so Accept follows the call shape.
//
// The session id must be a descending identifier, exactly like the CLI's
// SessionID.create(): the gate decodes it and treats an ascending (or
// otherwise malformed) id as a foreign client. The CLI sends the same session
// id on four headers.
//
// affinitySession is the conversation-stable id derived from the proxy's
// reconciled session identity (openCodeAffinitySession). It keeps the prompt
// cache warm across turns and holds upstream session affinity. Only when no
// conversation identity is available does the executor mint a fresh id.
func applyOpenCodeHeaders(req *http.Request, apiKey string, stream bool, affinitySession string) {
	session := strings.TrimSpace(affinitySession)
	if session == "" {
		session = newOpenCodeIdentifier("ses", true)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", openCodeUserAgent())
	req.Header.Set("x-opencode-client", openCodeClient)
	req.Header.Set("x-opencode-session", session)
	req.Header.Set("x-opencode-session-id", session)
	req.Header.Set("x-session-affinity", session)
	req.Header.Set("X-Session-Id", session)
	req.Header.Set("x-opencode-request", newOpenCodeIdentifier("msg", false))
	req.Header.Set("x-opencode-project", openCodeProject)
	if apiKey == "" {
		apiKey = openCodePublicKey
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
}

// openCodeUserAgent builds the UA the CLI sends (App.useragent:
// opencode/<channel>/<version>/<client>). OPENCODE_CLI_VERSION overrides the
// mirrored release version when the upstream gate moves on.
func openCodeUserAgent() string {
	version := openCodeVersion
	if override := strings.TrimSpace(os.Getenv("OPENCODE_CLI_VERSION")); override != "" {
		version = override
	}
	return "opencode/" + openCodeChannel + "/" + version + "/" + openCodeClient
}

// resolveOpenCodeModelName maps a configured alias to its upstream model name.
func resolveOpenCodeModelName(cfg *config.Config, auth *cliproxyauth.Auth, model string) string {
	if cfg == nil || auth == nil {
		return model
	}
	apiKey := strings.TrimSpace(auth.Attributes["api_key"])
	for i := range cfg.OpenCodeKey {
		key := cfg.OpenCodeKey[i]
		if apiKey != "" && !key.MatchesCredential(apiKey, auth.ProxyURL) {
			continue
		}
		for _, m := range key.Models {
			if strings.TrimSpace(m.Alias) == model && strings.TrimSpace(m.Name) != "" {
				return strings.TrimSpace(m.Name)
			}
		}
	}
	return model
}

// openCodeFreeModels are the OpenCode gateway models served through the free
// tier. They are the only models that require the OpenCode tool envelope.
var openCodeFreeModels = map[string]struct{}{
	"big-pickle": {},
}

// isOpenCodeFreeModel reports whether the upstream model is served by the free
// tier. Free models are suffixed "-free"; "big-pickle" is the one free model
// without the suffix.
func isOpenCodeFreeModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return false
	}
	if _, ok := openCodeFreeModels[model]; ok {
		return true
	}
	return strings.HasSuffix(model, "-free")
}

// openCodeFreeTierBashTool and openCodeFreeTierReadTool are the minimal tool
// definitions injected when a free-tier request does not already carry the
// OpenCode tool envelope. Only the names are inspected by the gate; the schemas
// mirror the CLI tools so a model that decides to call them still gets a usable
// contract.
const (
	openCodeFreeTierBashTool = `{"type":"function","function":{"name":"bash","description":"Executes a given bash command in a persistent shell session with optional timeout, ensuring proper handling and security measures.","parameters":{"type":"object","properties":{"command":{"type":"string","description":"The command to execute"},"workdir":{"type":"string","description":"The working directory to run the command in"}},"required":["command"],"additionalProperties":false}}}`
	openCodeFreeTierReadTool = `{"type":"function","function":{"name":"read","description":"Read a file or directory from the local filesystem. If the path does not exist, an error is returned.","parameters":{"type":"object","properties":{"filePath":{"type":"string","description":"The absolute path to the file or directory to read"},"offset":{"type":"number","description":"The line number to start reading from (1-indexed)"},"limit":{"type":"number","description":"The maximum number of lines to read"}},"required":["filePath"],"additionalProperties":false}}}`
)

// ensureOpenCodeFreeTools guarantees the OpenCode tool envelope on free-tier
// requests. The gateway answers `FreeTierError: OpenCode's free tier can only be
// used from within OpenCode` unless the request declares both the "bash" and
// "read" tools, so the proxy tops up the tool list when a client (for example a
// plain chat UI) does not send them. Non-free models and payloads that already
// carry both tools are returned untouched.
func ensureOpenCodeFreeTools(payload []byte, model string) []byte {
	if !isOpenCodeFreeModel(model) || len(payload) == 0 {
		return payload
	}
	hasBash, hasRead := false, false
	tools := gjson.GetBytes(payload, "tools")
	if tools.IsArray() {
		tools.ForEach(func(_, value gjson.Result) bool {
			switch value.Get("function.name").String() {
			case "bash":
				hasBash = true
			case "read":
				hasRead = true
			}
			return !(hasBash && hasRead)
		})
	}
	if hasBash && hasRead {
		return payload
	}
	out := payload
	index := int(tools.Get("#").Int())
	if !tools.IsArray() {
		if _, err := sjson.SetRawBytes(out, "tools", []byte("[]")); err != nil {
			return payload
		}
		index = 0
	}
	if !hasBash {
		next, err := sjson.SetRawBytes(out, fmt.Sprintf("tools.%d", index), []byte(openCodeFreeTierBashTool))
		if err != nil {
			return payload
		}
		out = next
		index++
	}
	if !hasRead {
		next, err := sjson.SetRawBytes(out, fmt.Sprintf("tools.%d", index), []byte(openCodeFreeTierReadTool))
		if err != nil {
			return payload
		}
		out = next
	}
	return out
}

// forceOpenCodeFreeStream sets stream:true for free-tier models. The gateway
// serves the free tier exclusively over SSE, so a buffered request is rejected
// before it reaches a model. The caller folds the stream back into one body.
func forceOpenCodeFreeStream(payload []byte, model string) []byte {
	if !isOpenCodeFreeModel(model) || len(payload) == 0 {
		return payload
	}
	if gjson.GetBytes(payload, "stream").Bool() {
		return payload
	}
	next, err := sjson.SetBytes(payload, "stream", true)
	if err != nil {
		return payload
	}
	return next
}

// openCodeIDTailAlphabet is the base62 alphabet OpenCode appends after the
// timestamp segment (see @opencode-ai/schema/identifier create()).
const openCodeIDTailAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// openCodeIDCounter mirrors the monotonic counter OpenCode packs into the low
// 12 bits of the identifier timestamp segment.
var openCodeIDCounter struct {
	sync.Mutex
	lastMs  int64
	counter uint64
}

// newOpenCodeIdentifier returns an identifier in the exact shape OpenCode
// emits: <prefix>_<12 lowercase hex characters><14 base62 characters>. The
// gateway parses this segment, and requests that carry a malformed session or
// request id are treated as foreign clients and rejected with the free-tier
// error, so the shape is part of the wire contract. Session ids are
// descending (the timestamp segment is bit-inverted), message ids ascending.
func newOpenCodeIdentifier(prefix string, descending bool) string {
	ms := time.Now().UnixMilli()
	openCodeIDCounter.Lock()
	if ms != openCodeIDCounter.lastMs {
		openCodeIDCounter.lastMs = ms
		openCodeIDCounter.counter = 0
	}
	openCodeIDCounter.counter = (openCodeIDCounter.counter + 1) & 0xfff
	counter := openCodeIDCounter.counter
	openCodeIDCounter.Unlock()

	// 48 bit timestamp segment: (ms << 12 | counter) truncated to 48 bits,
	// rendered as 12 lowercase hex characters — the same packing (and the same
	// truncation) the schema identifier applies.
	current := (uint64(ms) << 12) | counter
	if descending {
		current = ^current
	}
	current &= 0xffffffffffff
	var stamp [12]byte
	const hexDigits = "0123456789abcdef"
	for i := 5; i >= 0; i-- {
		b := byte(current >> (8 * (5 - i)))
		stamp[i*2] = hexDigits[b>>4]
		stamp[i*2+1] = hexDigits[b&0x0f]
	}

	tail := make([]byte, 14)
	randomBytes := make([]byte, 14)
	if _, err := rand.Read(randomBytes); err != nil {
		for i := range randomBytes {
			randomBytes[i] = byte(time.Now().UnixNano() >> (i % 8))
		}
	}
	for i := range tail {
		tail[i] = openCodeIDTailAlphabet[int(randomBytes[i])%len(openCodeIDTailAlphabet)]
	}

	return prefix + "_" + string(stamp[:]) + string(tail)
}
