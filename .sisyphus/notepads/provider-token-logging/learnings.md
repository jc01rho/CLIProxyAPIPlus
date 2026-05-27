# Learnings - Provider Token Logging

## 2026-04-05: Usage Detail Context Storage Implementation

### Implementation
`UsageReporter.publishWithOutcome`에서 `storeUsageDetailInContext` helper를 호출하여 usage detail을 gin context에 저장.

### Key Pattern
- `ctx.Value("gin")`으로 gin.Context 추출
- `ginCtx.Set("usageDetail", detail)`로 저장
- 저장 키: `"usageDetail"` (충돌 가능성 낮음, 의미 명확)

### Files Modified
- `CLIProxyAPIPlus/internal/runtime/executor/helps/usage_helpers.go`
  - `storeUsageDetailInContext()` helper 추가 (11줄)
  - `publishWithOutcome()` 수정 (1줄 추가)
- `CLIProxyAPIPlus/internal/runtime/executor/helps/usage_helpers_test.go`
  - `TestStoreUsageDetailInContext()` 테스트 추가 (41줄)
  - import 추가 (context, gin)

### Test Results
```
PASS: TestStoreUsageDetailInContext
- gin context에 usage detail 저장 검증
- InputTokens, OutputTokens, TotalTokens, CachedTokens, ReasoningTokens 모두 확인
```

### Next Task
gin_logger.go에서 `ctx.Value("gin")`으로 gin context를 가져온 후 `c.Get("usageDetail")`로 저장된 usage detail을 읽어 access log에 append.

## 2026-04-05: Token Segment Logging in Access Log

### Implementation
`gin_logger.go`에서 usage detail을 읽어 access log에 token segment 추가.

### Key Changes
- `getUsageDetailFromContext()` helper 추가: gin context에서 `usageDetail` 키로 저장된 usage.Detail 추출
- Token segment append 로직: AI API 경로에서만, InputTokens 또는 OutputTokens > 0일 때만 `tokens in=X out=Y` 형식으로 추가
- 기존 로그 포맷 유지: provider/model/auth 정보 뒤, error message 앞에 token segment 삽입

### Files Modified
- `CLIProxyAPIPlus/internal/logging/gin_logger.go`
  - import 추가: `github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage`
  - `getUsageDetailFromContext()` 함수 추가 (11줄)
  - `GinLogrusLogger()` 내부에 token segment append 로직 추가 (9줄)
- `CLIProxyAPIPlus/internal/logging/gin_logger_test.go`
  - import 추가: `bytes`, `usage`, `log`
  - `TestGinLogrusLoggerAppendsTokenSegment()` 테스트 추가 (30줄)
  - `TestGinLogrusLoggerSkipsZeroTokens()` 테스트 추가 (30줄)

### Test Results
```
PASS: TestGinLogrusLoggerAppendsTokenSegment
- 123 input tokens, 456 output tokens 저장 시 "tokens in=123 out=456" 로그 확인

PASS: TestGinLogrusLoggerSkipsZeroTokens
- 0 input tokens, 0 output tokens 저장 시 token segment 미추가 확인
```

### Log Format Example
Before: `200 | 23.559s | 127.0.0.1 | POST "/v1/messages" | claude-opus-4-6 | claude:rkjnice@gmail.com`
After: `200 | 23.559s | 127.0.0.1 | POST "/v1/messages" | claude-opus-4-6 | claude:rkjnice@gmail.com | tokens in=123 out=456`

### Pattern
- Token segment은 기존 provider/model/auth 정보 뒤에 append
- 값이 모두 0이면 segment 미추가 (로그 가독성 유지)
- `/v1/messages` 등 AI API 경로에만 적용
- Non-AI 경로는 영향 없음

### Next Task
전체 빌드/테스트 검증 및 최종 정리

## 2026-04-05: Type Mismatch Fix - Value vs Pointer Type

### Problem
`usage_helpers.go`는 `ginCtx.Set("usageDetail", detail)`로 **값 타입** `usage.Detail`을 저장하지만, `gin_logger.go`의 `getUsageDetailFromContext()`는 **포인터 타입** `*usage.Detail`만 읽으려고 해서 런타임에 토큰 segment가 붙지 않음.

### Solution
`getUsageDetailFromContext()` 함수를 값 타입과 포인터 타입 둘 다 지원하도록 수정:
```go
func getUsageDetailFromContext(c *gin.Context) *usage.Detail {
	if c == nil {
		return nil
	}

	if v, exists := c.Get("usageDetail"); exists {
		if detail, ok := v.(*usage.Detail); ok {
			return detail
		}
		if detail, ok := v.(usage.Detail); ok {
			return &detail
		}
	}
	return nil
}
```

### Test Update
테스트를 실제 저장 방식과 일치하도록 수정:
- Before: `c.Set("usageDetail", &usage.Detail{...})` (포인터)
- After: `c.Set("usageDetail", usage.Detail{...})` (값 타입)

### Files Modified
- `CLIProxyAPIPlus/internal/logging/gin_logger.go`: `getUsageDetailFromContext()` 수정 (3줄 추가)
- `CLIProxyAPIPlus/internal/logging/gin_logger_test.go`: 테스트 2개 수정 (포인터 제거)

### Test Results
```
PASS: TestGinLogrusLoggerAppendsTokenSegment
- 값 타입 저장 시 "tokens in=123 out=456" 로그 확인

PASS: TestGinLogrusLoggerSkipsZeroTokens
- 값 타입 저장 시 zero token segment 미추가 확인
```

### Key Insight
- Go의 type assertion은 정확한 타입 매칭 필요
- gin.Context.Set()은 interface{} 저장하므로 런타임에 타입 확인 필수
- 값 타입과 포인터 타입 둘 다 지원하면 호환성 향상

## 2026-04-06: Task 3 Validation - Pointer Type Test Coverage

### Task Objective
변경 경로를 검증하는 테스트 추가 및 백엔드 빌드/테스트 통과 확인

### Analysis
기존 테스트 범위 검토:
- `TestGinLogrusLoggerAppendsTokenSegment`: 값 타입 저장 검증 ✓
- `TestGinLogrusLoggerSkipsZeroTokens`: 값 타입 zero token 검증 ✓
- `TestStoreUsageDetailInContext`: usage detail 저장 검증 ✓

부족한 부분:
- `gin_logger.go`의 `getUsageDetailFromContext()`는 포인터/값 타입 둘 다 지원하지만, 포인터 타입 저장 경로에 대한 테스트 없음

### Implementation
`CLIProxyAPIPlus/internal/logging/gin_logger_test.go`에 포인터 타입 테스트 추가:
- `TestGinLogrusLoggerAppendsTokenSegmentPointerType()`: 포인터 타입 저장 시 token segment 로깅 검증
  - `&usage.Detail{InputTokens: 789, OutputTokens: 321}` 저장
  - "tokens in=789 out=321" 로그 확인

### Verification Results
```
go test ./internal/runtime/executor/helps/... ./internal/logging/... -v
✓ TestParseOpenAIUsageChatCompletions
✓ TestParseOpenAIUsageResponses
✓ TestUsageReporterBuildRecordIncludesLatency
✓ TestStoreUsageDetailInContext
✓ TestGinLogrusRecoveryRepanicsErrAbortHandler
✓ TestGinLogrusRecoveryHandlesRegularPanic
✓ TestGinLogrusLoggerAppendsTokenSegment
✓ TestGinLogrusLoggerSkipsZeroTokens
✓ TestGinLogrusLoggerAppendsTokenSegmentPointerType (NEW)
✓ TestEnforceLogDirSizeLimitDeletesOldest
✓ TestEnforceLogDirSizeLimitSkipsProtected
PASS ok github.com/router-for-me/CLIProxyAPI/v6/internal/logging 0.009s

go build ./cmd/server
✓ Build successful
```

### Conclusion
- 기존 테스트가 값 타입 경로만 검증했으므로 포인터 타입 경로 테스트 추가 필요
- 포인터 타입 테스트 추가로 `getUsageDetailFromContext()` 양쪽 타입 지원 경로 모두 검증
- 모든 테스트/빌드 통과 확인
- Task 3 완료: 변경 경로 검증 완료, 백엔드 빌드/테스트 통과 확인

### Files Modified
- `CLIProxyAPIPlus/internal/logging/gin_logger_test.go`: 포인터 타입 테스트 추가 (30줄)

### Next Phase
Final Verification Wave 진행 가능
