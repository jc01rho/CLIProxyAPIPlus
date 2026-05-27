## 2025-01-29: Context Key 설계 결정

### 결정: 문자열 기반 Context Key 사용

**배경**:
- conductor.go (sdk/cliproxy/auth)와 gin_logger.go (internal/logging) 간 데이터 전달 필요
- Import cycle 방지 필수

**고려한 옵션**:

#### Option 1: 타입 기반 Context Key (초기 시도)
```go
type providerAuthContextKey struct{}
```
**장점**: 타입 안전성
**단점**: 패키지 간 공유 불가 (다른 타입으로 인식)
**결과**: ❌ 채택 안 함

#### Option 2: 문자열 기반 Context Key (최종 선택)
```go
const providerAuthContextKey = "cliproxy.provider_auth"
```
**장점**: 
- 패키지 간 공유 가능
- Import cycle 없음
- 간단하고 명확

**단점**: 
- 타입 안전성 낮음 (컴파일 타임 체크 불가)
- 문자열 오타 가능성

**결과**: ✅ 채택

### 근거

1. **Go Context 패턴**: 
   - 공식 문서에서도 문자열 key 사용 권장 (패키지 간 공유 시)
   - 타입 기반은 같은 패키지 내에서만 유효

2. **Import Cycle 방지**:
   - `internal/logging` → `sdk/cliproxy/auth` import 불가
   - 문자열 상수는 각 패키지에서 독립적으로 정의 가능

3. **실용성**:
   - 로깅 목적의 메타데이터 전달
   - 타입 안전성보다 유연성이 중요

### 네이밍 규칙

**선택한 이름**: `"cliproxy.provider_auth"`

**이유**:
- 프로젝트 네임스페이스 포함 (`cliproxy.`)
- 명확한 의미 (`provider_auth`)
- 충돌 가능성 최소화

### 대안 고려

**Option A**: 공통 패키지 생성
```
common/
  └── contextkeys/
      └── keys.go
```
**평가**: 오버엔지니어링, 단일 key를 위해 패키지 추가 불필요

**Option B**: Gin context에 직접 저장
```go
c.Set("provider_auth", ...)
```
**평가**: Request context와 분리됨, executor에서 접근 불가

### 결론

문자열 기반 context key가 이 상황에서 최적의 선택:
- ✅ 간단하고 명확
- ✅ Import cycle 없음
- ✅ 패키지 간 공유 가능
- ✅ Go 커뮤니티 관례 준수
