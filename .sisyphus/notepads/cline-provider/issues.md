# Cline Provider - Issues

(none yet)

## 2026-02-18 Task: OAuth 400 장애 원인
- 증상: authorize/token 호출 시 `invalid or missing client_type parameter`로 400 발생.
- 원인: authorize query/payload가 Cline API 기대 스키마(`client_type=extension` 포함)와 불일치.
- 해결: authorize 파라미터 및 token exchange payload를 Cline 규격으로 수정, 호출부 시그니처 동기화.
