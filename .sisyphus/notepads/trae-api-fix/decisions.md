# Trae API Executor 아키텍처 결정사항

## 1. 엔드포인트 선택
**결정**: `https://trae-api-sg.mchost.guru/api/ide/v1/chat` 사용
**근거**: 
- trae2api 참조 코드에서 확인된 올바른 엔드포인트
- OpenAI 호환 엔드포인트가 아닌 Trae 전용 API 사용 필요

## 2. 인증 방식
**결정**: `x-ide-token` 헤더 사용
**근거**:
- Trae API는 Bearer 토큰이 아닌 IDE 토큰 방식 사용
- 다양한 디바이스 정보 헤더들과 함께 IDE 클라이언트로 인식되어야 함

## 3. 요청 변환 전략
**결정**: Executor 내부에서 직접 변환 수행
**근거**:
- translator 패키지 수정 금지 요구사항
- Trae API의 특수한 구조로 인해 범용 translator로는 한계
- 단일 책임 원칙에 따라 Trae 전용 로직을 Executor에 집중

## 4. 응답 처리 방식
**결정**: SSE 스트림을 직접 파싱하여 OpenAI 형식으로 변환
**근거**:
- Trae의 event/data 형식과 OpenAI의 data: 형식 차이
- thinking 태그 처리 등 Trae 특화 기능 지원 필요

## 5. 디바이스 정보 생성
**결정**: 랜덤 생성으로 실제 IDE 환경 시뮬레이션
**근거**:
- Trae API가 IDE 클라이언트 검증을 위해 디바이스 정보 요구
- 고정값보다 동적 생성이 더 안전하고 현실적

## 6. 에러 처리
**결정**: 상세한 에러 메시지와 함께 적절한 HTTP 상태 코드 반환
**근거**:
- 디버깅 편의성
- 기존 CLIProxyAPI 에러 처리 패턴 유지