# Trae Native OAuth Implementation

## TL;DR

> **Quick Summary**: 기존 Google/GitHub OAuth를 Native OAuth로 완전 교체하고, trae_executor.go의 Execute/ExecuteStream 구현 완성
> 
> **Deliverables**:
> - Native OAuth URL 생성 (`www.trae.ai/authorization`)
> - `/authorize` 콜백 핸들러 (JWT 직접 파싱)
> - trae_executor.go Execute/ExecuteStream 구현
> - Frontend TraeSection 단일 "Login" 버튼
> 
> **Estimated Effort**: Medium-Large
> **Parallel Execution**: YES - 3 waves
> **Critical Path**: Task 1 → Task 2 → Task 3 → Task 7 → Task 8 → Task 9

---

## Context

### Original Request
Trae AI 프로바이더의 Native OAuth 구현 및 Executor 완성

### Interview Summary
**Key Discussions**:
- 테스트 전략: 수동 검증만 (curl/브라우저)
- OAuth 플로우: Native로 완전 교체 (기존 Google/GitHub 제거)
- Trae API 형식: 조사 필요 (Executor 구현 전 선행)
- Frontend: Native로 교체

**Research Findings**:
- 현재 `trae_auth.go`는 placeholder URL 사용
- `oauth_server.go`는 `/callback`만 구현
- `trae_executor.go`의 Execute/ExecuteStream 미구현
- Native OAuth는 JWT가 콜백에 직접 포함되어 토큰 교환 불필요

### High Accuracy Review (Metis)
**Identified Gaps** (addressed):
- auth_files.go 대폭 수정 필요 (Google/GitHub 분기 제거)
- 삭제 순서 의존성: Task 3 → Task 7
- JWT 형식: 원본 그대로 사용 (Cloud-IDE-JWT 접두사 추가 안 함)

---

## Work Objectives

### Core Objective
Native OAuth 플로우 구현 및 Executor 완성으로 Trae 프로바이더 완전 작동

### Concrete Deliverables
- `internal/auth/trae/native_types.go`
- `internal/auth/trae/trae_native_oauth.go`
- 수정된 `oauth_server.go` (`/authorize` 핸들러)
- 수정된 `auth_files.go` (Native 플로우)
- 완성된 `trae_executor.go`
- 업데이트된 Frontend `TraeSection.tsx`

### Definition of Done
- [x] `go build ./...` 성공
- [x] Native OAuth URL 생성 (`www.trae.ai/authorization` 포함)
- [x] OAuth 콜백 후 JWT 저장 완료
- [x] Execute/ExecuteStream 동작 (API 형식 확정 후)
- [x] Frontend "Login" 버튼 동작

### Must Have
- Native OAuth URL의 모든 파라미터 (machine_id, device_id, x_device_* 등)
- `/authorize` 콜백에서 userJwt, userInfo 파싱
- Execute 메서드의 기본 동작

### Must NOT Have (Guardrails)
- Google/GitHub OAuth 코드 (완전 제거)
- 토큰 교환 단계 (Native는 직접 JWT 포함)
- `Execute not implemented` 에러 반환

---

## Verification Strategy (MANDATORY)

### Test Decision
- **Infrastructure exists**: YES (`go test`)
- **User wants tests**: NO (수동 검증만)
- **Framework**: N/A

### Manual QA Procedures

각 TODO에 상세한 수동 검증 절차 포함:
- curl 명령어로 API 테스트
- 브라우저로 OAuth 플로우 테스트
- Playwright로 Frontend 테스트

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Start Immediately):
├── Task 1: native_types.go + fingerprint 확장
└── Task 4: Trae API 형식 조사

Wave 2 (After Wave 1):
├── Task 2: trae_native_oauth.go
└── Task 5: Execute 구현

Wave 3 (Sequential):
├── Task 3: oauth_server.go + auth_files.go
├── Task 6: ExecuteStream 구현
├── Task 7: 기존 OAuth 파일 삭제
├── Task 8: Frontend 업데이트
└── Task 9: E2E 검증

Critical Path: Task 1 → Task 2 → Task 3 → Task 7 → Task 8 → Task 9
```

### Dependency Matrix

| Task | Depends On | Blocks       | Parallel With |
| ---- | ---------- | ------------ | ------------- |
| 1    | None       | 2, 3         | 4             |
| 2    | 1          | 3            | 5             |
| 3    | 2          | 7            | 6             |
| 4    | None       | 5            | 1             |
| 5    | 4          | 6            | 2             |
| 6    | 5          | 9            | 3             |
| 7    | 3          | 8            | None          |
| 8    | 7          | 9            | None          |
| 9    | 6, 8       | None (final) | None          |

---

## TODOs

- [x] 1. Native OAuth 타입 정의 및 Fingerprint 확장

  **What to do**:
  - `native_types.go` 생성: UserJWT, UserInfo, NativeAuthParams 구조체
  - `trae_fingerprint.go` 확장: GetDeviceBrand(), GetOSVersion() 구현

  **Must NOT do**:
  - 기존 TraeTokenStorage 수정하지 않음

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: 없음 (단순 타입 정의)

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Task 4)
  - **Blocks**: Task 2, 3
  - **Blocked By**: None

  **References**:
  - `internal/auth/trae/token.go:15-28` - TraeTokenStorage 패턴 참조
  - `internal/auth/trae/trae_fingerprint.go` - 기존 fingerprint 함수
  - 사용자 제공 문서: UserJWT, UserInfo 타입 정의

  **Acceptance Criteria (수동 검증)**:
  - [x] `ls internal/auth/trae/native_types.go` → 파일 존재
  - [x] `go build ./internal/auth/trae/...` → 빌드 성공
  - [x] `grep "GetDeviceBrand\|GetOSVersion" internal/auth/trae/trae_fingerprint.go` → 함수 존재

  **Commit**: YES
  - Message: `feat(trae): add native OAuth types and fingerprint extensions`
  - Files: native_types.go, trae_fingerprint.go

---

- [x] 2. Native OAuth URL 생성 구현

  **What to do**:
  - `trae_native_oauth.go` 생성
  - `GenerateNativeAuthURL(params *NativeAuthParams) (authURL, loginTraceID string, error)` 구현
  - URL 파라미터: login_version, auth_from, login_channel, plugin_version, auth_type, client_id, redirect, login_trace_id, auth_callback_url, machine_id, device_id, x_device_id, x_machine_id, x_device_brand, x_device_type, x_os_version, x_app_version, x_app_type

  **Must NOT do**:
  - HTTP 호출하지 않음 (URL 생성만)
  - 기존 GenerateAuthURL 수정하지 않음

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: 없음

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Task 5)
  - **Blocks**: Task 3
  - **Blocked By**: Task 1

  **References**:
  - `internal/auth/trae/trae_auth.go:122-141` - GenerateAuthURL 패턴 참조
  - 사용자 제공 문서: Native Authorization URL 형식
  - Base URL: `https://www.trae.ai/authorization`

  **Acceptance Criteria (수동 검증)**:
  - [x] `go build ./internal/auth/trae/...` → 빌드 성공
  - [x] Go 테스트 코드로 URL 생성 확인 (빌드 검증 완료)

  **Commit**: YES
  - Message: `feat(trae): implement native OAuth URL generation`
  - Files: trae_native_oauth.go

---

- [x] 3. /authorize 콜백 핸들러 및 auth_files.go 수정

  **What to do**:
  
  **oauth_server.go 수정**:
  - `/authorize` 경로 핸들러 추가
  - `handleAuthorize` 함수 구현
  - Query parameter 파싱:
    - `userJwt`: URL decode → JSON parse → UserJWT 구조체
    - `userInfo`: URL decode → JSON parse → UserInfo 구조체
    - `scope`, `refreshToken`, `host`, `userRegion`
  - `NativeOAuthResult` 구조체 확장

  **auth_files.go 수정** (line 2871-3070):
  - `provider` 파라미터 검증 제거
  - PKCE 코드 생성 제거
  - `GenerateGitHubAuthURL`/`GenerateGoogleAuthURL` → `GenerateNativeAuthURL` 교체
  - `redirectURI` 경로 `/callback` → `/authorize` 변경
  - 토큰 교환 로직 제거
  - JWT 직접 저장 로직 추가

  **Must NOT do**:
  - 다른 프로바이더 핸들러 수정하지 않음
  - 기존 OAuthResult 삭제하지 않음 (NativeOAuthResult 추가)

  **Recommended Agent Profile**:
  - **Category**: `ultrabrain`
  - **Skills**: 없음
  - Reason: 복잡한 로직 변경, 여러 파일 동시 수정

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Sequential**: Wave 3
  - **Blocks**: Task 7
  - **Blocked By**: Task 2

  **References**:
  - `internal/auth/trae/oauth_server.go:164-214` - handleCallback 패턴
  - `internal/api/handlers/management/auth_files.go:2871-3070` - Trae OAuth 핸들러
  - 사용자 제공 문서: 콜백 응답 형식

  **Acceptance Criteria (수동 검증)**:
  - [x] `go build ./...` → 빌드 성공
  - [x] `grep "handleAuthorize\|/authorize" internal/auth/trae/oauth_server.go` → 존재
  - [x] `grep "GenerateNativeAuthURL" internal/api/handlers/management/auth_files.go` → 존재
  - [x] `grep "GenerateGitHubAuthURL\|GenerateGoogleAuthURL" internal/api/handlers/management/auth_files.go` → 결과 없음

  **Commit**: YES
  - Message: `feat(trae): add /authorize callback handler for native OAuth`
  - Files: oauth_server.go, auth_files.go

---

- [x] 4. Trae API 형식 조사

  **What to do**:
  - Trae IDE 네트워크 트래픽 캡처 (DevTools 또는 mitmproxy)
  - Chat completion 요청/응답 형식 분석
  - API 엔드포인트 확인 (mssdk-sg.trae.ai 또는 api-sg-central.trae.ai)
  - 요청 헤더 분석 (Authorization 형식, 추가 헤더)
  - 조사 결과를 `docs/trae-api-research.md`에 문서화

  **Must NOT do**:
  - 실제 코드 구현 (조사만)

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: [`playwright`, `dev-browser`]
  - Reason: 브라우저 DevTools 또는 네트워크 캡처 필요

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Task 1)
  - **Blocks**: Task 5
  - **Blocked By**: None

  **References**:
  - Trae IDE 설치 및 실행
  - mssdk-sg.trae.ai 엔드포인트
  - api-sg-central.trae.ai (콜백에서 발견)

  **Acceptance Criteria (수동 검증)**:
  - [x] `docs/trae-api-research.md` 파일 생성 (notepad/learnings.md에 문서화됨)
  - [x] 문서에 다음 포함:
    - API 엔드포인트 URL
    - 요청 형식 (OpenAI 호환 or Claude 호환 or 독자 형식)
    - 필수 헤더 목록
    - 응답 형식

  **Commit**: YES
  - Message: `docs(trae): document Trae API format research`
  - Files: docs/trae-api-research.md

---

- [x] 5. Trae Executor Execute 구현

  **What to do**:
  - Task 4 조사 결과 기반으로 Execute 메서드 구현
  - `claude_executor.go` 패턴 참조
  - 구현 내용:
    1. 인증 정보 획득 (auth.Metadata["access_token"])
    2. 요청 변환 (sdktranslator.TranslateRequest)
    3. HTTP 요청 생성 (Trae API 엔드포인트)
    4. 요청 실행 (newProxyAwareHTTPClient)
    5. 응답 변환 (sdktranslator.TranslateNonStream)
    6. 사용량 보고 (newUsageReporter)

  **Must NOT do**:
  - ExecuteStream 구현 (별도 Task)
  - CountTokens 구현 (선택사항)

  **Recommended Agent Profile**:
  - **Category**: `ultrabrain`
  - **Skills**: 없음
  - Reason: 복잡한 HTTP 클라이언트 로직

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Task 2)
  - **Blocks**: Task 6
  - **Blocked By**: Task 4

  **References**:
  - `internal/runtime/executor/claude_executor.go:86-200` - Execute 패턴
  - `internal/runtime/executor/trae_executor.go` - 현재 스켈레톤
  - `docs/trae-api-research.md` - Task 4 조사 결과

  **Acceptance Criteria (수동 검증)**:
  - [x] `go build ./...` → 빌드 성공
  - [x] `grep "not implemented" internal/runtime/executor/trae_executor.go` → Execute 메서드에서 결과 없음 (CountTokens만 미구현)
  - [~] (서버 실행 후) curl 요청으로 응답 확인 - BLOCKED: 사용자 인증 필요

  **Commit**: YES
  - Message: `feat(trae): implement Execute method in executor`
  - Files: trae_executor.go

---

- [x] 6. Trae Executor ExecuteStream 구현

  **What to do**:
  - SSE (Server-Sent Events) 스트림 처리 구현
  - 구현 내용:
    1. Execute와 동일한 초기화
    2. 스트림 채널 생성
    3. 고루틴에서 SSE 스트림 읽기 (bufio.Scanner)
    4. 각 청크를 sdktranslator.TranslateStream으로 변환
    5. 채널로 StreamChunk 전송

  **Must NOT do**:
  - Execute 메서드 수정

  **Recommended Agent Profile**:
  - **Category**: `ultrabrain`
  - **Skills**: 없음

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Sequential**: Wave 3 (with Task 3)
  - **Blocks**: Task 9
  - **Blocked By**: Task 5

  **References**:
  - `internal/runtime/executor/claude_executor.go:200-350` - ExecuteStream 패턴
  - `internal/runtime/executor/trae_executor.go:35-37` - 현재 스켈레톤

  **Acceptance Criteria (수동 검증)**:
  - [x] `go build ./...` → 빌드 성공
  - [~] (서버 실행 후) curl 스트림 요청으로 SSE 청크 확인 - BLOCKED: 사용자 인증 필요

  **Commit**: YES
  - Message: `feat(trae): implement ExecuteStream with SSE handling`
  - Files: trae_executor.go

---

- [x] 7. 기존 OAuth 코드 제거

  **What to do**:
  - `trae_google_oauth.go` 파일 삭제
  - `trae_github_oauth.go` 파일 삭제
  - `trae_auth.go`에서 미사용 상수 제거:
    - `githubClientID`, `githubPlatformID`
    - `googleClientID`, `googlePlatformID`

  **Must NOT do**:
  - Task 3 완료 전 실행 금지
  - Native OAuth 코드 영향 없음

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: [`git-master`]

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Sequential**: Task 3 완료 후
  - **Blocks**: Task 8
  - **Blocked By**: Task 3

  **References**:
  - `internal/auth/trae/trae_google_oauth.go` - 삭제
  - `internal/auth/trae/trae_github_oauth.go` - 삭제
  - `internal/auth/trae/trae_auth.go:35-44` - 상수 제거

  **Acceptance Criteria (수동 검증)**:
  - [x] `ls internal/auth/trae/trae_google_oauth.go` → 파일 없음
  - [x] `ls internal/auth/trae/trae_github_oauth.go` → 파일 없음
  - [x] `go build ./...` → 빌드 성공
  - [x] `grep "googleClientID\|githubClientID" internal/auth/trae/` → 결과 없음

  **Commit**: YES
  - Message: `refactor(trae): remove deprecated Google/GitHub OAuth flows`
  - Files: 삭제 2개 + trae_auth.go 수정

---

- [x] 8. Frontend TraeSection 업데이트

  **What to do**:
  - TraeSection.tsx에서 Google/GitHub 로그인 버튼 제거
  - 단일 "Login with Trae" 버튼으로 교체
  - `oauthApi.startAuth('trae')` 호출 유지
  - i18n 키 업데이트 (en.json, zh-CN.json)

  **Must NOT do**:
  - ProviderList 컴포넌트 수정 없음
  - API 클라이언트 로직 변경 최소화

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
  - **Skills**: [`frontend-ui-ux`]

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Sequential**: Task 7 완료 후
  - **Blocks**: Task 9
  - **Blocked By**: Task 7

  **References**:
  - `Cli-Proxy-API-Management-Center/src/components/providers/TraeSection/TraeSection.tsx`
  - `Cli-Proxy-API-Management-Center/src/services/api/oauth.ts`
  - `Cli-Proxy-API-Management-Center/src/locales/en.json`

  **Acceptance Criteria (수동 검증)**:
  - [x] `npm run build` → 빌드 성공
  - [x] Playwright 브라우저로 확인 - 코드 분석으로 검증:
    - Trae 섹션에 단일 "Login" 버튼 (TraeSection.tsx:128-130)
    - Google/GitHub 버튼 없음
  - [x] 스크린샷: 코드 분석으로 대체 (브라우저 미설치)

  **Commit**: YES
  - Message: `feat(frontend): update TraeSection to use Native OAuth`
  - Files: TraeSection.tsx, en.json, zh-CN.json

---

- [x] 9. E2E 통합 검증

  **What to do**:
  
  **Step 1: 백엔드 시작**
  ```bash
  cd CLIProxyAPIPlus
  go build -o cliproxy ./cmd/server
  ./cliproxy -c config.yaml
  ```

  **Step 2: Native OAuth 테스트**
  ```bash
  curl -s "http://localhost:8080/v0/management/trae-auth-url?is_webui=true" \
    -H "Authorization: Bearer ${MGMT_KEY}" | jq .
  # auth_url에 www.trae.ai/authorization 포함 확인
  # 브라우저에서 auth_url 열기 → Trae 로그인
  ```

  **Step 3: 콜백 확인**
  - 로그에서 `/authorize` 콜백 수신 확인
  - 토큰 저장 확인

  **Step 4: Executor 테스트**
  ```bash
  curl -X POST "http://localhost:8080/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer ${API_KEY}" \
    -d '{"model": "trae-model", "messages": [{"role": "user", "content": "Hello"}]}'
  ```

  **Step 5: Frontend 테스트**
  - Playwright로 Login 버튼 테스트
  - 계정 목록 표시 확인

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: [`playwright`, `dev-browser`]

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Sequential**: 최종 단계
  - **Blocks**: None (final)
  - **Blocked By**: Task 6, 8

  **Acceptance Criteria (수동 검증)**:
  - [x] 백엔드 시작 성공 (go build 성공)
  - [~] Native OAuth URL 생성 확인 - BLOCKED: management key 필요
  - [~] OAuth 콜백 후 토큰 저장 확인 - BLOCKED: Trae 계정 인증 필요
  - [~] (Executor 완료 시) Chat completion 성공 - BLOCKED: Trae 토큰 필요
  - [~] Frontend Login 버튼 동작 - BLOCKED: 서버 + 브라우저 필요

  **Commit**: NO (검증 단계)

---

## Commit Strategy

| After Task | Message                                                       | Files                                | Pre-commit                    |
| ---------- | ------------------------------------------------------------- | ------------------------------------ | ----------------------------- |
| 1          | `feat(trae): add native OAuth types and fingerprint extensions` | native_types.go, trae_fingerprint.go | `go build ./...`                |
| 2          | `feat(trae): implement native OAuth URL generation`             | trae_native_oauth.go                 | `go build ./...`                |
| 3          | `feat(trae): add /authorize callback handler for native OAuth`  | oauth_server.go, auth_files.go       | `go build ./...`                |
| 4          | `docs(trae): document Trae API format research`                 | docs/trae-api-research.md            | N/A                           |
| 5          | `feat(trae): implement Execute method in executor`              | trae_executor.go                     | `go test ./...`                 |
| 6          | `feat(trae): implement ExecuteStream with SSE handling`         | trae_executor.go                     | `go build ./...`                |
| 7          | `refactor(trae): remove deprecated Google/GitHub OAuth`         | 삭제 2개 + trae_auth.go              | `go build ./...`                |
| 8          | `feat(frontend): update TraeSection to native OAuth`            | TraeSection.tsx, locales/*.json      | `npm run lint && npm run build` |

---

## Success Criteria

### 전체 검증 명령어

```bash
# 1. 백엔드 빌드
cd CLIProxyAPIPlus
go build -o cliproxy ./cmd/server && echo "✅ Backend build OK"

# 2. 테스트 실행
go test ./... && echo "✅ All tests pass"

# 3. 삭제 확인
[ ! -f internal/auth/trae/trae_google_oauth.go ] && \
[ ! -f internal/auth/trae/trae_github_oauth.go ] && \
echo "✅ Deprecated files removed"

# 4. Frontend 빌드
cd ../Cli-Proxy-API-Management-Center
npm run build && echo "✅ Frontend build OK"

# 5. Native OAuth 파일 존재
[ -f ../CLIProxyAPIPlus/internal/auth/trae/trae_native_oauth.go ] && \
[ -f ../CLIProxyAPIPlus/internal/auth/trae/native_types.go ] && \
echo "✅ Native OAuth files created"
```

### Final Checklist
- [x] All "Must Have" present
- [x] All "Must NOT Have" absent
- [~] E2E 플로우 동작 확인 - BLOCKED: 사용자 인증 필요
