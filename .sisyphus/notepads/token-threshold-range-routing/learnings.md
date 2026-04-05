# Learnings

## 2026-04-05 T1 Implementation

### JSON/YAML omitempty와 0값 처리
- `MinTokens int` 필드를 `omitempty` 태그와 함께 사용하면 JSON 직렬화 시 0값이 생략됨
- GET API 응답에서 `min-tokens: 0`이 누락되는 것은 의도적 동작
- 테스트에서는 `omitempty`로 인한 누락을 `continue`로 처리해야 함

### 정렬 기준 확장
- 기존: `MaxTokens` 오름차순 → `ModelPattern` 오름차순
- 확장: `MinTokens` 오름차순 → `MaxTokens` 오름차순 → `ModelPattern` 오름차순
- 범위 규칙에서 낮은 `MinTokens`가 먼저 평가되도록 보장

### Dead code 발견
- `normalizeTokenThresholdRuleBillingClass` 함수는 실제 호출부가 없음
- PUT 핸들러는 `SanitizeTokenThresholdRules`만 사용
- 이 함수는 향후 정리 대상일 수 있음

## 2026-04-06 T2 Runtime Fix

### 런타임 threshold 매칭 해석
- 런타임 `matchTokenThresholdRule`는 sanitize된 규칙 순서를 신뢰하고 첫 매칭 하나만 적용하면 충분함
- 단, upper-only만 보면 lower-only / bounded가 모두 깨지므로 `MinTokens`와 `MaxTokens`를 각각 독립 조건으로 평가해야 함

### billing class 읽기 경로
- 런타임 auth 필터링은 `Attributes["billing_class"]`만 보면 불완전함
- 실제 저장소에서는 `billing_class`, `billing-class`, 그리고 `Metadata` 경로까지 혼재할 수 있어 런타임 helper에서 모두 정규화해 읽는 편이 안전함
