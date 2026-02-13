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
