# Issues

## 2026-04-05 T1 Implementation

### 해결된 이슈
- 없음

### 발견된 잠재적 이슈
- `normalizeTokenThresholdRuleBillingClass` 함수 미사용 → 향후 리팩토링 고려

## 2026-04-06 T2 Runtime Fix

### 해결한 이슈
- `matchTokenThresholdRule`가 사실상 `max-tokens`만 검사해 lower-only / bounded 규칙이 런타임에서 오동작하던 문제 수정
- threshold auth 매칭이 `Attributes["billing_class"]`만 읽어 `billing-class` 또는 metadata 기반 billing class를 놓치던 문제 수정
