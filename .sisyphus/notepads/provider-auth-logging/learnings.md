## 2025-01-29: Provider Auth 정보를 Gin 로그에 표시 구현

### 구현 완료
- conductor.go에 context key 및 helper 함수 추가
- 6개 실행 함수에서 auth 선택 후 context에 provider auth 저장
- gin_logger.go에서 context에서 정보 읽어 로그에 표시
- 로그 형식: `model | provider:auth-label`

### 핵심 학습: Go Context Key는 타입 기반!

**문제 발견**:
- 처음에 `type providerAuthContextKey struct{}`로 정의
- conductor.go와 gin_logger.go에서 각각 정의
- 같은 이름이지만 **다른 패키지의 타입은 서로 다른 타입**
- Context에서 값을 찾을 수 없음!

**해결책**:
```go
// 문자열 상수 사용 (두 파일 모두 동일)
const providerAuthContextKey = "cliproxy.provider_auth"
```

### Go Context Key 패턴

#### ❌ 잘못된 방법 (타입 기반, 패키지 간 공유 불가)
```go
// package A
type myKey struct{}
ctx = context.WithValue(ctx, myKey{}, "value")

// package B
type myKey struct{}  // 다른 타입!
v := ctx.Value(myKey{})  // nil 반환
```

#### ✅ 올바른 방법 (문자열 기반, 패키지 간 공유 가능)
```go
// package A
const myKey = "my.context.key"
ctx = context.WithValue(ctx, myKey, "value")

// package B
const myKey = "my.context.key"  // 동일한 문자열
v := ctx.Value(myKey)  // "value" 반환
```

### Import Cycle 방지 전략

**상황**: `internal/logging`에서 `sdk/cliproxy/auth`의 타입 필요

**시도한 방법들**:
1. ❌ 직접 import → Import cycle 발생
2. ✅ Context key를 문자열로 정의하여 각 패키지에서 복제

**교훈**: 
- Go에서 패키지 간 순환 참조는 불가능
- Context를 통한 데이터 전달 시 문자열 key 사용이 안전
- 타입 안전성은 떨어지지만 유연성 증가

### 로그 형식 설계

**요구사항**: 
- 기존: `model (apiKey)` - Proxy 접근키
- 원하는: `model | provider:auth-label` - 실제 백엔드 인증 정보

**구현**:
```go
if modelName != "" && providerInfo != "" {
    logLine = logLine + " | " + fmt.Sprintf("%s | %s", modelName, providerInfo)
}
```

**하위 호환성**:
- provider 정보가 없으면 기존처럼 apiKey 표시
- 점진적 마이그레이션 가능

### 수정한 함수들 (6개)
1. `executeMixedOnce()` - 일반 실행
2. `executeCountMixedOnce()` - 토큰 카운트
3. `executeStreamMixedOnce()` - 스트리밍 실행
4. `executeWithProvider()` - 단일 프로바이더 실행
5. `executeCountWithProvider()` - 단일 프로바이더 토큰 카운트
6. `executeStreamWithProvider()` - 단일 프로바이더 스트리밍

**패턴**:
```go
execCtx = SetProviderAuthInContext(execCtx, provider, auth.ID, auth.Label)
```

### 테스트 전략
- 단위 테스트: Context key 동작 확인 (문자열 기반)
- 통합 테스트: 실제 요청 시 로그 출력 확인
