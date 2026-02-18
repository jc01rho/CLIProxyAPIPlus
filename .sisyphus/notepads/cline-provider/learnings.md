# Cline Provider - Learnings

## 2026-02-18 Task: Initial Analysis
- Cline uses OAuth 2.0 via WorkOS for authentication
- Base URL: `https://api.cline.bot`
- Auth header format: `Bearer workos:{accessToken}` (CRITICAL: the `workos:` prefix!)
- Custom headers: `HTTP-Referer: https://cline.bot`, `X-Title: Cline`
- Chat completion endpoint: `POST /api/v1/chat/completions` (OpenAI-compatible)
- Token response: `{ success: true, data: { accessToken, refreshToken, expiresAt, userInfo } }`
- Token refresh: 5 minutes before expiry
- OAuth callback: local HTTP server on ports 48801-48811
- Model IDs use OpenRouter format: `anthropic/claude-sonnet-4.6`
- Free models: `["anthropic/claude-sonnet-4.6", "kwaipilot/kat-coder-pro", "z-ai/glm-5"]`
- Provider constant should be `"cline"`
- Kilo provider pattern is the closest reference (device flow vs OAuth, but same executor pattern)
- Module path: `github.com/router-for-me/CLIProxyAPI/v6`
- Use `util.NewProxyClient()` or `newProxyAwareHTTPClient()` for HTTP clients
- `refresh_registry.go` already has `kilocode` entry - "kilo" and "kilocode" are different providers

## 2026-02-18 Task: Cline auth package implementation
- `internal/auth/cline/cline_token.go` was added following Kilo token persistence pattern exactly (MkdirAll 0700, JSON encode, `misc.LogSavingCredentials`, `Type="cline"`).
- `ClineTokenStorage` stores `accessToken`, `refreshToken`, `expiresAt`, `email`, `userId`, `displayName`, and `type`.
- `internal/auth/cline/cline_auth.go` implements WorkOS OAuth flow endpoints:
  - `GET /api/v1/auth/authorize` with `callbackUrl`
  - `POST /api/v1/auth/token` with `{code, state}`
  - `POST /api/v1/auth/refresh` with `{refreshToken}`
  - `GET /api/v1/users/me` with `Authorization: Bearer workos:{accessToken}`
- Added local callback server method with automatic fallback over ports `48801..48811` and graceful shutdown after receiving code/state.
- `expiresAt` parsing supports integer, float, numeric string, and RFC3339/RFC3339Nano timestamp string formats to match possible API variants.

## 2026-02-18 Task: Create cline_models.go
- Created `/home/jc01rho/git/cli-proxy/CLIProxyAPIPlus/internal/registry/cline_models.go`
- Followed exact pattern from `kilo_models.go` (same package structure, no imports needed)
- Function: `GetClineModels() []*ModelInfo`
- Models defined:
  - `cline/auto`: Auto model selection with thinking support (200K context, 64K completion)
  - `anthropic/claude-sonnet-4.6`: Claude Sonnet 4.6 via Cline (200K context, 64K completion, thinking support)
  - `kwaipilot/kat-coder-pro`: KAT Coder Pro via Cline (128K context, 32K completion)
  - `z-ai/glm-5`: GLM-5 via Cline (128K context, 32K completion)
- All models have Type="cline", OwnedBy="cline"
- No LSP errors in the created file

## 2026-02-18 Task: Create cline_executor.go
- Created `/home/jc01rho/git/cli-proxy/CLIProxyAPIPlus/internal/runtime/executor/cline_executor.go`
- Followed exact pattern from kilo_executor.go (same structure and method organization)
- Key implementations:
  - `ClineExecutor` struct with config
  - `NewClineExecutor()` constructor
  - `Identifier()` returns "cline"
  - `PrepareRequest()` - applies Cline headers with workos: prefix
  - `HttpRequest()` - raw HTTP request execution
  - `Execute()` - non-streaming chat completion
  - `ExecuteStream()` - streaming chat completion with SSE handling
  - `Refresh()` - placeholder (returns auth as-is, will be enhanced when cline auth package is ready)
  - `CountTokens()` - returns unsupported error (matching Kilo pattern)
  - `clineCredentials()` - extracts tokens from auth metadata/attributes
  - `applyClineHeaders()` - sets required headers including Authorization: Bearer workos:{token}
  - `FetchClineModels()` - dynamic model fetching from Cline API
- Critical implementation details:
  - Authorization header uses `Bearer workos:` prefix (CRITICAL for Cline API)
  - Custom headers: HTTP-Referer: https://cline.bot, X-Title: Cline
  - API endpoint: https://api.cline.bot/api/v1/chat/completions
  - Uses existing executor package utilities (newProxyAwareHTTPClient, newUsageReporter, parseOpenAIUsage, etc.)
- File compiles successfully with existing codebase (verified with go build)
- All comments follow Go docstring conventions matching kilo_executor.go pattern

## 2026-02-18 Task: Integrate Cline into registration wiring (6-file surgical update)
- Registration touched exactly 6 existing files, following Kilo registration pattern with minimal deltas.
- `internal/constant/constant.go`: added `Cline = "cline"` constant for provider identity consistency.
- `internal/cmd/auth_manager.go`: registered `sdkAuth.NewClineAuthenticator()` in manager constructor list.
- `sdk/auth/refresh_registry.go`: added refresh lead registration for `cline` using `NewClineAuthenticator()`.
- `internal/registry/model_definitions.go`: wired `case "cline"` in static channel lookup and included `GetClineModels()` in global static lookup slice.
- `sdk/cliproxy/service.go`: wired executor registration (`NewClineExecutor`) and dynamic model fetch path (`FetchClineModels`).
- `sdk/cliproxy/auth/oauth_model_alias.go`: added `cline` to OAuth model-alias-supported providers.
- Build verification passed with `go build ./cmd/server` from `CLIProxyAPIPlus`.
- LSP diagnostics tool in this session reports workspace-scoping warnings (`No active builds contain ...`) rather than code issues; build success served as functional compile verification.

## 2026-02-18 Task: Cline OAuth 파라미터/토큰 교환 정합성 수정
- `/api/v1/auth/authorize` 호출 시 Cline은 `callbackUrl`이 아니라 `client_type=extension`, `callback_url`, `redirect_uri`를 기대한다.
- authorize 응답은 환경에 따라 두 형태가 가능하다: (1) HTTP 3xx + `Location` 헤더, (2) 200 JSON + `redirect_url`/`url`.
- OAuth state는 JSON의 `state`가 있으면 우선 사용하고, 없으면 redirect URL query의 `state`를 파싱하며, 둘 다 없을 때만 fallback 생성이 안전하다.
- `/api/v1/auth/token` 교환은 `{grant_type, code, client_type, redirect_uri}` 조합이 필요하며 기존 `state` 기반 payload는 400 원인이 된다.
- `ExchangeCode` 시그니처가 `callbackURL` 기반으로 바뀌면 SDK/관리 핸들러 호출부도 함께 맞춰야 컴파일 및 런타임 일관성이 유지된다.
