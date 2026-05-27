# Provider Token Logging

## Context

사용자 요청: `/v1/messages` access log에 input/output token 정보를 함께 출력한다.

## TODOs

- [x] 1. usage detail을 요청 컨텍스트에 저장하는 최소 범위 로직 추가
- [x] 2. gin access log에 input/output token 정보를 append 하도록 로깅 포맷 확장
- [x] 3. 변경 경로를 검증하는 테스트 추가 및 백엔드 빌드/테스트 통과 확인

## Final Verification Wave

- [x] F1. 구현 범위가 `/v1/messages` 토큰 로깅 요구에만 한정되었는지 검토 승인
- [x] F2. usage 저장 경로와 gin logger 소비 경로가 논리적으로 연결되는지 검토 승인
- [x] F3. 관련 테스트와 빌드가 모두 통과하는지 검토 승인
- [x] F4. 최종 로그 형식이 기존 정보에 input/output token만 추가하는지 검토 승인
