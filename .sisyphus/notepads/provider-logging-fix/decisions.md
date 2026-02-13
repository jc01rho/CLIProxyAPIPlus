# Decisions - Provider Logging Fix

## 2026-01-30: gin_logger에서 context 저장

### Decision
`gin_logger.go`에서 request context에 gin.Context를 저장하기로 결정.

### Alternatives Considered

1. **SetProviderAuthInContext 시그니처 변경**
   - `*gin.Context`를 직접 파라미터로 받도록 변경
   - 장점: 명시적
   - 단점: 호출 위치 6군데 수정 필요, 함수 시그니처 변경

2. **gin_logger에서 context 저장** (선택됨)
   - 장점: 1줄 코드 추가만 필요, 기존 패턴과 일치
   - 단점: 암시적 의존성

### Rationale
- 기존 `handlers.go:269`에서 동일한 패턴 사용
- 최소한의 코드 변경
- 이미 SetProviderAuthInContext가 `ctx.Value("gin")`를 기대하고 있음
