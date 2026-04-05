# Antigravity Primary Handoff 구현 계획 (v2 — 병합)

## 목표
Antigravity provider에 대해 **단일 primary credential**만 활성 상태로 유지하고,  
primary에서 특정 오류(401/403/429, provider auth/quota 오류, 재시도 가능한 5xx) 발생 시  
자동으로 다음 credential로 primary를 교체하는 **handoff 정책**을 구현한다.  
정기 quota check도 primary credential에 대해서만 실행하도록 제한한다.

## 핵심 설계 원칙
1. **Request-time fallback 아님** — 요청 시점에 여러 credential을 순회하는 것이 아니라, primary 하나만 사용하고 실패 시 primary 자체를 교체
2. **기존 Disabled 필드 재사용** — `Auth.Disabled` 필드를 이용하여 비-primary credential을 비활성화
3. **최소 변경** — 기존 conductor.go의 선택/결과반영 로직을 antigravity 전용 분기로 확장
4. **교체 사유 한정** — 401, 403, 429, provider-specific auth/quota 오류, 502/503/504만 교체 트리거

## 교체 트리거 HTTP 상태 코드
| 코드 | 분류 | 교체 여부 |
|------|------|-----------|
| 401 | Unauthorized | ✅ primary 교체 |
| 403 | Forbidden / quota | ✅ primary 교체 |
| 429 | Rate limit / quota exceeded | ✅ primary 교체 |
| 502 | Bad Gateway | ✅ primary 교체 (retryable 5xx) |
| 503 | Service Unavailable | ✅ primary 교체 (retryable 5xx) |
| 504 | Gateway Timeout | ✅ primary 교체 (retryable 5xx) |
| 400, 404, 408, 500 | 기타 | ❌ 교체 안 함 |

## 수정 대상 파일 및 변경 내용

### 1. `sdk/cliproxy/auth/types.go`
- **추가**: `Auth` 구조체에 `PrimaryOrder int` 필드 (antigravity credential 간 순서 지정)
- **추가**: antigravity primary handoff 판정용 헬퍼 상수/함수

### 2. `sdk/cliproxy/auth/conductor.go` (핵심)
- **추가**: `isAntigravityHandoffError(result Result) bool` — 교체 트리거 판정 함수
  - 401, 403, 429, 502, 503, 504 + provider-specific auth/quota 오류 메시지 패턴 매칭
- **수정**: `MarkResult()` 내부에 antigravity 전용 분기 추가
  - 교체 트리거 조건 충족 시 → `promoteNextAntigravityPrimary()` 호출
- **추가**: `promoteNextAntigravityPrimary(currentAuthID string)` 메서드
  - 현재 primary를 `Disabled=true`로 설정
  - PrimaryOrder 기준으로 다음 enabled 가능한 credential을 `Disabled=false`로 승격
  - 모든 credential이 소진되면 첫 번째로 순환(wrap-around)
  - persist 호출하여 상태 저장
- **수정**: `pickNext()` / `pickNextMixed()` — 기존 `Disabled` 필터링이 이미 있으므로 추가 변경 최소화
  - antigravity provider일 때 disabled credential을 자동 스킵하는 동작은 기존 로직으로 충분

### 3. `sdk/cliproxy/auth/conductor.go` — Quota Check 제한
- **수정**: 정기 quota check 루프(goroutine)에서 antigravity provider인 경우 `Disabled == false`인 credential(= primary)만 체크하도록 필터 추가
- 현재 quota check 진입점 확인 필요: `startQuotaChecker` 또는 유사 함수

### 4. `internal/runtime/executor/antigravity_executor.go`
- **변경 없음 예상** — 에러는 이미 `statusErr`로 올바르게 전파되며, conductor의 `MarkResult`가 상태코드를 읽음
- 필요시: `newAntigravityStatusErr` 반환값에 provider-specific 오류 메시지 파싱 보강

### 5. `internal/api/handlers/management/auth_files.go`
- **수정**: `RequestAntigravityToken()` — 새 credential 등록 시 기존 antigravity credential이 있으면 새것을 `Disabled=true`로 저장 (primary는 기존 것 유지)
- **수정**: `PatchAuthFileStatus()` — antigravity provider에 대해 enable 시 다른 antigravity credential을 자동 disable (단일 primary 보장)

### 6. `sdk/cliproxy/auth/conductor.go` — 초기화
- **수정**: `LoadAuths()` 또는 초기 로드 시 antigravity credential 중 enabled가 0개이면 첫 번째를 자동 enable

## 구현 순서

```
Phase 1: 타입 확장
  1-1. Auth 구조체에 PrimaryOrder 필드 추가 (types.go)
  1-2. isAntigravityHandoffError() 헬퍼 추가 (conductor.go)

Phase 2: Primary Handoff 핵심 로직
  2-1. promoteNextAntigravityPrimary() 구현 (conductor.go)
  2-2. MarkResult()에 antigravity 분기 추가 (conductor.go)
  2-3. 초기 로드 시 primary 보장 로직 (conductor.go)

Phase 3: Quota Check 제한
  3-1. quota check 루프에서 antigravity disabled 스킵 필터 추가

Phase 4: Management API 연동
  4-1. RequestAntigravityToken() — 신규 등록 시 auto-disable
  4-2. PatchAuthFileStatus() — antigravity 단일 enable 보장

Phase 5: 검증
  5-1. 단위 테스트: isAntigravityHandoffError
  5-2. 단위 테스트: promoteNextAntigravityPrimary (순환, wrap-around, 빈 목록)
  5-3. 단위 테스트: MarkResult antigravity 분기
  5-4. 통합: 빌드 확인, lsp_diagnostics 클린
```

## 테스트 포인트

| # | 테스트 | 검증 내용 |
|---|--------|-----------|
| T1 | `isAntigravityHandoffError` | 401/403/429/502/503/504 → true, 400/404/500 → false |
| T2 | `promoteNextAntigravityPrimary` | 3개 credential 중 1번 실패 → 2번 승격, 3번은 disabled 유지 |
| T3 | `promoteNextAntigravityPrimary` wrap-around | 마지막 credential 실패 → 첫 번째로 순환 |
| T4 | `MarkResult` antigravity 분기 | 401 결과 시 handoff 트리거 확인 |
| T5 | Quota check 필터 | antigravity disabled credential이 quota check에서 제외 |
| T6 | 신규 등록 auto-disable | 기존 primary 있을 때 새 credential은 disabled로 저장 |
| T7 | PatchAuthFileStatus 단일성 | antigravity enable 시 다른 것 자동 disable |

## 위험/고려사항
- **동시성**: `promoteNextAntigravityPrimary`는 `m.mu.Lock()` 내부에서 호출되어야 함 (MarkResult 패턴 따름)
- **Persist 실패**: handoff 중 persist 실패 시 in-memory 상태만 변경되고 재시작 시 이전 상태로 복원 → 허용 가능
- **모든 credential 실패**: 전부 교체 실패 시 wrap-around 후 첫 credential 재시도 → 무한 순환 방지를 위해 cooldown 필요
- **기존 credits fallback과의 관계**: credits fallback은 executor 내부 로직, primary handoff는 conductor 레벨 → 독립적으로 동작

## 의존성
- 기존 `Auth.Disabled` 필드와 `pickNext`의 disabled 필터링에 의존
- `MarkResult`의 기존 에러 분류 로직(statusCode 추출)에 의존
- `persist()` 메서드로 상태 영속화
