package executor

import (
	"context"
	ed25519 "crypto/ed25519"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/zcode"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

// ZCodeAnthropicBaseURL is the direct Anthropic-compatible base for the zcode
// provider, used as the unsigned fallback (and historically the only path).
const ZCodeAnthropicBaseURL = "https://api.z.ai/api/anthropic"

// ZCodeUltraBaseURL is the ultra gateway that the desktop client routes
// signed traffic to (3.11.2 agent-configs proxyEndpoint.mapping:
// https://api.z.ai/api/anthropic/v1/messages ->
// https://zcode.z.ai/api/v1/ultra-zai/anthropic/v1/messages). The extension
// reference (omo-zcode-oauth) pins this as the default inference base for
// signed traffic. Set ZCODE_ANTHROPIC_BASE_URL to revert to the direct
// endpoint.
const ZCodeUltraBaseURL = "https://zcode.z.ai/api/v1/ultra-zai/anthropic"

// ZCodeStartPlanBaseURL is the ZCode start-plan gateway, verified from the
// ZCode desktop app (app.asar buildZCodeEndpointUrls): the start/coding-plan
// billing routes live on the zcode.z.ai origin under /api/v1/zcode-plan. When
// the account has an active start plan, requests are routed through this
// gateway with the broker JWT so the start plan quota is consumed instead of
// the individual plan that the provisioned Z.AI API key bills to. The
// zcode-plan model paths are exempt from the desktop client's Ed25519 signing
// (isUnsignedModelRequestPath), so a plain Bearer JWT works.
const ZCodeStartPlanBaseURL = "https://zcode.z.ai/api/v1/zcode-plan/anthropic"

// zcodeDefaultAppVersion mirrors the ZCode desktop release used for source
// headers and the balance probe. Keep it aligned with a real published release
// so the gateway treats the client as a current ZCode build (3.11.2 linux-x64).
// ZCODE_APP_VERSION overrides it, matching the extension's zcodeAppVersion().
const zcodeDefaultAppVersion = "3.11.2"

// zcodeDefaultReleaseChannel is the release channel used in the ZCode source
// headers. ZCODE_RELEASE_CHANNEL overrides it.
const zcodeDefaultReleaseChannel = "production"

// zcodeProviderUtilsVersion is the @ai-sdk/provider-utils version the ZCode
// desktop bundle appends to its User-Agent (3.11.2 glm/zcode.cjs xm()).
const zcodeProviderUtilsVersion = "4.0.27"

// zcodePrintableASCII drops every non printable-ASCII byte, mirroring the
// desktop and extension header normalizer (zcode.cjs tL()).
func zcodePrintableASCII(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= 0x20 && r <= 0x7e {
			return r
		}
		return -1
	}, value)
}

// zcodeEnv reads an environment variable as a printable-ASCII value.
func zcodeEnv(name string) string {
	return zcodePrintableASCII(strings.TrimSpace(os.Getenv(name)))
}

// zcodeAppVersion returns the advertised ZCode client version.
func zcodeAppVersion() string {
	if v := zcodeEnv("ZCODE_APP_VERSION"); v != "" {
		return v
	}
	return zcodeDefaultAppVersion
}

// zcodeReleaseChannel returns the advertised ZCode release channel.
func zcodeReleaseChannel() string {
	if v := zcodeEnv("ZCODE_RELEASE_CHANNEL"); v != "" {
		return v
	}
	return zcodeDefaultReleaseChannel
}

// zcodeUserAgent builds the User-Agent the desktop sends: the ZCode app id
// followed by the AI SDK layers the bundle appends. The proxy has no Node
// runtime, so the runtime tag is either injected via ZCODE_RUNTIME_NODE_VERSION
// or reported as "unknown" — the same fallback the desktop uses for a missing
// version field.
func zcodeUserAgent() string {
	node := zcodeEnv("ZCODE_RUNTIME_NODE_VERSION")
	if node == "" {
		node = "unknown"
	}
	return "ZCode/" + zcodeAppVersion() + " ai-sdk/provider-utils/" + zcodeProviderUtilsVersion + " runtime/node.js/" + node
}

// ZCodeSigningGateURL is the agent-configs endpoint that reports the client
// signing feature gate (data.codingPlanSignature.enable).
const ZCodeSigningGateURL = "https://zcode.z.ai/api/v1/agent/configs"

// zcodeGateTTL caches an affirmative gate answer for one hour, mirroring the
// desktop's tTn cache TTL. Negative results are never cached (fail-open).
const zcodeGateTTL = time.Hour

// ZcodeExecutor is a stateless executor for the GLM ZCode provider. It reuses
// the Anthropic-compatible ClaudeExecutor request/stream path but pins the
// base URL to api.z.ai and injects the ZCode source headers so api.z.ai sees
// the request as the ZCode client.
type ZcodeExecutor struct {
	*ClaudeExecutor
}

// NewZcodeExecutor creates a zcode executor.
func NewZcodeExecutor(cfg *config.Config) *ZcodeExecutor {
	return &ZcodeExecutor{ClaudeExecutor: NewClaudeExecutor(cfg)}
}

// Identifier returns the executor identifier.
func (e *ZcodeExecutor) Identifier() string {
	return "zcode"
}

// Execute runs a non-streaming zcode request. On a 401 carrying a signing
// verification reason the cached key is invalidated and the SAME request is
// transparently re-sent once: first with a fresh handshake, then (after a
// second rejection) unsigned — the desktop's retry-then-bypass sequence
// (zcode.cjs ClientRequestSigningV4Signer.request).
func (e *ZcodeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	auth, req, opts = e.prepareZcodeRequest(ctx, auth, req, opts)
	resp, err := e.ClaudeExecutor.Execute(ctx, auth, req, opts)
	if !zcodeSignatureRejected(auth, err) {
		return resp, err
	}
	zcodeInvalidateSigningKey(auth)
	auth, req, opts = e.prepareZcodeRequest(ctx, auth, req, opts)
	resp, err = e.ClaudeExecutor.Execute(ctx, auth, req, opts)
	if !zcodeSignatureRejected(auth, err) {
		return resp, err
	}
	zcodeDisableSigning(auth)
	auth, req, opts = e.prepareZcodeRequest(ctx, auth, req, opts)
	return e.ClaudeExecutor.Execute(ctx, auth, req, opts)
}

// ExecuteStream runs a streaming zcode request, applying the same
// invalidate -> re-handshake -> unsigned retry sequence as Execute. The retry
// only happens before any bytes reach the caller, so a mid-stream failure is
// returned as-is (matching the desktop's behavior inside a stream).
func (e *ZcodeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	auth, req, opts = e.prepareZcodeRequest(ctx, auth, req, opts)
	result, err := e.ClaudeExecutor.ExecuteStream(ctx, auth, req, opts)
	if !zcodeSignatureRejected(auth, err) {
		return result, err
	}
	zcodeInvalidateSigningKey(auth)
	auth, req, opts = e.prepareZcodeRequest(ctx, auth, req, opts)
	result, err = e.ClaudeExecutor.ExecuteStream(ctx, auth, req, opts)
	if !zcodeSignatureRejected(auth, err) {
		return result, err
	}
	zcodeDisableSigning(auth)
	auth, req, opts = e.prepareZcodeRequest(ctx, auth, req, opts)
	return e.ClaudeExecutor.ExecuteStream(ctx, auth, req, opts)
}

// prepareZcodeRequest pins the base URL, injects the ZCode source headers,
// and attaches the desktop client's session identity header. ctx bounds the
// gate/handshake/availability probes so a cancelled request stops probing.
//
// Routing (3.11.2 parity with the omo-zcode-oauth extension):
//  1. Active start plan + broker JWT -> zcode.z.ai start-plan gateway with the
//     broker JWT (unchanged local behavior; balance-gated).
//  2. Otherwise the ultra gateway (ZCodeUltraBaseURL) with the provisioned
//     Z.AI API key, Client Signing V4 signed when the signing feature gate is
//     on and the handshake succeeds. Gate/handshake failure or an env
//     override (ZCODE_ANTHROPIC_BASE_URL) sends the request unsigned — to
//     the direct endpoint when overridden, ultra otherwise.
//
// Wire delivery: ClaudeExecutor never reads opts.Headers for the outbound
// request, so injecting here would silently vanish. Headers travel in
// auth.Attributes as "header:<name>" entries — util.ApplyCustomHeadersFromAttrs
// replays them with Set() on the finished request, after the credential
// rewrite, exactly the seam the desktop's own provider entries use.
func (e *ZcodeExecutor) prepareZcodeRequest(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) {
	if ctx == nil {
		ctx = context.Background()
	}
	if auth != nil {
		auth = auth.Clone()
		if auth.Attributes == nil {
			auth.Attributes = map[string]string{}
		}
		// Wire headers (including X-Session-Id) must be installed before the
		// signature is built: BuildSigningHeaders signs over X-Session-Id and
		// fails closed when it is absent, so signing after the wire headers would
		// silently drop the signature on every request.
		useGateway := zcodeBrokerToken(auth) != "" && zcodeUseStartPlan(auth)
		zcodeSetWireHeaders(auth, useGateway)
		if useGateway {
			auth.Attributes["base_url"] = ZCodeStartPlanBaseURL
			// The broker JWT is the credential, not an extra header. Verified from
			// app.asar loadPresetProviders: the zaiStartPlan provider entry is built
			// as {id: zaiStartPlan, endpoints:{baseURL: zcodePlanAnthropicBaseUrl},
			// apiKey: p} where p = loadZaiProviderConnectionZcodeJwtToken().
			//
			// It must occupy the credential slot rather than opts.Headers, because
			// ClaudeExecutor resolves claudeCreds(auth) and then unconditionally
			// rewrites Authorization/x-api-key from it in
			// applyClaudeHeadersWithNativeProfile. A JWT injected only as a header is
			// overwritten by the provisioned Z.AI key, which the gateway rejects with
			// 401. Metadata is overridden too: claudeCreds falls back to
			// Metadata["access_token"] whenever Attributes carries no api_key.
			auth.Attributes["api_key"] = zcodeBrokerToken(auth)
			if auth.Metadata != nil {
				metadata := make(map[string]any, len(auth.Metadata))
				for k, v := range auth.Metadata {
					metadata[k] = v
				}
				metadata["access_token"] = zcodeBrokerToken(auth)
				auth.Metadata = metadata
			}
		} else {
			// Extension parity: default destination is the ultra gateway, signed
			// when possible. ZCODE_ANTHROPIC_BASE_URL overrides to the direct
			// endpoint with signing disabled AND off-peak routing off (override
			// always wins — extension offpeak.ts explicitEndpointOverride).
			override := strings.TrimSpace(os.Getenv("ZCODE_ANTHROPIC_BASE_URL"))
			if override == "" && zcodeOffPeakActive(ctx, auth, req.Model) {
				// Off-peak ("Idle plan") path: route flash traffic through the
				// ticket-gated zero-quota gateway. Credential slot = JWT
				// (start-plan style): ClaudeExecutor rewrites Authorization from
				// it, giving Bearer <jwt> exactly the off-peak contract. Ticket
				// and API-key headers travel as header: attrs, replayed after the
				// credential rewrite.
				auth.Attributes["base_url"] = zcode.OffPeakBaseURL
				auth.Attributes["api_key"] = zcodeBrokerToken(auth)
				if auth.Metadata != nil {
					metadata := make(map[string]any, len(auth.Metadata))
					for k, v := range auth.Metadata {
						metadata[k] = v
					}
					metadata["access_token"] = zcodeBrokerToken(auth)
					auth.Metadata = metadata
				}
				zcodeAttachOffPeakTicket(auth)
			} else {
				baseURL := ZCodeUltraBaseURL
				sign := true
				if override != "" {
					baseURL = override
					sign = false
				}
				auth.Attributes["base_url"] = baseURL
				if sign {
					zcodeSignRequest(ctx, auth)
				}
			}
		}
		if !useGateway {
			req = zcodeInjectUserIdentity(req, auth)
		}
	}
	return auth, req, opts
}

// --- off-peak ("Idle plan") routing ---

var (
	zcodeOffPeakTicket *zcode.OffPeakTicketState
	zcodeOffPeakTaskID string
	zcodeOffPeakMu     sync.Mutex
)

// zcodeOffPeakTaskID returns the stable per-process task id the ticket queue
// requires (extension currentOffPeakTaskId).
func zcodeCurrentOffPeakTaskID() string {
	zcodeOffPeakMu.Lock()
	defer zcodeOffPeakMu.Unlock()
	if zcodeOffPeakTaskID == "" {
		zcodeOffPeakTaskID = "omo-offpeak-" + zcodeUUID()
	}
	return zcodeOffPeakTaskID
}

// zcodeOffPeakEnabled reports whether off-peak routing is turned on. Opt-in
// only, mirroring the extension's ZCODE_OFFPEAK_ENABLE=1 gate.
func zcodeOffPeakEnabled() bool {
	return os.Getenv("ZCODE_OFFPEAK_ENABLE") == "1"
}

// zcodeOffPeakActive decides whether THIS request should take the off-peak
// path: enabled, in window, flash model, broker JWT present, and the plan
// currently grants tickets. Availability is checked once per window per
// decision point — cheap enough at request granularity.
func zcodeOffPeakActive(ctx context.Context, auth *cliproxyauth.Auth, model string) bool {
	if !zcodeOffPeakEnabled() {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if zcodeBrokerToken(auth) == "" {
		return false
	}
	now := time.Now()
	if !zcode.IsOffPeakWindow(now) {
		return false
	}
	if !zcode.IsFlashModelID(model) {
		return false
	}
	jwt := zcodeBrokerToken(auth)
	apiKey := zcodeCreds(auth)
	if apiKey == "" {
		return false
	}
	availabilityCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return zcode.OffPeakAvailability(availabilityCtx, jwt, apiKey)
}

// zcodeAttachOffPeakTicket ensures a ready ticket exists (reusing the cached
// one when fresh) and installs the off-peak auth header set on the cloned
// auth: X-Off-Peak-Ticket-ID plus X-Coding-Plan-Api-Key. The Authorization
// header itself comes from the credential slot (JWT), set by the caller.
// Fail-open: any ticket failure logs at debug and leaves the request without
// a ticket — the executor returns the upstream error visibly rather than
// silently mis-routing (extension installOffPeakAuth contract).
func zcodeAttachOffPeakTicket(auth *cliproxyauth.Auth) {
	jwt := zcodeBrokerToken(auth)
	apiKey := zcodeCreds(auth)
	if jwt == "" || apiKey == "" {
		return
	}
	now := time.Now()
	zcodeOffPeakMu.Lock()
	fresh := zcode.OffPeakTicketFresh(zcodeOffPeakTicket, now, jwt, apiKey)
	ticketID := ""
	if fresh {
		ticketID = zcodeOffPeakTicket.TicketID
	}
	zcodeOffPeakMu.Unlock()
	if !fresh {
		ctx, cancel := context.WithTimeout(context.Background(), zcode.OffPeakReadyWaitMS+15*time.Second)
		defer cancel()
		id, err := zcode.EnsureOffPeakTicket(ctx, jwt, apiKey, zcodeCurrentOffPeakTaskID())
		if err != nil {
			log.Debugf("zcode off-peak: ticket unavailable, staying on ultra: %v", err)
			return
		}
		ticketID = id
		zcodeOffPeakMu.Lock()
		zcodeOffPeakTicket = &zcode.OffPeakTicketState{
			JWT: jwt, APIKey: apiKey, TicketID: ticketID,
			TakenAt: time.Now(), WindowEpoch: kstEpochOf(time.Now()),
		}
		zcodeOffPeakMu.Unlock()
	}
	if auth.Attributes == nil {
		auth.Attributes = map[string]string{}
	}
	auth.Attributes["header:X-Off-Peak-Ticket-ID"] = ticketID
	auth.Attributes["header:X-Coding-Plan-Api-Key"] = apiKey
	// The signed-request headers must not ride along on off-peak requests
	// (extension SIGNATURE_HEADERS stripping).
	for _, name := range zcodeSignatureHeaderNames {
		delete(auth.Attributes, "header:"+name)
	}
}

// zcodeSignatureHeaderNames are the Client Signing V4 headers stripped from
// off-peak requests.
var zcodeSignatureHeaderNames = []string{
	"X-Client-Ts", "X-Client-Version", "X-Client-Sig", "X-Client-Nonce",
	"X-Client-Pow", "X-App-Id", "X-Client-Sign-Verified",
}

// kstEpochOf mirrors zcode.kstWindowEpoch via a fresh computation.
func kstEpochOf(now time.Time) int64 {
	return now.Add(-9*time.Hour).UTC().Unix() / 86_400
}

// zcodeSignRequest attaches Client Signing V4 headers to the cloned auth via
// header: attributes. Fail-open: any gate/handshake/signing error logs at
// debug and leaves the request unsigned, mirroring the desktop and the
// extension reference (omo-zcode-oauth signing.ts).
func zcodeSignRequest(ctx context.Context, auth *cliproxyauth.Auth) {
	if ctx == nil {
		ctx = context.Background()
	}
	credential := zcodeCreds(auth)
	if credential == "" {
		return
	}
	apiKeyID, _, err := zcode.ParseSigningCredential(credential)
	if err != nil {
		log.Debugf("zcode signing: parse credential: %v", err)
		return
	}
	if !zcodeSigningEnabled(ctx, credential) {
		return
	}
	zcodeSigningMu.Lock()
	bypassed := zcodeBypassSigning[credential]
	zcodeSigningMu.Unlock()
	if bypassed {
		return
	}
	priv, err := zcodeHandshakeFor(ctx, credential)
	if err != nil {
		log.Debugf("zcode signing: handshake unavailable, sending unsigned: %v", err)
		return
	}
	sessionID := auth.Attributes["header:X-Session-Id"]
	headers, err := zcode.BuildSigningHeaders(priv, apiKeyID, sessionID, zcodeAppVersion(), time.Now())
	if err != nil {
		log.Debugf("zcode signing: build headers: %v", err)
		return
	}
	// Attribution headers travel with signed requests on the desktop agent
	// path (zcode.cjs xnt): request id, trace id, query id, session type.
	headers["x-request-id"] = zcodeUUID()
	headers["x-zcode-trace-id"] = zcodeUUID()
	headers["x-query-id"] = zcodeUUID()
	headers["x-zcode-session-type"] = "main"
	for name, value := range headers {
		auth.Attributes["header:"+name] = value
	}
}

// zcodeSetWireHeaders materializes the ZCode desktop wire identity as
// "header:<name>" attributes. ClaudeExecutor's credential rewrite would drop
// anything injected into opts.Headers, but ApplyCustomHeadersFromAttrs replays
// these onto the finished upstream request after the auth rewrite. Called on
// the cloned auth only — the original record is never mutated.
func zcodeSetWireHeaders(auth *cliproxyauth.Auth, useGateway bool) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = map[string]string{}
	}
	for name, value := range buildZCodeSourceHeaders() {
		auth.Attributes["header:"+name] = value[0]
	}
	// X-Device-Mid is only set by the host request path (dynamic per-request
	// variant); the agent path omits it, so the extension reference and this
	// executor do not send it either.
	auth.Attributes["header:X-Session-Id"] = zcodeSessionID(auth)
}

// zcodeSignatureRejected reports whether err is a 401 whose body carries one
// of the desktop's refreshable signature verification reasons
// (VERIFY_SIGNATURE_INVALID / VERIFY_APIKEY_EXPIRED).
func zcodeSignatureRejected(auth *cliproxyauth.Auth, err error) bool {
	if err == nil || auth == nil {
		return false
	}
	status, body, ok := zcodeUpstreamStatusBody(err)
	return ok && zcode.HasRefreshableSignatureReason(status, body)
}

// zcodeInvalidateSigningKey drops the cached Ed25519 private key for the
// credential so the next prepare performs a fresh handshake (desktop
// invalidatePrivateKey).
func zcodeInvalidateSigningKey(auth *cliproxyauth.Auth) {
	if credential := zcodeCreds(auth); credential != "" {
		zcodeSigningMu.Lock()
		delete(zcodePrivateKeys, credential)
		zcodeSigningMu.Unlock()
		log.Debug("zcode signing: upstream rejected signature, handshake key invalidated")
	}
}

// zcodeDisableSigning turns off signing for the credential after a rejected
// retry, mirroring the desktop's bypassSigning latch. The cached key is also
// dropped so a later gate-driven attempt starts from a fresh handshake.
func zcodeDisableSigning(auth *cliproxyauth.Auth) {
	credential := zcodeCreds(auth)
	zcodeSigningMu.Lock()
	delete(zcodePrivateKeys, credential)
	if credential != "" {
		zcodeBypassSigning[credential] = true
	}
	zcodeSigningMu.Unlock()
	log.Debug("zcode signing: rejected after re-handshake, sending unsigned (bypass)")
}

// zcodeUpstreamStatusBody extracts (status, body) from an upstream HTTP error
// without depending on the concrete error type.
func zcodeUpstreamStatusBody(err error) (int, []byte, bool) {
	type statusBodyer interface {
		StatusCode() int
		Body() []byte
	}
	var target statusBodyer
	if errors.As(err, &target) {
		return target.StatusCode(), target.Body(), true
	}
	return 0, nil, false
}

// zcodeResetSigningStateForTests clears every signing-side cache. Test-only.
func zcodeResetSigningStateForTests() {
	zcodeSigningMu.Lock()
	zcodeGateStates = map[string]zcodeSignGate{}
	zcodePrivateKeys = map[string]ed25519.PrivateKey{}
	zcodeBypassSigning = map[string]bool{}
	zcodeSigningMu.Unlock()
	zcodeOffPeakMu.Lock()
	defer zcodeOffPeakMu.Unlock()
	zcodeOffPeakTicket = nil
	zcodeOffPeakTaskID = ""
}

// --- Client Signing V4 wiring (gate -> handshake -> per-request signing) ---

var (
	zcodeGateStates    = map[string]zcodeSignGate{}
	zcodePrivateKeys   = map[string]ed25519.PrivateKey{}
	zcodeBypassSigning = map[string]bool{}
	zcodeHTTPClient    = &http.Client{Timeout: 15 * time.Second}
	zcodeSigningMu     sync.Mutex
)

// zcodeSignGate caches the affirmative codingPlanSignature gate answer for
// one hour. Negative answers are never cached (fail-open).
type zcodeSignGate struct {
	enabled   bool
	checkedAt time.Time
}

// zcodeSigningEnabled reports whether the upstream signing feature gate is on
// for the credential. Any failure reads as "disabled" (send unsigned). The
// answer is cached per credential for one hour; negatives are never cached,
// mirroring the extension's per-API-key states map (signing.ts signingEnabled).
func zcodeSigningEnabled(ctx context.Context, credential string) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	zcodeSigningMu.Lock()
	cached, ok := zcodeGateStates[credential]
	zcodeSigningMu.Unlock()
	if ok && cached.enabled && time.Since(cached.checkedAt) < zcodeGateTTL {
		return true
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ZCodeSigningGateURL, nil)
	if err != nil {
		return false
	}
	for name, value := range buildZCodeSourceHeaders() {
		req.Header.Set(name, value[0])
	}
	req.Header.Set("x-api-key", credential)
	resp, err := zcodeHTTPClient.Do(req)
	if err != nil {
		log.Debugf("zcode signing gate: request failed: %v", err)
		return false
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Debugf("zcode signing gate: close: %v", err)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return false
	}
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			CodingPlanSignature struct {
				Enable bool `json:"enable"`
			} `json:"codingPlanSignature"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Code != 0 || !envelope.Data.CodingPlanSignature.Enable {
		return false
	}
	zcodeSigningMu.Lock()
	zcodeGateStates[credential] = zcodeSignGate{enabled: true, checkedAt: time.Now()}
	zcodeSigningMu.Unlock()
	return true
}

// zcodeHandshakeFor returns the cached Ed25519 private key for the credential,
// performing the get_sign_key handshake on first use. A failed handshake is
// retried on the next request (the failing request itself sends unsigned).
func zcodeHandshakeFor(ctx context.Context, credential string) (ed25519.PrivateKey, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	zcodeSigningMu.Lock()
	if priv, ok := zcodePrivateKeys[credential]; ok {
		zcodeSigningMu.Unlock()
		return priv, nil
	}
	zcodeSigningMu.Unlock()
	priv, err := zcode.Handshake(ctx, "https://api.z.ai", credential, zcodeHTTPClient)
	if err != nil {
		return nil, err
	}
	zcodeSigningMu.Lock()
	zcodePrivateKeys[credential] = priv
	zcodeSigningMu.Unlock()
	return priv, nil
}

// zcodeUUID returns a random UUID string for attribution headers.
func zcodeUUID() string {
	b := make([]byte, 16)
	_, _ = crand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	// 8-4-4-4-12 hex layout: the last group reads bytes 10..15 (6 bytes).
	var last [8]byte
	copy(last[2:], b[10:16])
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		binary.BigEndian.Uint32(b[0:4]),
		binary.BigEndian.Uint16(b[4:6]),
		binary.BigEndian.Uint16(b[6:8]),
		binary.BigEndian.Uint16(b[8:10]),
		binary.BigEndian.Uint64(last[:])&0xffffffffffff)
}

// zcodeUseStartPlan reports whether the auth record has an active start plan
// and a broker token to route through the ZCode gateway with.
func zcodeUseStartPlan(auth *cliproxyauth.Auth) bool {
	if auth == nil {
		return false
	}
	if v, ok := auth.Attributes["start_plan_active"]; ok && v == "true" {
		return zcodeBrokerToken(auth) != ""
	}
	if v, ok := auth.Metadata["start_plan_active"].(bool); ok && v {
		return zcodeBrokerToken(auth) != ""
	}
	// Legacy key persisted by pre-revert builds (v7.2.146-4 era).
	if v, ok := auth.Metadata["start_plan"].(bool); ok && v {
		return zcodeBrokerToken(auth) != ""
	}
	return false
}

// zcodeBrokerToken returns the broker JWT stored alongside the auth record.
// Attributes["zcode_token"] is the freshest copy (set in-memory right after
// OAuth); Metadata["zcode_token"] survives reload from disk.
func zcodeBrokerToken(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if tok := strings.TrimSpace(auth.Attributes["zcode_token"]); tok != "" {
			return tok
		}
	}
	if auth.Metadata != nil {
		if tok, ok := auth.Metadata["zcode_token"].(string); ok {
			return strings.TrimSpace(tok)
		}
	}
	return ""
}

// buildZCodeSourceHeaders replicates ZCode's buildZCodeSourceHeaders() so
// api.z.ai sees the request as the ZCode client. Printable-ASCII only;
// platform/arch resolved at runtime. X-Device-Mid is intentionally omitted:
// the desktop agent path does not send it (extension P5 decision).
func buildZCodeSourceHeaders() http.Header {
	h := http.Header{}
	h.Set("User-Agent", zcodeUserAgent())
	h.Set("HTTP-Referer", "https://zcode.z.ai")
	h.Set("X-Title", "Z Code@electron")
	h.Set("X-Platform", runtime.GOOS+"-"+runtime.GOARCH)
	h.Set("X-Client-Language", language())
	h.Set("X-Client-Timezone", timezone())
	h.Set("X-Os-Category", normalizeOsCategory(runtime.GOOS))
	h.Set("X-ZCode-Agent", "glm")
	h.Set("X-ZCode-App-Version", zcodeAppVersion())
	h.Set("X-Release-Channel", zcodeReleaseChannel())
	h.Set("X-Os-Version", osVersion())
	return h
}

// language returns the client language locale the way Intl reports it
// ("ko-KR"), falling back to "en-US". POSIX values such as "ko_KR.UTF-8" are
// normalized to the BCP-47 form the desktop sends.
func language() string {
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG", "LANGUAGE"} {
		value := zcodeEnv(os.Getenv(name))
		if value == "" {
			continue
		}
		if idx := strings.IndexAny(value, ":.@"); idx >= 0 {
			value = value[:idx]
		}
		value = strings.ReplaceAll(value, "_", "-")
		if value == "C" || value == "POSIX" {
			continue
		}
		return value
	}
	return "en-US"
}

// timezone returns the IANA client timezone (the desktop reads
// Intl.DateTimeFormat().resolvedOptions().timeZone), falling back to "UTC".
func timezone() string {
	if tz := zcodeEnv(os.Getenv("TZ")); tz != "" {
		return tz
	}
	// POSIX systems link /etc/localtime into the zoneinfo tree.
	if target, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
		if idx := strings.Index(target, "/zoneinfo/"); idx >= 0 {
			if name := strings.Trim(strings.TrimPrefix(target[idx:], "/zoneinfo/"), "/"); name != "" {
				return name
			}
		}
	}
	// Debian-family fallback.
	if data, err := os.ReadFile("/etc/timezone"); err == nil {
		if name := zcodeEnv(strings.SplitN(string(data), "\n", 2)[0]); name != "" {
			return name
		}
	}
	return "UTC"
}

// deviceMid reads the ZCode device ID from the telemetry file at the path the
// app itself uses (~/.zcode/v2/telemetry-state.json), creating one in the
// app's format when absent so a later real app install adopts the same id.
// ZCODE_DEVICE_ID overrides the file, matching the extension reference.
func deviceMid() string {
	if mid := zcodeEnv("ZCODE_DEVICE_ID"); mid != "" {
		return mid
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	path := filepath.Join(home, ".zcode", "v2", "telemetry-state.json")
	if data, err := os.ReadFile(path); err == nil {
		var m map[string]any
		if json.Unmarshal(data, &m) == nil {
			if v, ok := m["deviceMid"].(string); ok {
				if mid := zcodeEnv(v); mid != "" {
					return mid
				}
			}
		}
	} else if os.IsNotExist(err) {
		mid := zcodeUUID()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			if f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644); err == nil {
				defer func() {
					if err := f.Close(); err != nil {
						log.Debugf("zcode deviceMid: close: %v", err)
					}
				}()
				if data, err := json.MarshalIndent(map[string]string{"deviceMid": mid}, "", "  "); err == nil {
					_, _ = f.Write(data)
					return mid
				}
			}
		}
	}
	return ""
}

// osVersion returns the OS version string. Node's os.version() — which the
// desktop reports — is the kernel release ("5.14.0-…"), not the distribution
// release, so read the kernel release rather than /etc/os-release.
func osVersion() string {
	if runtime.GOOS == "linux" {
		if data, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
			if release := strings.TrimSpace(string(data)); release != "" {
				return release
			}
		}
	}
	if runtime.GOOS == "darwin" {
		out, _ := exec.Command("uname", "-r").Output()
		return strings.TrimSpace(string(out))
	}
	if runtime.GOOS == "windows" {
		out, _ := exec.Command("cmd", "/c", "ver").Output()
		return strings.TrimSpace(string(out))
	}
	return ""
}

// normalizeOsCategory maps a GOOS to ZCode's OS category.
func normalizeOsCategory(goos string) string {
	switch goos {
	case "darwin":
		return "macos"
	case "windows":
		return "windows"
	case "linux":
		return "linux"
	default:
		return "linux"
	}
}

// mergeHeaders merges extra headers into base, with extra winning.
func mergeHeaders(base, extra http.Header) http.Header {
	if base == nil {
		base = http.Header{}
	}
	for k, vs := range extra {
		for _, v := range vs {
			base.Set(k, v)
		}
	}
	return base
}

// zcodeCreds returns the provisioned Z.AI API key from the auth record.
//
// Attributes["api_key"] is populated in-memory right after OAuth, but zcode auth
// files persist as the flat TokenStorage form and carry no "attributes" object,
// so a record reloaded from disk has a nil Attributes map. Metadata["access_token"]
// holds the same provisioned Z.AI API key "{id}.{secret}" (see
// internal/auth/zcode/zcode.go Credentials.AccessToken), so it is the correct
// fallback: without it the key is silently lost on every restart or config reload.
func zcodeCreds(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if key := strings.TrimSpace(auth.Attributes["api_key"]); key != "" {
			return key
		}
	}
	if auth.Metadata != nil {
		if token, ok := auth.Metadata["access_token"].(string); ok {
			return strings.TrimSpace(token)
		}
	}
	return ""
}

// zcodeInjectUserIdentity stamps the desktop's device-identity metadata onto
// the anthropic request body (zcode.cjs A2e/UIo):
//
//	metadata.user_id = JSON({device_id, account_uuid: "", session_id})
//
// The gateway classifies signed traffic with this marker; requests without it
// are billed at par. Existing metadata.user_id values are never overwritten.
func zcodeInjectUserIdentity(req cliproxyexecutor.Request, auth *cliproxyauth.Auth) cliproxyexecutor.Request {
	if len(req.Payload) == 0 {
		return req
	}
	deviceId := deviceMid()
	if deviceId == "" {
		return req
	}
	var payload map[string]any
	if json.Unmarshal(req.Payload, &payload) != nil {
		return req
	}
	metadata, _ := payload["metadata"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
	}
	if _, exists := metadata["user_id"]; exists {
		return req
	}
	sessionID := ""
	if auth != nil {
		sessionID = auth.Attributes["header:X-Session-Id"]
	}
	identity, _ := json.Marshal(map[string]string{"device_id": deviceId, "account_uuid": "", "session_id": sessionID})
	metadata["user_id"] = string(identity)
	payload["metadata"] = metadata
	if updated, err := json.Marshal(payload); err == nil {
		req.Payload = updated
	}
	return req
}

// zcodeSessionID derives the per-account session identity logged on the auth
// record. The X-Session-Id value feeds the Client Signing V4 PoW salt and the
// gateway's session tracking, so it must stay stable across restarts for the
// same account. Hashing the account email/key avoids leaking any credential
// material while keeping per-account separation.
func zcodeSessionID(auth *cliproxyauth.Auth) string {
	seed := ""
	if auth != nil {
		if auth.Metadata != nil {
			if v, ok := auth.Metadata["account_id"].(string); ok && v != "" {
				seed = v
			}
		}
		if seed == "" && auth.Attributes != nil {
			seed = auth.Attributes["email"]
		}
	}
	if seed == "" {
		seed = "zcode"
	}
	sum := sha256.Sum256([]byte("zcode-session\n" + seed))
	return hex.EncodeToString(sum[:16])
}
