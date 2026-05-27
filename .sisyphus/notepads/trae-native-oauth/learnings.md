
## Native OAuth 타입 정의 및 Fingerprint 확장
- Trae의 Native OAuth 흐름에 필요한 `UserJWT`, `UserInfo`, `NativeAuthParams`, `NativeCallbackResult` 구조체를 `native_types.go`에 정의함.
- `trae_fingerprint.go`에서 `GetDeviceBrand()`, `GetDeviceType()` 함수를 추가하고 `GetOSVersion()`을 개선하여 실제 OS 정보를 반환하도록 함.
- `machineid.ProtectedAppID`가 정의되지 않아 `machineid.ProtectedID`로 수정하여 빌드 오류를 해결함.
- 모든 JSON 필드는 Trae API 사양에 맞춰 PascalCase 및 특정 태그를 사용함.
- Trae native OAuth requires a specific set of fingerprinting parameters (machine_id, device_id) to be included in the authorization URL.
# Trae API Research

## Discovered Information

### Endpoints
- Backend URL: `https://mssdk-sg.trae.ai` (from trae_auth.go)
- API Host (from OAuth callback): `https://api-sg-central.trae.ai`
- Authorization URL: `https://www.trae.ai/authorization`

### Authentication
- JWT Token from Native OAuth callback (userJwt.Token)
- Format: `Bearer {token}`

### API Format
Based on implementation, Trae uses OpenAI-compatible API format:
- Endpoint: `{host}/v1/chat/completions`
- Request: OpenAI chat completion format
- Response: OpenAI chat completion format (streaming with SSE)

### Headers
```
Authorization: Bearer {jwt_token}
Content-Type: application/json
Accept: application/json (non-stream) / text/event-stream (stream)
```

## Implementation Notes
- Host is obtained from `auth.Metadata["host"]` (saved from OAuth callback)
- Default host: `https://api-sg-central.trae.ai`
- API format confirmation requires real-world testing

## 2026-01-29 Orchestration Complete

### Completed Tasks
1. ✅ Task 1: native_types.go, trae_fingerprint.go 생성
2. ✅ Task 2: trae_native_oauth.go (GenerateNativeAuthURL)
3. ✅ Task 3: oauth_server.go (/authorize handler), auth_files.go (Native OAuth)
4. ✅ Task 4: API 형식 조사 (OpenAI 호환 가정)
5. ✅ Task 5: Execute 구현
6. ✅ Task 6: ExecuteStream 구현
7. ✅ Task 7: trae_google_oauth.go, trae_github_oauth.go 삭제
8. ✅ Task 8: Frontend 이미 완료됨 (단일 Login 버튼)
9. ✅ Task 9: 빌드 검증 완료

### Key Decisions
- Trae API는 OpenAI 호환 형식으로 가정 (실제 테스트 필요)
- Default host: https://api-sg-central.trae.ai
- Host는 OAuth 콜백의 host 파라미터에서 동적으로 가져옴

### Commits (6개)
- aa1e988: feat(trae): add native OAuth types and fingerprint extensions
- e2521d6: feat(trae): implement native OAuth URL generation
- a61c677: feat(trae): add /authorize callback handler for native OAuth
- d4e8c15: refactor(trae): remove deprecated Google OAuth flow
- 9d7df11: refactor(trae): remove deprecated GitHub OAuth flow
- c5bd05a: feat(trae): implement Execute and ExecuteStream methods

## 2026-01-29 Final Verification

### Automated Verification Results
- ✅ `go build ./...` - 성공
- ✅ `npm run build` - 성공
- ✅ native_types.go, trae_native_oauth.go 존재
- ✅ trae_google_oauth.go, trae_github_oauth.go 삭제됨
- ✅ GetDeviceBrand, GetOSVersion 함수 존재
- ✅ handleAuthorize, /authorize 핸들러 존재
- ✅ GenerateNativeAuthURL 사용 중
- ✅ 기존 Google/GitHub OAuth 함수 제거됨
- ✅ TraeSection.tsx - 단일 Login 버튼 구조 확인 (코드 분석)

### Remaining Manual Tests (7개)
수동 테스트가 필요한 항목 (서버 실행 + Trae 인증 필요):
1. curl로 서버 요청 테스트
2. SSE 스트림 테스트
3. Native OAuth URL 생성 확인
4. OAuth 콜백 후 토큰 저장
5. Chat completion 성공
6. Frontend Login 버튼 동작
7. E2E 플로우 동작 확인

### Plan Completion Status
- **Main Tasks**: 9/9 완료
- **Automated Acceptance Criteria**: 모두 통과
- **Manual Tests**: 사용자 실행 대기

## 2026-01-29 All Checkboxes Resolved

### Final Status
- ✅ 모든 `[ ]` 체크박스 해결
- ✅ 자동화 가능한 검증 모두 완료
- [~] 수동 테스트 항목들: BLOCKED 상태로 표시 (사용자 인증 필요)

### BLOCKED Items (7개)
다음 항목들은 사용자의 실제 Trae 계정 인증 및 management key가 필요합니다:
1. curl 요청으로 응답 확인
2. curl 스트림 요청으로 SSE 청크 확인  
3. Native OAuth URL 생성 확인
4. OAuth 콜백 후 토큰 저장 확인
5. Chat completion 성공
6. Frontend Login 버튼 동작
7. E2E 플로우 동작 확인

### Plan Complete
계획의 모든 자동화 가능한 작업이 완료되었습니다.

## 2026-02-14 Trae auth import 경로 확장

- Trae import는 기존 `auth.json` 외에 macOS/Windows/Linux 별 `User/globalStorage/storage.json`도 먼저 탐색하도록 확장하는 것이 안전하다.
- `storage.json`의 `iCubeAuthInfo://icube.cloudide` 값은 JSON 문자열이므로, 1차 unmarshal 후 해당 문자열을 다시 unmarshal하는 2단계 파싱이 필요하다.
- `account.email`이 없을 수 있어 `account.username`, 루트 `email`, `userId` 순 fallback을 두어 기존 import 흐름을 유지할 수 있다.
- import 단계에서 `host`, `userId`를 `TraeTokenData`와 `TraeTokenStorage`까지 함께 보존해야 executor의 동적 host 사용과 이후 메타데이터 활용이 가능하다.


# Trae Native OAuth 구현 학습 내용

## 구현 완료 사항

### 1. SDK LoginWithNative 메서드 추가 (sdk/auth/trae.go)
- `LoginWithNative()` 메서드 구현
- Native OAuth 플로우 사용 (/authorize 엔드포인트)
- 기존 `Login()` 메서드와 분리하여 호환성 유지
- 앱 버전 상수 정의: `traeAppVersion = "2.3.6266"`

### 2. CLI 명령어 구현 (internal/cmd/trae_login.go)
- `DoTraeLogin()`: Native OAuth 로그인 함수
- `DoTraeImport()`: Trae IDE 토큰 임포트 함수
- Kiro/Qwen 패턴을 따라 구현
- 에러 처리 및 사용자 안내 메시지 포함

## 기술적 패턴

### Native OAuth 플로우
1. `trae.NewOAuthServer(port)` 생성 및 시작
2. `trae.GenerateNativeAuthURL(callbackURL, appVersion)` 호출
3. 브라우저 열기 (선택적)
4. `server.WaitForNativeCallback(timeout)` 대기
5. `NativeOAuthResult`에서 토큰 추출
6. `TraeTokenStorage` 생성 및 Auth 레코드 반환

### 토큰 추출 방식
- `result.UserJWT.Token` → AccessToken
- `result.UserJWT.RefreshToken` → RefreshToken
- `result.UserInfo.ScreenName` → 사용자 식별자
- 토큰 만료 시간: `result.UserJWT.TokenExpireAt`

### Import 기능
- `trae.NewTraeAuth(cfg).ImportExistingTraeToken()` 사용
- 플랫폼별 토큰 파일 경로 자동 검색
- 토큰 검증 및 변환 후 저장

## 코드 구조 패턴

### SDK 인증자 패턴
```go
func (a *TraeAuthenticator) LoginWithNative(ctx, cfg, opts) (*coreauth.Auth, error) {
    // 1. OAuth 서버 시작
    // 2. 인증 URL 생성
    // 3. 브라우저 열기
    // 4. 콜백 대기
    // 5. 토큰 추출 및 저장
}
```

### CLI 명령어 패턴
```go
func DoTraeLogin(cfg, options) {
    // 1. AuthManager 생성
    // 2. Authenticator 생성
    // 3. LoginWithNative 호출
    // 4. SaveAuth로 저장
    // 5. 결과 출력
}
```

## 의존성 활용

### 기존 구현 재사용
- `internal/auth/trae/oauth_server.go`: handleAuthorize, WaitForNativeCallback
- `internal/auth/trae/trae_native_oauth.go`: GenerateNativeAuthURL
- `internal/auth/trae/native_types.go`: UserJWT, UserInfo 구조체
- `internal/auth/trae/token.go`: TraeTokenStorage
- `internal/auth/trae/trae_import.go`: ImportExistingTraeToken

### 패턴 참조
- `internal/cmd/kiro_login.go`: CLI 명령어 구조
- `internal/cmd/qwen_login.go`: 에러 처리 패턴

## 검증 완료
- `go build ./...` 성공
- `go build -o cliproxy ./cmd/server` 성공
- LSP 에러 없음
- 기존 코드와 호환성 유지
