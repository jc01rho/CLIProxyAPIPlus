# Decisions

## 2026-04-05 T1 Implementation

### MinTokens 음수 정규화 방식
- **결정**: 음수 `MinTokens`는 0으로 정규화 (제거하지 않음)
- **이유**: 사용자가 범위 지정 의도로 입력했을 가능성이 높고, 0부터 시작은 유효한 범위
- **대안**: invalid로 제거 → 과도한 strictness로 사용자 경험 저하

### MinTokens > MaxTokens 처리
- **결정**: invalid로 규칙 전체 제거
- **이유**: 논리적 불가능한 범위는 설정 오류
- **근거**: `MaxTokens <= 0` 제거와 일관성 유지

### 필드 순서
- **결정**: `ModelPattern` → `MinTokens` → `MaxTokens` → `BillingClass` → `Enabled`
- **이유**: 시각적 흐름: 대상 → 범위 시작 → 범위 끝 → 결과 → 상태

## 2026-04-05 T1 스펙 보정

### MaxTokens 필수 → 선택으로 변경
- **결정**: `MaxTokens` 태그를 `omitempty`로 변경, 둘 중 하나만 있어도 유효
- **이유**: lower-only rule (`min-tokens: 2001`) 표현을 위해 필수 해제 필요
- **세 가지 형태**: upper-only, lower-only, bounded 모두 지원

### 둘 다 없는 규칙 처리
- **결정**: `MinTokens == 0 && MaxTokens == 0`이면 invalid로 제거
- **이유**: 범위 지정이 전혀 없으면 라우팅 규칙으로서 의미 없음

### MaxTokens 음수 처리
- **결정**: `MaxTokens < 0`은 0으로 정규화 후 "둘 다 없음" 검사에서 제거
- **이유**: `MinTokens < 0` 처리와 일관성 유지

## 2026-04-06 T2 런타임 적용

### threshold 런타임 구현 범위
- **결정**: `sdk/cliproxy/auth/conductor.go`와 해당 런타임 테스트만 수정
- **이유**: T2 목표는 런타임 해석/선택 경로 보정이며 설정/API 레이어 재수정은 범위 밖

### billing class 정규화 위치
- **결정**: 런타임 전용 helper에서 `billing_class`/`billing-class` + metadata 경로를 모두 읽고 `metered`/`per-request`로 정규화
- **이유**: watcher/API에서 이미 정규화되더라도 기존 auth 데이터나 혼합 경로를 안전하게 수용해야 threshold 후보 필터가 정확해짐
