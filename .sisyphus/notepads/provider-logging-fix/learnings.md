# Learnings - Provider Logging Fix

## 2026-01-30: Context Propagation Pattern

### Problem
`SetProviderAuthInContext` (conductor.go)에서 `ctx.Value("gin")`으로 gin.Context를 가져오려 했으나, 해당 시점에 context에 gin.Context가 저장되어 있지 않아 동작하지 않음.

### Root Cause
- `handlers.go:269`에서 `context.WithValue(newCtx, "gin", c)`가 호출되지만, 이건 handler 진입 시점
- `gin_logger.go` middleware에서는 request context에 request ID만 저장하고 있었음
- conductor.go는 handler를 통해 호출되므로, gin_logger보다 나중에 실행됨

### Solution
`gin_logger.go`의 `GinLogrusLogger` 함수에서 request context에 gin.Context를 저장:

```go
if isAIAPIPath(path) {
    requestID = GenerateRequestID()
    SetGinRequestID(c, requestID)
    ctx := WithRequestID(c.Request.Context(), requestID)
    ctx = context.WithValue(ctx, "gin", c)  // 추가된 라인
    c.Request = c.Request.WithContext(ctx)
}
```

### Key Insight
- Go context는 immutable이므로, `context.WithValue`로 새 context를 만들어야 함
- `c.Request.WithContext(ctx)`로 request에 새 context를 설정해야 이후 코드에서 접근 가능
- gin.Context를 context에 저장하면, 이후 `SetProviderAuthInContext`에서 `ctx.Value("gin")`으로 가져와서 `c.Set()`을 호출할 수 있음

### Pattern
이 패턴은 이미 `sdk/api/handlers/handlers.go:269`에서 사용되고 있었음:
```go
newCtx = context.WithValue(newCtx, "gin", c)
```

middleware에서도 동일한 패턴을 적용함.

### Commit
- `ce653ee`: `fix(logging): add gin.Context to request context for provider auth propagation`

---

## 2026-01-30: Verification Results

### Automated Testing
서버를 실행하고 실제 API 요청을 보내서 로그 형식을 확인함.

### Evidence
```
[2026-01-30 02:35:41] [64242458] [gin_logger.go:183] 200 | POST "/v1/chat/completions" | glm-4.7 | nvidia:nvidia
[2026-01-30 02:36:44] [6142a4c2] [gin_logger.go:183] 200 | POST "/v1/chat/completions" | glm-4.7 | z.ai:z.ai
```

### Before vs After
- **Before**: `glm-4.7 | (opencode-D3h3drck6)` (proxy access key)
- **After**: `glm-4.7 | nvidia:nvidia` (provider:auth-label) ✅

### Verification Method
1. `go build ./cmd/server` - 새 바이너리 빌드
2. `./cliproxy -config config.yaml` - 서버 실행
3. `curl` 요청 전송
4. `logs/main.log` 확인

## credential_stats_sync.py: config:<provider>[...] 패턴 파싱 추가 (2026-03-05)

### 문제
CLIProxyAPIPlus에서 openai-compatibility 계정의 `source` 필드가 `config:z.ai[abcd]`, `config:alibaba[...]` 형식으로 전달되는데, 기존 fallback 로직이 이를 처리하지 못해 provider가 `Unknown` 또는 `API Key`로 표시됨.

### 해결
`resolve_credential()` 함수의 fallback 분기에 `config:<provider>[...]` 패턴 파싱 로직 추가:
- 정규식 `^config:([^\[\]\s]+)\[`로 provider 추출
- `=` 검사 전에 우선 처리하여 기존 로직과 충돌 방지
- provider를 소문자로 정규화 (frontend `getProviderDisplay()`와 호환)

### 구현 위치
- 파일: `CLIProxyAPI-Dashboard/collector/credential_stats_sync.py`
- 함수: `resolve_credential()` (341~385라인)
- 변경: 359~381라인 fallback 로직에 config 패턴 분기 추가

### 검증
- `python3 -m py_compile` 통과
- LSP diagnostics 새 에러 없음
- 기존 fallback 순서 유지 (aizasy, .json, @, =, 길이>40)

### 영향 범위
- `CredentialStatsSync.aggregate_stats()` → `resolve_credential()` 경로
- 최종적으로 `credential_stats[].provider` 필드에 반영
- Frontend `CredentialStatsCard.jsx`의 provider 표시 정확도 향상
