# Issues - Provider Logging Fix

## 2026-01-30: BLOCKED - 사용자 검증 필요

### Status
BLOCKED - 자동화된 검증 불가

### Remaining Tasks
1. `- [ ] 로그에서 model | provider:auth-label 형식 표시 (사용자 검증 필요)` (Line 61)
2. `- [ ] (사용자 검증) 실제 로그에서 provider:auth-label 표시 확인` (Line 230)

### Why Blocked
- 이 작업은 **실제 서버 실행** 및 **실제 API 요청**이 필요함
- 자동화된 검증으로는 확인할 수 없음
- 사용자가 직접:
  1. 서버를 실행 (`./cliproxy -c config.yaml`)
  2. API 요청을 전송
  3. 로그를 확인해야 함

### Expected Log Format
- **Before**: `glm-4.7 | (opencode-D3h3drck6)` (proxy access key)
- **After**: `glm-4.7 | openai-compatibility:my-auth-label` (provider:credential)

### Action Required
사용자가 서버를 실행하고 로그를 확인한 후, 위 체크박스들을 수동으로 완료 표시해야 함.

### Dev Tasks Completed
- [x] gin_logger.go 수정 완료
- [x] 빌드 검증 완료
- [x] 테스트 통과 완료
- [x] 커밋 및 푸시 완료 (`ce653ee`)
