# Learnings

## 2026-02-20 Phase 2 Start
- FetchAntigravityModels requires LS to be running → fails during model registration
- ZeroGravity uses static model list in models.rs (7 models with protobuf enums)
- Other providers (Claude, Gemini, Kiro, etc.) all use static model lists via registry functions
- Model registration happens in service.go registerModelsForAuth() around line 841
- GetAntigravityModelConfig() in model_definitions_static_data.go has Thinking configs for 14+ models
- LS binary uses `-standalone=true` currently (detectable), ZeroGravity uses `--enable_lsp` etc.
- LS client uses plain HTTP JSON-RPC currently, ZeroGravity uses HTTPS with CSRF + Chrome headers

## 2026-02-20 Phase 2 Complete - Antigravity Static Model Registration

### Implementation Summary
Fixed Antigravity model registration to use static model list instead of dynamic LS fetching.

### Changes
1. **Added `GetAntigravityModels()`** to `model_definitions_static_data.go`
   - 19 models total (ZeroGravity core + existing LS models)
   - All with `OwnedBy: "antigravity"`, `Type: "antigravity"`
   - Thinking support configured per existing `GetAntigravityModelConfig()`

2. **Updated `service.go`** line 841-844
   - Changed from `executor.FetchAntigravityModels(ctx, a, s.cfg)` to `registry.GetAntigravityModels()`
   - Removed context/cancel wrapper

### Verification
- Build: `go build ./cmd/server` ✓
- Tests: registry and cliproxy packages passed ✓

### Key Models Added
- opus-4.6, sonnet-4.6 (ZeroGravity core)
- gemini-3-flash, gemini-3.1-pro (ZeroGravity core)
- gemini-2.5-flash, gemini-2.5-flash-lite (existing)
- gemini-3-pro-high, gemini-3-pro-image (existing)
- claude-opus-4-5/4-6-thinking, claude-sonnet-4-5/4-6 (existing)
- gpt-oss-120b-medium (existing)

## 2026-02-20 Phase 3 Complete - ZeroGravity LS Infrastructure Migration

### Implementation Summary
Rewrote the Antigravity LS subprocess infrastructure to match ZeroGravity's approach.

### Files Created/Modified
1. **Created `ls_stub_server.go`** - Stub TCP extension server (361 lines)
   - Handles Connect RPC protocol (HTTP/1.1, ServerStream)
   - Implements SubscribeToUnifiedStateSyncTopic with chunked transfer-encoding
   - Serves OAuth token via GetSecretValue
   - Sends keepalive every 5 seconds to keep stream alive

2. **Rewrote `ls_process.go`** - ZeroGravity-style spawn (594 lines)
   - Added CSRF token generation (UUID v4)
   - Added stub server integration
   - New CLI args with double-dash flags
   - Proper environment variables (VSCODE_*, CHROME_DESKTOP, etc.)
   - Directory setup with user_settings.pb
   - Init metadata protobuf via stdin
   - Protobuf encoding helpers (appendProtoString, appendProtoVarint, appendVarint)

3. **Rewrote `ls_client.go`** - HTTPS + CSRF + Chrome headers (119 lines)
   - HTTPS with TLS skip-verify for self-signed certs
   - CSRF token header: x-codeium-csrf-token
   - Chrome-like headers in proper order
   - Service path: exa.language_server_pb.LanguageServerService/{method}
   - HealthCheck uses TCP connect instead of HTTP

4. **Updated `antigravity_executor.go`** - Wired CSRF and new request format
   - NewLSClient now takes CSRF token parameter
   - SendRequest calls include method name (SendUserCascadeMessage, CountTokens, FetchAvailableModels)
   - All 6 SendRequest call sites updated

### Key Technical Details

#### Connect RPC Envelope Format
```
[flag(1 byte)] [length(4 bytes big-endian)] [data(N bytes)]
- flag 0x00 = data message (protobuf)
- flag 0x02 = end-of-stream trailer (JSON "{}")
```

#### CLI Args (Critical)
```go
args := []string{
    "--enable_lsp",
    fmt.Sprintf("--lsp_port=%d", lspPort),
    "--extension_server_port", fmt.Sprintf("%d", stubServer.Port()),
    "--csrf_token", csrfToken,
    "--server_port", fmt.Sprintf("%d", port),
    "--workspace_id", workspaceID,
    "--cloud_code_endpoint", "https://daily-cloudcode-pa.googleapis.com",
    "--app_data_dir", "antigravity",  // NOTE: relative path only!
    "--gemini_dir", geminiDir,
}
```

#### Init Metadata Protobuf Fields
- Field 1: api_key (UUID v4)
- Field 3: ide_name ("antigravity")
- Field 4: antigravity_version ("1.107.0")
- Field 5: ide_version ("1.16.5")
- Field 6: locale ("en_US")
- Field 10: session_id (UUID v4)
- Field 11: editor_name ("antigravity")
- Field 24: device_fingerprint (UUID)
- Field 34: detect_and_use_proxy (varint 1)

### Testing Updates
- `ls_client_test.go` - Updated for new signatures (HTTPS, CSRF, method param)
- `ls_process_test.go` - Updated for new buildCommand signature (5 return values)

### Verification
- Build: `go build ./cmd/server` ✓
- Tests: `go test ./internal/runtime/executor/...` ✓

### Critical Gotchas
1. LS binary rejects absolute paths for --gemini_dir and --app_data_dir
2. LS uses self-signed certs - must use InsecureSkipVerify
3. OAuth token in stub server must be base64-encoded in Topic proto
4. SubscribeToUnifiedStateSyncTopic must stay open or LS reconnects in tight loop
5. Cannot have duplicate function names in same package (appendVarint → stubAppendVarint)
