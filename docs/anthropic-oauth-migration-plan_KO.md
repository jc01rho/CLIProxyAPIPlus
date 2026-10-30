# Anthropic OAuth 송수신 로직 비교 및 마이그레이션 계획

작성일: 2026-10-30. 이 문서는 계획 문서이며, 코드 동작은 바꾸지 않는다.

## 0. 기준 커밋

| 대상 | 저장소 | 커밋 |
|------|--------|------|
| 우리 | jc01rho/CLIProxyAPIPlus `main` | `e45449ff02322cb7a8d4dbe8895d81f066e38872` |
| 비교 대상 | cortexkit/anthropic-auth `main` | `b9a8bd928f6859efc8e26ac217225a9fd2dc290c` (2026-10-30, package `@cortexkit/anthropic-auth-core` 2.4.4) |
| 비교 대상 의존성 | cortexkit/common-auth | npm `@cortexkit/common-auth@0.31.0` (main `d079e207f5172a1bf5cbf3e4bd1e7b63edb88807`) |

요청에서 지목한 cortexkit 브랜치 세 개는 이미 `main`에 들어가 있다(branch tip이 main의 조상이고, merge commit 없이 직접 반영됨).

| 브랜치 | tip | 핵심 커밋 | 내용 |
|--------|-----|-----------|------|
| `feat/claude-code-2.1.302-betas` | `f27445c7` | `b8aaf18f3` | 요청 종류별(main/title/quota) beta 목록, `claude-code-20250219` 선두 배치, advisor/fallback-credit beta 제거 |
| `feat/claude-code-2.1.302-gzip` | `16923b29` | `e926ce35a` | Claude Code 2.1.302로 위장, `x-claude-code-request-class`/`x-claude-code-prompt-id` 헤더, billing `cc_turn_*` 필드, 4096자 이상 본문 gzip |
| `fix/per-turn-effort-opus-sonnet-55` | `38627af02` | `d56b35d9f` → `38627af02` | 메시지 단위 effort 마커를 Opus 5.5·Sonnet 5.5로 한정(Fable 5.1은 매 요청 상단 effort 고정) |

주의: 로컬 체크아웃의 `origin`(jc01rho/claude-lb-cpa, main `d3f59b475`)은 이 저장소 main과 갈라져 있다(merge-base 이후 plus 쪽 92커밋, claude-lb-cpa 쪽 29커밋). 특히 `claude_executor_request.go`에 175줄 안팎의 차이가 있다. 이 문서는 CLIProxyAPIPlus main 기준이며, claude-lb-cpa에 이식할 때는 단계 1에서 그 차이를 다시 대조해야 한다.

## 1. 현재 구조 (우리)

### 1.1 토큰 발급/갱신

| 항목 | 값 | 위치 |
|------|----|------|
| authorize | `https://claude.com/cai/oauth/authorize` (콘솔 모드 `https://platform.claude.com/oauth/authorize`는 로그인 경로에서 선택되지 않음) | `internal/auth/claude/anthropic_auth.go:28-29`, `GenerateAuthURL` |
| token/refresh | `https://platform.claude.com/v1/oauth/token` | `anthropic_auth.go:30-31`, `tokenEndpoint` |
| client_id | `9d1c250a-e61b-44d9-88ed-5944d1962f5e` | `anthropic_auth.go:32` |
| redirect | `http://localhost:54545/callback` (로컬 콜백 서버) | `anthropic_auth.go:33`, `oauth_server.go` |
| scope | 로그인: `org:create_api_key` + 아래 / 갱신: `user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload` | `anthropic_auth.go:35, 311` |
| PKCE | S256, verifier 96바이트 base64url | `pkce.go` |
| 갱신 헤더 | `User-Agent: axios/1.15.2`, `Accept: application/json, text/plain, */*`, JSON 본문 | `applyClaudeOAuthAxiosHeaders` |
| 갱신 동시성 | refresh token별 singleflight, 429 시 Retry-After(5초~5분 clamp) 동안 차단 | `RefreshTokens`, `setClaudeRefreshBlockedUntil` |
| 갱신 재시도 | 3회, 500ms/1s/2s, 명시적 5xx만 재시도(전송 오류는 토큰 소모 가능성 때문에 재시도 안 함) | `RefreshTokensWithRetry`, `isClaudeRefreshRetryable` |
| 갱신 선행 시간 | 4시간 | `sdk/auth/claude.go:RefreshLead`, `sdk/cliproxy/auth/auto_refresh_loop.go:485` |
| 401 처리 | conductor가 한 번 갱신 후 재시도 | `sdk/cliproxy/auth/conductor_direct.go:tryRefreshAfterUnauthorized` |
| 저장 | `ClaudeTokenStorage` (access/refresh/expired/email/account_uuid/organization_*/claude_device_ids/base_url) | `internal/auth/claude/token.go` |
| TLS | OAuth 제어면과 OAuth 요청 모두 표준 `net/http`(uTLS는 API 키 경로만). `internal/auth/claude/utls_transport.go`의 `newUtlsRoundTripper`는 테스트에서만 참조 | `claude_executor.go:httpClient`, `utls_transport.go:225` |

### 1.2 요청 송신 (OAuth 자격증명)

- OAuth 판별: 키에 `sk-ant-oat` 포함 여부 (`claude_executor_request.go:isClaudeOAuthToken`, `claudeCredentialUsesOAuth`).
- URL: `{base}/v1/messages?beta=true`, count_tokens는 `/v1/messages/count_tokens?beta=true`.
- 헤더(`applyClaudeHeadersWithNativeProfile`): `Authorization: Bearer`, `Anthropic-Version: 2023-06-01`, `anthropic-dangerous-direct-browser-access: true`, `x-app: cli`, `X-Stainless-*`(package `0.112.1`, runtime `v26.3.0`), `X-Claude-Code-Session-Id`, 1st-party일 때 `x-client-request-id`, `Accept-Encoding: gzip, deflate, br, zstd`. api.anthropic.com에서는 와이어 대소문자를 맞춘다(`applyClaudeWireHeaderCasing`).
- User-Agent: `claude-cli/2.1.280 (external, cli)` (`helps/claude_device_profile.go:24` `DefaultClaudeCodeVersion = "2.1.280"`). bootstrap 조회는 `claude-code/2.1.177` (`helps/claude_code_identity.go:148`).
- anthropic-beta: 본문 형태에 따라 요청마다 조립(`claudeCodeCLIBetas`, 주석 표 `claude_executor_request.go:132-172`, 29개 위치). 관리 대상 beta는 미확인 클라이언트에서 제거하고, 모르는 beta는 통과(`isManagedClaudeBeta`). 운영자 설정 `anthropic-beta-strip`/`anthropic-beta-append`가 마지막에 적용된다. `helps/claude_code_betas.go:SelectClaudeCodeBetas`는 운영 호출자가 없는 옛 목록이다.
- 위장(cloaking): `resolveClaudeWirePolicy`(기본 auto: OAuth이면 위장). system 첫 블록에 billing header(`cc_version=<ver>.<fp3>; cc_entrypoint=cli; cch=00000;` + `cc_prompt_id`/`cc_turn_*` 등), 그다음 `You are Claude Code, Anthropic's official CLI for Claude.` (`claude_executor_cloaking.go`).
- cch 서명: xxHash64 seed `0x4D659218E32A3268`, `& 0xFFFFF`, 5자리 hex. `max_tokens`·`fallbacks`·`fallback_credit_token` 제외, `model` 문자열을 비움(`claude_signing.go:signAnthropicMessagesBody`). payload 설정 뒤 마지막에 서명한다.
- metadata.user_id: `{"account_uuid","device_id","session_id"}` JSON 문자열. Go map 직렬화라 키가 알파벳 순서다(`helps/claude_code_identity.go:BuildClaudeCodeMetadataUserID`).
- 도구 이름: `mcp_` 접두사 + BIP39 별칭(`remapOAuthToolNamesWithOptions`, 응답 역변환 `reverseRemapOAuthToolNames*`).
- 캐시: `ensureCacheControl`, 4개 제한, OAuth+CLI 프로필은 ttl `1h`로 승격.
- 요청 본문 압축: 없음. Claude 요청 경로에는 `Content-Encoding`이 없다.
- effort/per-turn: `per-turn-control-2026-07-01`는 Opus 5.5·Fable 5.1에만 붙는다(`claudeModelHasPerTurnEffort`).

### 1.3 응답 수신

- 비스트림 `Execute`, 스트림 `ExecuteStream`(`bufio.Scanner` 50MB, `message_stop`이 없으면 `upstream stream closed before message_stop`).
- 응답 해제: `helps/response_decode.go:DecodeResponseBody`(gzip/br/zstd + 매직바이트 판별).
- 오류 분류: `classifyClaudeStatusError`. 429는 unified 5h/7d 거절이면 자격증명 단위 쿨다운, fast-mode 크레딧 거절은 요청 단위, 그 밖은 모델 단위다. 스트림 `overloaded_error` 이벤트는 처리된다(`claude_executor_stream.go:658`).
- Retry-After: 헤더(`helps/claude_ratelimit.go:ParseClaudeRateLimitReset*`, 1~30초 fuzz, 최대 7일+1시간)와 본문 fallback(`claudeBodyRetryAfter`)을 모두 읽는다.
- `anthropic-ratelimit-unified-*` 헤더: status/5h/7d/7d_oi/overage/representative-claim/utilization 등을 파싱한다.

### 1.4 OAuth 경로와 맞물린 포크 기능

모델 alias(`ClaudeExecutor.upstreamModel`), model-level cooling(`claude.model-level-cooling`, Fable 전용 거절 판별 `isOverageOrFableOnlyRejection`), conductor 쿨다운/quota fallback, compaction 별도 호출, tool search 대체, web search 결과 캐시, thinking 서명 replay, 세션 날짜 고정(`resolveClaudeSessionPinnedDate`), diagnostics 주입(`cache-diagnosis-2026-04-07`), claude warmup(관리 API, 와이어 경로 밖), count_tokens 전용 beta 세트. `requestEvents`라는 이름의 기능은 이 커밋에서 찾지 못했다(conductor와 executor 범위 검색).

## 2. 대상 구조 (cortexkit)

| 영역 | 위치 |
|------|------|
| 상수(client_id, URL, scope, 버전, gzip) | `packages/core/src/constants.ts` |
| 로그인/교환/갱신 | `packages/core/src/auth.ts` (`authorize`, `exchangeCode`, `refreshClaudeOAuthToken`) |
| 갱신 일정/동시성 | `packages/core/src/accounts.ts`(선행 240분, 10분 주기, 실패 백오프 5~60분, 비일시 오류 24시간), `native-refresh-coordinator.ts`(계정 잠금, 3회, rejected credential 기준 join) |
| 토큰 목적지 제한 | `packages/core/src/oauth-origin.ts`(`https://api.anthropic.com`만 허용, `redirect: 'error'`) |
| 헤더 | `packages/core/src/claude-code.ts:applyClaudeCodeHeaders` |
| beta 목록 | `packages/core/src/model-capabilities.ts:30-67`, `claude-code.ts:selectClaudeCodeBetas` |
| billing/cch | `packages/core/src/cch.ts` |
| 요청 gzip | `packages/core/src/request-gzip.ts` |
| effort | `packages/core/src/mid-conversation-output-config.ts`, `model-capabilities.ts`(`effortMarkers`) |
| quota 헤더 | `packages/core/src/quota-headers.ts` |
| 오류 분류 | `packages/core/src/provider-error.ts` |
| 같은 계정 401 재시도 | `packages/opencode/src/v2/{adapter,source,prime}.ts` + common-auth `opencode2/install.ts` |
| 와이어 벡터 | `packages/opencode/src/tests/fixtures/claude-oauth-wire/vectors.json` (7개, 재구성 벡터, sha256 `47072a18eb3640dd03502a4f24e7e9b5325160faffacdf47cb99db6462cb4ba5`) |

## 3. 1:1 비교 표

판정: 같음 / 우리만 / 저쪽만 / 의미 다름. 조치: 유지 / 가져옴 / 변형해서 가져옴.

| # | 항목 | 우리 (파일:함수) | cortexkit (파일:함수) | 판정 | 조치 | 회귀 위험 |
|---|------|------------------|-----------------------|------|------|-----------|
| 1 | client_id, authorize/token URL | `anthropic_auth.go:28-32` | `constants.ts:3-13` | 같음 | 유지 | 없음 |
| 2 | redirect URI | 로컬 `http://localhost:54545/callback` | `https://platform.claude.com/oauth/code/callback`(코드 붙여넣기) | 의미 다름 | 유지(서버형 로그인에 로컬 콜백이 필요) | 바꾸면 기존 로그인 UX가 깨짐 |
| 3 | 갱신 scope/헤더/본문 | `refreshTokensSingleFlight` + axios 헤더 | `refreshClaudeOAuthToken` | 같음 | 유지 | 없음 |
| 4 | 갱신 재시도 | 명시적 5xx만, 3회 | 5xx와 일시 네트워크 오류, 2회 재시도 | 의미 다름 | 유지(우리 쪽이 1회용 refresh token 소모를 더 보수적으로 막음) | 전송 오류 재시도를 넣으면 토큰 무효화 위험 |
| 5 | 갱신 선행/주기 | 4h 선행, 실패 백오프는 conductor 공통 | 240분 선행, 10분 주기, 5~60분 백오프, 비일시 24h | 같음(선행)/의미 다름(백오프) | 유지, 비일시 오류 24h 정지는 검토 | conductor 쿨다운과 이중 적용 위험 |
| 6 | 같은 계정 401 재시도 | conductor가 1회 갱신 후 재시도 | `retrySameAccount`(다른 저장소가 더 새 토큰을 가진 경우만, 계정 바뀌면 중단), `b9a8bd928` | 의미 다름 | 변형해서 가져옴: 갱신 전에 파일 저장소의 더 새 토큰을 다시 읽는 단계만 | 파일 watcher와 경합. 테스트 필요 |
| 7 | 토큰 목적지 제한 | 비 1st-party base_url에도 Bearer 전송(사용자 설정 base_url 지원) | api.anthropic.com HTTPS만, redirect 차단(`92fb0b099`) | 우리만/저쪽만 | 변형해서 가져옴: OAuth 토큰 요청은 리다이렉트를 따르지 않게(`CheckRedirect`) | 커스텀 base_url 사용자 경로가 깨지지 않게 허용 목록은 base_url 기준 |
| 8 | Claude Code 버전/UA | `claude-cli/2.1.280 (external, cli)`, bootstrap `claude-code/2.1.177`, antigravity `2.1.177` | `claude-cli/2.1.302 (external, cli)`, bootstrap/quota `claude-code/2.1.302` | 의미 다름 | 가져옴(버전 상수 일괄 갱신 + 버전 불일치 정리) | 버전과 beta·본문 형태가 함께 움직여야 함. 버전만 올리면 지문 불일치 |
| 9 | stainless 패키지 버전 | `0.112.1` | `0.128.0` | 의미 다름 | 가져옴(8과 같은 단계) | 8과 같음 |
| 10 | 신규 헤더 | 없음 | `x-claude-code-request-class: main/auxiliary`, `x-claude-code-prompt-id`(billing `cc_prompt_id`와 같은 값) | 저쪽만 | 가져옴 | 헤더 순서·대소문자 테스트 갱신 필요 |
| 11 | beta 기본 목록 | 요청마다 조립. OAuth 기본값(코드 순서로 유도): `claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07,mid-conversation-tool-changes-2026-07-01,extended-cache-ttl-2025-04-11` | main: `claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,mid-conversation-tool-changes-2026-07-01,advanced-tool-use-2025-11-20,mid-conversation-system-clear-at-2026-08-21,effort-2025-11-24,thinking-binding-controls-2026-08-01,thinking-display-updates-2026-08-18,extended-cache-ttl-2025-04-11,cache-diagnosis-2026-04-07` + title/quota 목록 | 의미 다름 | 변형해서 가져옴: 요청마다 조립하는 우리 구조는 유지하고, 2.1.302 기준 위치 표만 갱신 | 가장 큰 위험. 우리 쪽은 2.1.280 실측 기반이고 저쪽 벡터는 재구성값이라, 실측 캡처 없이 바꾸면 안 됨 |
| 12 | per-turn-control 대상 | Opus 5.5, Fable 5.1 | Fable 5.1, Opus 5.5, Sonnet 5.5, Haiku 5.5 (Haiku 5.5는 `claude-code-20250219` 위치도 다름) | 의미 다름 | 가져옴(11과 함께) | Sonnet/Haiku 5.5 400 가능성. 모델별 테스트 필요 |
| 13 | 메시지 단위 effort | 지시문/옵션 처리(`claude_effort_directive_test.go` 범위) | Opus 5.5·Sonnet 5.5만 메시지 단위, Fable 5.1은 상단 고정(`38627af02`) | 의미 다름 | 변형해서 가져옴(모델 표만 맞춤) | thinking 서명 replay와 상호작용 |
| 14 | billing header | `cc_version=<ver>.<fp3>; cc_entrypoint=cli; cch=00000;` + `cc_prev_req`/`cc_prompt_id`/`cc_turn_*` | 같은 구조, 버전 2.1.302 | 같음(구조) | 버전만 갱신 | 낮음 |
| 15 | cch 서명 | xxHash64 seed 동일, `max_tokens`·`fallbacks`·`fallback_credit_token` 제외, `model` 비움 | 동일 seed, `max_tokens` 삭제, `model=""`, 재직렬화 객체 해시 | 같음(핵심)/의미 다름(제외 필드) | 유지. 저쪽 벡터로 교차 검증 테스트만 추가 | 낮음 |
| 16 | metadata.user_id | JSON 키 알파벳 순서(`account_uuid,device_id,session_id`) | `device_id,account_uuid,session_id` 순서 | 의미 다름 | 가져옴(필드 순서 고정 구조체로) | 낮음. 단 캐시 키 변동 가능 |
| 17 | 요청 본문 gzip | 없음 | 4096자 이상, api.anthropic.com 직결만, 프록시/mTLS/사용자 CA면 생략, cch를 `00000`으로 되돌리고 0~256개 공백 패딩, level 6, 거절 시 비압축 재전송 + 프로세스 latch(프록시 거절이면 7일) | 저쪽만 | 변형해서 가져옴(설정 키로 기본 꺼짐) | 높음: payload 설정이 최종 의미 경계라는 규칙과 cch 재설정 순서가 충돌할 수 있음. 압축은 전송 프레이밍으로만 다뤄야 함 |
| 18 | 본문 필드 순서 | 번역기 출력 순서 그대로 | `orderClaudeCodeBody`(model, messages, system, tools, tool_choice, metadata, max_tokens, temperature, thinking, context_management, output_config, diagnostics, stream, speed) | 저쪽만 | 검토(실측과 비교 후 결정) | cch 값이 바뀜(직렬화 기준이 다르면) |
| 19 | 응답 SSE | 50MB scanner, `message_stop` 필수, 잘림 오류 | 바이트 안전 프레이머, 최대 프레임 초과 시 거부(`3031bd979`) | 의미 다름 | 유지(우리 쪽 잘림 검증이 더 강함) | 없음 |
| 20 | quota 헤더 | unified 전체(5h/7d/7d_oi/overage/claim/utilization) 파싱해 쿨다운에 사용 | 5h/7d utilization·reset, `fallback=available`, representative-claim | 같음(대부분)/우리만(overage, 7d_oi) | 유지. `anthropic-ratelimit-unified-fallback` 해석만 가져옴 | 낮음 |
| 21 | 오류 분류 | 429 범위(자격증명/모델/요청) 구분, overloaded 스트림 처리 | `provider-error.ts` 재시도 가능 분류, 거절 복구 백오프 | 의미 다름 | 유지 | 없음 |
| 22 | 사용자 지정 헤더 보호 | `_CustomHeadersCannotOverrideAnthropicIdentity` 테스트로 보호 | `PROTECTED_HEADERS`에 `content-encoding` 포함 | 같음(대부분) | gzip 도입 시 `content-encoding`을 보호 목록에 추가 | 낮음 |
| 23 | count_tokens/title/quota beta | count_tokens 고정 세트 | title/quota 목록 별도 | 의미 다름 | title·quota 목록은 해당 요청을 우리도 보낼 때만 가져옴 | 낮음 |
| 24 | 와이어 벡터 테스트 | Go 단위 테스트에 분산 | vectors.json 7개(재구성) | 저쪽만 | 가져옴(테스트 픽스처로, 기대값 일부 실측 확인 후) | 없음(테스트 전용) |
| 25 | 포크 기능(alias, cooling, compaction, replay, 날짜 고정, warmup) | 여러 파일 | 해당 없음 | 우리만 | 유지 | 이식 단계마다 기존 테스트로 보호 |

## 4. 단계별 마이그레이션 계획

모든 단계는 별도 PR로 진행하고, 각 단계에서 `go test ./internal/runtime/executor/... ./internal/auth/claude/ ./sdk/auth/ ./sdk/cliproxy/auth/`가 통과해야 한다. payload 설정이 마지막 의미 경계라는 규칙(AGENTS.md)을 모든 단계에서 지킨다.

### 단계 0: 실측 기준 확보 (코드 변경 없음)
- Claude Code 2.1.302 실제 캡처(main/title/quota/count_tokens, Opus 5.5·Sonnet 5.5·Haiku 5.5·Fable 5.1, gzip 켬/끔)를 확보한다. cortexkit `scripts/capture-with-mitmproxy.sh` 참고.
- cortexkit `vectors.json`과 실측을 비교해 재구성값 중 틀린 부분을 기록한다.
- 롤백: 해당 없음.

### 단계 1: 와이어 벡터 테스트 도입 (테스트만)
- 파일: `internal/runtime/executor/testdata/claude_oauth_wire/*.json`, `internal/runtime/executor/claude_oauth_wire_vectors_test.go`(신규).
- 내용: 현재 2.1.280 동작을 고정하는 벡터와, 단계 0 실측 2.1.302 벡터(처음에는 skip이 아닌 별도 build tag나 기대값 분리로 관리)를 둔다. cch 교차 검증 포함.
- 롤백: 테스트 파일 삭제.

### 단계 2: 버전 상수와 헤더 (표 8, 9, 10, 14, 16)
- 파일: `helps/claude_device_profile.go`(버전, stainless), `helps/claude_code_identity.go`(bootstrap UA, user_id 필드 순서), `internal/antigravity/signature.go`(2.1.177, 별도 확인 후), `claude_executor_request.go`(request-class, prompt-id 헤더, 와이어 대소문자 표), `claude_executor_cloaking.go`(billing 버전).
- 예상 테스트: `claude_executor_wire_casing_test.go`, `claude_executor_native_helper_test.go`, `claude_cloaked_cache_repro_test.go`, `helps/claude_device_profile_test.go`, `helps/claude_code_identity` 관련 테스트 갱신.
- 롤백: 버전 상수 하나로 되돌릴 수 있게 상수를 한곳에 모은다. 설정 `claude-header-defaults`로 운영 중 덮어쓰기 가능.

### 단계 3: beta 위치 표와 per-turn/effort (표 11, 12, 13, 23)
- 파일: `claude_executor_request.go`(`claudeCodeCLIBetas`, `claudeModelHasPerTurnEffort`, 주석 표), effort 처리 파일.
- 예상 테스트: `claude_executor_beta_policy_test.go`, `claude_executor_beta_passthrough_test.go`, `claude_effort_directive_test.go`, `helps/claude_code_betas_test.go`. 옛 `SelectClaudeCodeBetas`는 호출자가 없으므로 제거 후보로 별도 커밋.
- 롤백: 단계 2와 묶어서 되돌림(버전과 beta는 함께 움직임).

### 단계 4: 토큰 목적지·리다이렉트 보호와 같은 계정 401 재읽기 (표 6, 7)
- 파일: `internal/auth/claude/anthropic_auth.go`(OAuth HTTP 클라이언트 `CheckRedirect` 차단), `sdk/cliproxy/auth/conductor_direct.go`(갱신 전 저장소의 더 새 토큰 재확인).
- 예상 테스트: `anthropic_auth_test.go`, `conductor_issue6742_test.go` 계열, `TestManagerSetsProviderAuthContextForMixedExecutionPaths`.
- 롤백: 단독 PR이라 revert 한 번.

### 단계 5: 요청 본문 gzip (표 17, 22) - 설정으로 기본 꺼짐
- 파일: `internal/runtime/executor/helps/claude_request_gzip.go`(신규, 판정·인코딩·거절 분류·latch), `claude_executor_execute.go`/`claude_executor_stream.go`/`claude_executor_tokens.go` 전송 직전 호출, `internal/config` 설정 키.
- 순서: payload 설정 적용 → cch 서명(비압축 본문) → gzip 판단 시 cch를 `00000`으로 되돌리고 패딩 후 압축(전송 프레이밍) → 거절 시 서명된 비압축 본문 재전송. 로그·사용량·재시도에는 비압축 본문을 유지한다.
- 예상 테스트: 판정 표(호스트, 크기, 프록시, 설정), 거절 분류(`req_`/`cf-ray` 유무, invalid JSON), 재전송 1회, latch, payload 설정 이후 순서 회귀 테스트.
- 롤백: 설정 끄기(즉시), 코드 revert.

### 단계 6: 정리
- 사용되지 않는 `internal/auth/claude/utls_transport.go`의 `newUtlsRoundTripper`와 낡은 "Firefox TLS" 주석 정리 여부 결정.
- `expires_in`이 없을 때 `Expire = now`가 되어 즉시 재갱신되는 동작 확인.

## 5. 위험과 포크 기능 충돌

- 버전·beta·본문 형태는 한 묶음이다. 2.1.302 버전만 올리고 beta 표를 2.1.280으로 두면, 실제 Claude Code에 없는 조합이 돼서 지문이 더 나빠진다. 단계 2와 3은 같은 릴리스로 내보낸다.
- cortexkit 벡터는 `source: "reconstructed"`이다. 우리 쪽 2.1.280 표는 실측 기반이라 실측 없이 저쪽 값으로 덮으면 퇴행할 수 있다.
- gzip은 payload 설정 최종 경계 규칙, cch 서명, 요청 로그(비압축 본문 기록), 재시도/fallback 경로(conductor가 같은 본문을 재사용)와 모두 맞물린다. 압축은 전송 직전 바이트만 바꾸는 것으로 제한해야 한다.
- per-turn-control을 Sonnet/Haiku 5.5로 넓히면 model-level cooling과 Fable 전용 거절 판별(`isOverageOrFableOnlyRejection`)의 모델 범위와 함께 검토해야 한다.
- metadata.user_id 필드 순서나 본문 필드 순서를 바꾸면 cch 값과 프롬프트 캐시 키가 바뀐다. 배포 직후 캐시 적중률이 일시 하락할 수 있다.
- 토큰 목적지 제한을 cortexkit처럼 api.anthropic.com 고정으로 하면, 사용자 지정 base_url(`ClaudeTokenStorage.base_url`)로 OAuth 토큰을 보내는 포크 사용 사례가 깨진다. 리다이렉트 차단만 가져온다.
- claude-lb-cpa(로컬 origin)는 이 저장소와 갈라져 있어서, 같은 이식을 두 저장소에 하면 충돌 해소가 두 번 필요하다.

## 6. 결정 필요 항목

1. 이식 대상 저장소: CLIProxyAPIPlus만 할지, claude-lb-cpa에도 할지(또는 claude-lb-cpa만 할지).
2. 2.1.302 실측 캡처를 먼저 확보할지(권장), cortexkit 재구성 벡터를 기준으로 바로 진행할지.
3. 요청 본문 gzip: 가져오지 않음 / 설정으로 기본 꺼짐(권장) / 기본 켜짐.
4. 같은 계정 401 처리: 현재 유지 / 갱신 전 저장소 재읽기만 추가(권장).

## 7. 검증 방법 (이 문서 PR)

이 PR은 문서만 추가하므로 동작 변화가 없다. 확인 명령:

```bash
GOTOOLCHAIN=auto go build ./...
GOTOOLCHAIN=auto go test ./internal/auth/claude/ ./internal/runtime/executor/... ./sdk/auth/
```
