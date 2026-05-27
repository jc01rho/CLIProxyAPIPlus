# Antigravity LS Binary Migration

## TL;DR

> **Quick Summary**: CLIProxyAPIPlus의 Antigravity executor를 Go HTTP 직접 호출 방식에서 LS(Language Server) 바이너리 서브프로세스 방식으로 완전 교체. LS 바이너리는 Antigravity IDE에 내장된 실제 바이너리로, BoringSSL 기반 TLS를 사용하여 JA3/JA4 핑거프린팅을 원천 회피.
> 
> **Deliverables**:
> - LS 바이너리 관리 모듈 (자동 감지, 다운로드, 수명주기 관리)
> - LS 프로세스 매니저 (서브프로세스 시작/정지/크래시 복구)
> - LS HTTP 클라이언트 (로컬 API 통신)
> - state.vscdb 리더 (SQLite 토큰 자동 갱신)
> - Antigravity executor 완전 재작성
> - 설정 스키마 확장
> 
> **Estimated Effort**: Large
> **Parallel Execution**: YES - 4 waves
> **Critical Path**: Task 1 → Task 7 → Task 10 → Final
> **NOTE**: 빌드/실행 방식은 기존 유지. Dockerfile 변경 없음. Antigravity 호출 방식만 마이그레이션.

---

## Context

### Original Request
ZeroGravity 프로젝트의 Antigravity 호출 방식(LS 바이너리 서브프로세스)을 분석하고, CLIProxyAPIPlus에 동일한 방식을 적용하여 ban 회피를 도모.

### Interview Summary
**Key Discussions**:
- **완전 교체**: 기존 Go HTTP 직접 호출 방식을 제거하고 LS 바이너리 방식으로 완전 대체
- **자동 감지 + 다운로드**: 설치된 Antigravity에서 LS 바이너리 자동 감지, 미설치 시 Google CDN에서 자동 다운로드
- **state.vscdb**: Antigravity의 SQLite DB에서 refresh token 읽어 자동 갱신 지원
- **전 플랫폼**: Linux, macOS, Windows 모두 지원

**Research Findings**:
- ZeroGravity는 LS 바이너리를 서브프로세스로 실행, 로컬 HTTP API (127.0.0.1:8742)로 통신
- LS 바이너리 API는 `SendUserCascadeMessage`, `GetCommandModelConfigs` 등의 메서드 사용
- 토큰은 HTTP 헤더가 아닌 JSON body의 `metadata.apiKey` 필드에 포함
- Google CDN URL: `https://edgedl.me.gvt1.com/edgedl/release2/j0qc3/antigravity/stable/{version}/{platform}/Antigravity.tar.gz`
- LS 바이너리는 BoringSSL 사용으로 JA3/JA4 핑거프린팅 회피
- state.vscdb 경로: Linux `~/.config/Antigravity/User/globalStorage/state.vscdb`, macOS `~/Library/Application Support/Antigravity/User/globalStorage/state.vscdb`, Windows `%APPDATA%\Antigravity\User\globalStorage\state.vscdb`

### Metis Review
**Identified Gaps** (addressed):
- **버전 관리**: 설정 가능한 버전 핀닝 (기본값: 최신 알려진 버전 1.18.3-4739469533380608)
- **포트 충돌**: 동적 포트 할당으로 해결
- **크래시 복구**: 지수 백오프 + 최대 5회 재시작, 이후 요청 실패
- **UID 격리**: 이번 스코프에서 제외 (문서화만, 향후 선택적 적용)
- **Fallback**: 없음 (완전 교체 결정에 따라)
- **성능**: LS는 영구 프로세스, 로컬 HTTP 오버헤드 <1ms로 무시 가능

---

## Work Objectives

### Core Objective
Antigravity executor의 API 호출 방식을 Go HTTP 직접 호출에서 LS 바이너리 서브프로세스 방식으로 교체하여, 네트워크 트래픽이 실제 Antigravity IDE와 동일한 TLS 핑거프린트를 갖도록 함.

### Concrete Deliverables
- `internal/runtime/executor/ls_*.go` — LS 관련 새 모듈 5~7개
- `internal/runtime/executor/antigravity_executor.go` — 완전 재작성 (LS 기반)
- `internal/config/config.go` — LS 설정 필드 추가
- `config.example.yaml` — 새 LS 설정 예시

### Definition of Done
- [ ] `go build ./...` 성공
- [ ] `go test ./...` 통과
- [ ] LS 바이너리가 서브프로세스로 시작되고 헬스체크 통과
- [ ] OpenAI 호환 API를 통한 Antigravity 요청이 LS 바이너리 경유로 동작

### Must Have
- LS 바이너리 자동 감지 (설치된 Antigravity에서)
- LS 바이너리 자동 다운로드 (Google CDN에서)
- LS 프로세스 라이프사이클 관리 (시작, 정지, 크래시 복구)
- state.vscdb 토큰 자동 갱신
- 멀티 플랫폼 (Linux x64/ARM, macOS ARM, Windows x64)
- 기존 translator 시스템 호환 유지
- 기존 빌드/실행 방식 유지 (`go build ./cmd/server`, `./cliproxy -c config.yaml`)

### Must NOT Have (Guardrails)
- 기존 Go HTTP 직접 호출과의 병행 실행 (완전 교체만)
- Dockerfile 변경 — 빌드/실행 방식은 기존 그대로 유지
- UID 격리 (zerogravity-ls user, iptables) — 이번 스코프 외
- 자동 LS 바이너리 버전 업데이트 — 수동 설정만
- 새로운 API 기능 추가 — Antigravity 호출 방식 변경만
- 로깅 포맷 변경, 메트릭 추가 등 부수적 개선
- AI slop: 과도한 주석, 과잉 추상화, generic 변수명 (data/result/item/temp)
- `http.DefaultClient` 사용 → `util.NewProxyClient()` 또는 직접 구성한 클라이언트
- 토큰 직접 로깅 → 마스킹 필수

---

## Verification Strategy

> **ZERO HUMAN INTERVENTION** — ALL verification is agent-executed. No exceptions.

### Test Decision
- **Infrastructure exists**: YES (go test)
- **Automated tests**: YES (Tests-after)
- **Framework**: `go test`

### QA Policy
Every task MUST include agent-executed QA scenarios.
Evidence saved to `.sisyphus/evidence/task-{N}-{scenario-slug}.{ext}`.

- **Backend**: Use Bash — `go build`, `go test`, `curl` 요청 검증
- **Subprocess**: Use Bash (tmux) — 프로세스 시작/정지, 포트 리스닝 확인

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Start Immediately — foundation, ALL independent):
├── Task 1: LS Binary API 리서치 + 문서화 [deep]
├── Task 2: Config 스키마 확장 (LS 설정 필드) [quick]
├── Task 3: LS Binary Detector (플랫폼별 자동 감지) [unspecified-high]
├── Task 4: LS Binary Downloader (Google CDN 다운로드) [unspecified-high]
├── Task 5: state.vscdb Reader (SQLite 토큰 리더) [unspecified-high]
└── Task 6: Port Allocator (동적 포트 할당) [quick]

Wave 2 (After Wave 1 — core modules):
├── Task 7: LS Process Manager (서브프로세스 라이프사이클) [deep]
├── Task 8: LS HTTP Client (LS 로컬 API 통신) [unspecified-high]
└── Task 9: Token Provider (통합 토큰 소스) [unspecified-high]

Wave 3 (After Wave 2 — integration):
├── Task 10: Antigravity Executor 재작성 (LS 기반) [deep]
└── Task 11: Usage Helpers 적응 [quick]

Wave 4 (After Wave 3 — docs):
├── Task 12: config.example.yaml 업데이트 [quick]
└── Task 13: AGENTS.md 문서 업데이트 [quick]

Wave FINAL (After ALL — verification):
├── Task F1: Plan Compliance Audit [oracle]
├── Task F2: Code Quality Review [unspecified-high]
├── Task F3: Real QA [unspecified-high]
└── Task F4: Scope Fidelity Check [deep]

Critical Path: Task 1 → Task 7 → Task 10 → Final
Parallel Speedup: ~65% faster than sequential
Max Concurrent: 6 (Wave 1)
```

### Dependency Matrix

| Task | Depends On | Blocks | Wave |
|------|-----------|--------|------|
| 1 | — | 7, 8, 10 | 1 |
| 2 | — | 7, 9, 10, 12 | 1 |
| 3 | — | 7 | 1 |
| 4 | — | 7 | 1 |
| 5 | — | 9 | 1 |
| 6 | — | 7 | 1 |
| 7 | 1, 2, 3, 4, 6 | 10 | 2 |
| 8 | 1, 2 | 10 | 2 |
| 9 | 2, 5 | 10 | 2 |
| 10 | 7, 8, 9 | 11, 13 | 3 |
| 11 | 10 | — | 3 |
| 12 | 2 | — | 4 |
| 13 | 10 | — | 4 |

### Agent Dispatch Summary

- **Wave 1**: **6 tasks** — T1 → `deep`, T2 → `quick`, T3 → `unspecified-high`, T4 → `unspecified-high`, T5 → `unspecified-high`, T6 → `quick`
- **Wave 2**: **3 tasks** — T7 → `deep`, T8 → `unspecified-high`, T9 → `unspecified-high`
- **Wave 3**: **2 tasks** — T10 → `deep`, T11 → `quick`
- **Wave 4**: **2 tasks** — T12 → `quick`, T13 → `quick`
- **FINAL**: **4 tasks** — F1 → `oracle`, F2 → `unspecified-high`, F3 → `unspecified-high`, F4 → `deep`

---

## TODOs

> Implementation + Test = ONE Task. EVERY task MUST have: Agent Profile + Parallelization + QA Scenarios.

- [ ] 1. LS Binary API 리서치 + 문서화

  **What to do**:
  - LS 바이너리(`language_server_linux_x64`)를 실제 실행하여 로컬 HTTP API 형식 역분석
  - 바이너리 실행 시 필요한 환경 변수, 인자, 데이터 디렉토리 확인
  - 로컬 HTTP 서버의 포트, 엔드포인트 URL 패턴, 요청/응답 JSON 형식 기록
  - `SendUserCascadeMessage` (스트리밍 생성), `GetCommandModelConfigs` (모델 목록) 등 API 메서드 문서화
  - 토큰 전달 방식 확인: JSON body `metadata.apiKey` vs HTTP header
  - 현재 executor의 `buildRequest()` (line 1299-1418)가 생성하는 요청 형식과 LS API 형식 차이 매핑
  - 결과를 `.sisyphus/notepads/ls-binary-api-spec.md`에 기록

  **Must NOT do**:
  - 코드 구현 (리서치만)
  - LS 바이너리 수정

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 바이너리 역분석은 깊은 탐구와 자율적 문제해결이 필요
  - **Skills**: []
  - **Skills Evaluated but Omitted**:
    - `playwright`: 브라우저 불필요

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 2, 3, 4, 5, 6)
  - **Blocks**: Tasks 7, 8, 10
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `/home/jc01rho/git/zerogravity/Dockerfile:12-34` — LS 바이너리 다운로드 URL 및 추출 경로 (Google CDN에서 tar.gz 다운로드 후 특정 경로에서 바이너리 추출)
  - `/home/jc01rho/git/zerogravity/README.md:1-325` — LS 바이너리 관련 환경 변수 (`ZEROGRAVITY_LS_PATH`, `ZEROGRAVITY_DATA_DIR`, `ZEROGRAVITY_APP_ROOT`), 포트 (8742), API 메서드명, 토큰 위치
  - `/home/jc01rho/git/zerogravity/.github/workflows/safety-check.yml` — LS 바이너리 내부 기술 스택 (BoringSSL, JA3/JA4)

  **API/Type References**:
  - `CLIProxyAPIPlus/internal/runtime/executor/antigravity_executor.go:1299-1418` — 현재 `buildRequest()` 함수: URL 구성, payload 변환(`geminiToAntigravity`), 헤더 설정. LS API와의 차이를 이 코드 기준으로 매핑해야 함
  - `CLIProxyAPIPlus/internal/runtime/executor/antigravity_executor.go:38-52` — 현재 상수들: base URL, API path, client ID/secret. LS 방식에서는 불필요해지는 항목 파악용

  **Acceptance Criteria**:
  - [ ] `.sisyphus/notepads/ls-binary-api-spec.md` 파일 생성됨
  - [ ] LS 바이너리 실행 방법 (CLI args, env vars) 기록됨
  - [ ] 최소 2개 API 메서드 (채팅 생성, 모델 목록)의 요청/응답 형식 기록됨
  - [ ] 토큰 전달 방식 확인됨

  **QA Scenarios**:

  ```
  Scenario: LS 바이너리 실행 가능 확인
    Tool: Bash
    Preconditions: Google CDN에서 LS 바이너리 다운로드 완료
    Steps:
      1. chmod +x language_server_linux_x64
      2. ZEROGRAVITY_DATA_DIR=/tmp/.agcache ./language_server_linux_x64 --help 또는 단독 실행
      3. 프로세스 시작 여부 확인 (ps aux | grep language_server)
      4. 로컬 포트 리스닝 확인 (ss -tlnp | grep 8742 또는 동적 포트)
    Expected Result: 프로세스가 시작되고 HTTP 포트 리스닝
    Failure Indicators: 프로세스 즉시 종료, 포트 미개방
    Evidence: .sisyphus/evidence/task-1-ls-binary-startup.txt

  Scenario: LS API 요청/응답 형식 확인
    Tool: Bash (curl)
    Preconditions: LS 바이너리 실행 중
    Steps:
      1. curl -s http://127.0.0.1:{port}/ — 기본 엔드포인트 응답 확인
      2. curl -s -X POST http://127.0.0.1:{port}/{api-path} -H "Content-Type: application/json" -d '{"test": true}' — API 메서드 탐색
      3. 응답 JSON 구조 기록
    Expected Result: API 엔드포인트와 요청/응답 형식이 문서화됨
    Failure Indicators: 모든 엔드포인트에서 404 또는 연결 거부
    Evidence: .sisyphus/evidence/task-1-ls-api-format.json
  ```

  **Commit**: YES (groups with Wave 1)
  - Message: `docs(antigravity): document LS binary API specification`
  - Files: `.sisyphus/notepads/ls-binary-api-spec.md`

- [ ] 2. Config 스키마 확장 (LS 설정 필드)

  **What to do**:
  - `internal/config/config.go`에 LS 관련 설정 구조체 추가:
    ```go
    type AntigravityLSConfig struct {
        Enabled     bool   `yaml:"enabled" json:"enabled"`           // LS 모드 활성화
        BinaryPath  string `yaml:"binary-path" json:"binary-path"`  // 수동 경로 지정
        Version     string `yaml:"version" json:"version"`           // 다운로드 버전
        DataDir     string `yaml:"data-dir" json:"data-dir"`         // LS 데이터 디렉토리
        StateDBPath string `yaml:"state-db-path" json:"state-db-path"` // state.vscdb 경로
        Port        int    `yaml:"port" json:"port"`                 // 고정 포트 (0=동적)
        AutoDownload bool  `yaml:"auto-download" json:"auto-download"` // 자동 다운로드
    }
    ```
  - `Config` 구조체에 필드 추가: `AntigravityLS AntigravityLSConfig \`yaml:"antigravity-ls" json:"antigravity-ls"\``
  - 기본값 설정: `Enabled=true`, `Version="1.18.3-4739469533380608"`, `DataDir="/tmp/.agcache"`, `Port=0`, `AutoDownload=true`

  **Must NOT do**:
  - 기존 설정 필드 변경
  - 설정 로딩 로직 수정 (YAML 파싱은 태그 기반 자동)

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 단일 파일 필드 추가, 간단한 작업
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 3, 4, 5, 6)
  - **Blocks**: Tasks 7, 9, 10, 13
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `CLIProxyAPIPlus/internal/config/config.go:27-100` — 기존 Config 구조체 패턴: YAML 태그 (kebab-case), JSON 태그, 인라인 주석 스타일 참조
  - `CLIProxyAPIPlus/internal/config/config.go:36` — `TLS TLSConfig` 중첩 구조체 패턴 — 동일한 방식으로 `AntigravityLS` 추가

  **External References**:
  - `/home/jc01rho/git/zerogravity/README.md:230-260` — ZeroGravity 환경 변수 목록에서 LS 관련 설정 필드 영감

  **Acceptance Criteria**:
  - [ ] `go build ./...` 성공
  - [ ] `AntigravityLSConfig` 구조체가 `internal/config/config.go`에 정의됨
  - [ ] `Config.AntigravityLS` 필드가 YAML 태그 `antigravity-ls`로 추가됨

  **QA Scenarios**:

  ```
  Scenario: 설정 구조체 빌드 확인
    Tool: Bash
    Preconditions: Config 수정 완료
    Steps:
      1. cd CLIProxyAPIPlus && go build ./...
      2. go vet ./internal/config/...
    Expected Result: 빌드 성공, vet 경고 없음
    Failure Indicators: 컴파일 에러, 타입 불일치
    Evidence: .sisyphus/evidence/task-2-build-success.txt

  Scenario: YAML 파싱 검증
    Tool: Bash
    Preconditions: 빌드 성공
    Steps:
      1. echo 'antigravity-ls:\n  enabled: true\n  version: "1.18.3"\n  auto-download: true' > /tmp/test-config.yaml
      2. go test -run TestConfigParsing ./internal/config/... (기존 테스트가 있다면)
    Expected Result: YAML 필드가 정상 파싱됨
    Failure Indicators: 파싱 에러, 필드 누락
    Evidence: .sisyphus/evidence/task-2-yaml-parsing.txt
  ```

  **Commit**: YES (groups with Wave 1)
  - Message: `feat(config): add antigravity-ls configuration schema`
  - Files: `internal/config/config.go`

- [ ] 3. LS Binary Detector (플랫폼별 자동 감지)

  **What to do**:
  - `internal/runtime/executor/ls_detector.go` 생성
  - 플랫폼별 Antigravity 설치 경로에서 LS 바이너리 탐색:
    - **Linux x64**: `~/.local/share/Antigravity/resources/app/extensions/antigravity/bin/language_server_linux_x64`, `/usr/share/antigravity/...`, `/opt/Antigravity/...`
    - **Linux ARM**: 동일 경로, `language_server_linux_arm`
    - **macOS ARM**: `/Applications/Antigravity.app/Contents/Resources/app/extensions/antigravity/bin/language_server_darwin_arm64`
    - **Windows x64**: `%LOCALAPPDATA%\Programs\Antigravity\resources\app\extensions\antigravity\bin\language_server_windows_x64.exe`
  - `DetectLSBinary() (string, error)` — OS/arch 기반 자동 탐지 함수
  - `runtime.GOOS`, `runtime.GOARCH` 사용
  - 파일 존재 여부 + 실행 권한 검증

  **Must NOT do**:
  - 다운로드 로직 (Task 4)
  - 프로세스 시작 (Task 7)

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: 멀티 플랫폼 경로 관리는 중간 복잡도
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 2, 4, 5, 6)
  - **Blocks**: Task 7
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `/home/jc01rho/git/zerogravity/scripts/setup-linux.sh` — ZeroGravity의 LS 바이너리 탐지 로직 (bash 스크립트), 탐색 경로 목록, 우선순위
  - `/home/jc01rho/git/zerogravity/Dockerfile:18-29` — tar 추출 경로: `Antigravity/resources/app/extensions/antigravity/bin/language_server_linux_x64`

  **API/Type References**:
  - `CLIProxyAPIPlus/internal/config/config.go` — `AntigravityLSConfig.BinaryPath` 필드: 사용자 수동 지정 경로 우선 사용

  **Acceptance Criteria**:
  - [ ] `go build ./...` 성공
  - [ ] `ls_detector.go` 파일 생성됨
  - [ ] `DetectLSBinary()` 함수가 현재 플랫폼에서 올바른 경로 반환 (또는 미설치 시 에러)
  - [ ] 단위 테스트 `ls_detector_test.go` 작성됨

  **QA Scenarios**:

  ```
  Scenario: Linux에서 LS 바이너리 감지
    Tool: Bash
    Preconditions: Antigravity 설치 여부에 따라 결과 다름
    Steps:
      1. cd CLIProxyAPIPlus && go test -run TestDetectLSBinary -v ./internal/runtime/executor/...
      2. 반환된 경로 확인
    Expected Result: 설치 시 유효한 바이너리 경로 반환, 미설치 시 "not found" 에러
    Failure Indicators: 패닉, nil pointer, 잘못된 경로
    Evidence: .sisyphus/evidence/task-3-detect-linux.txt

  Scenario: 수동 경로 지정 우선
    Tool: Bash
    Preconditions: Config에 BinaryPath 설정됨
    Steps:
      1. 수동 경로가 설정된 경우 자동 감지 스킵 확인 (단위 테스트)
    Expected Result: 수동 경로가 우선 반환됨
    Failure Indicators: 자동 감지가 수동 설정을 무시
    Evidence: .sisyphus/evidence/task-3-manual-path.txt
  ```

  **Commit**: YES (groups with Wave 1)
  - Message: `feat(antigravity): add LS binary detector for all platforms`
  - Files: `internal/runtime/executor/ls_detector.go`, `internal/runtime/executor/ls_detector_test.go`

- [ ] 4. LS Binary Downloader (Google CDN 다운로드)

  **What to do**:
  - `internal/runtime/executor/ls_downloader.go` 생성
  - Google CDN에서 Antigravity tar.gz 다운로드 후 LS 바이너리 추출
  - URL 패턴: `https://edgedl.me.gvt1.com/edgedl/release2/j0qc3/antigravity/stable/{version}/{platform}/Antigravity.tar.gz`
  - 플랫폼 매핑: `amd64 → linux-x64`, `arm64 → linux-arm`, `darwin/arm64 → darwin-arm64`, `windows/amd64 → win32-x64`
  - tar.gz 내 추출 경로: `Antigravity/resources/app/extensions/antigravity/bin/{binary_name}`
  - `DownloadLSBinary(ctx, version, destDir string) (string, error)` — 다운로드 + 추출 + chmod +x
  - 진행률 로깅 (logrus)
  - HTTP 클라이언트: config에 프록시 설정이 있으면 프록시 경유
  - 중복 다운로드 방지: 대상 경로에 이미 바이너리 존재하면 스킵
  - 실패 시 부분 파일 정리

  **Must NOT do**:
  - 자동 버전 업데이트 체크
  - 체크섬 검증 (Google CDN 제공 안함)

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: HTTP 다운로드 + tar 추출 + 에러 처리는 중간 복잡도
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 2, 3, 5, 6)
  - **Blocks**: Task 7
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `/home/jc01rho/git/zerogravity/Dockerfile:12-34` — Google CDN URL, tar 추출 명령어, 플랫폼별 바이너리명. 이 Docker RUN 명령을 Go 코드로 재구현
  - `/home/jc01rho/git/zerogravity/scripts/setup-linux.sh` — 네이티브 환경 다운로드 로직 (curl + tar), 폴백 경로

  **API/Type References**:
  - `CLIProxyAPIPlus/internal/runtime/executor/proxy_helpers.go:41-50` — `newProxyAwareHTTPClient` 프록시 설정. 다운로드에도 프록시를 적용해야 함

  **External References**:
  - Google CDN URL: `https://edgedl.me.gvt1.com/edgedl/release2/j0qc3/antigravity/stable/1.18.3-4739469533380608/linux-x64/Antigravity.tar.gz`

  **Acceptance Criteria**:
  - [ ] `go build ./...` 성공
  - [ ] `ls_downloader.go` 파일 생성됨
  - [ ] `DownloadLSBinary()` 함수 정의됨
  - [ ] 단위 테스트 존재 (mock HTTP 서버 사용)

  **QA Scenarios**:

  ```
  Scenario: LS 바이너리 다운로드 성공
    Tool: Bash
    Preconditions: 인터넷 연결 가능, /tmp/ls-test 디렉토리 존재
    Steps:
      1. go test -run TestDownloadLSBinary -v ./internal/runtime/executor/... -count=1
      2. 또는 직접 실행하여 /tmp/ls-test/language_server_linux_x64 생성 확인
    Expected Result: 바이너리가 다운로드되고 실행 권한 설정됨
    Failure Indicators: 다운로드 실패, 추출 실패, 파일 미생성
    Evidence: .sisyphus/evidence/task-4-download-success.txt

  Scenario: 이미 존재하는 바이너리 스킵
    Tool: Bash
    Preconditions: 대상 경로에 바이너리 이미 존재
    Steps:
      1. 동일 대상으로 DownloadLSBinary 재호출
      2. 다운로드 스킵 로그 확인
    Expected Result: "already exists, skipping download" 로그 출력, 재다운로드 안 함
    Failure Indicators: 불필요한 재다운로드 발생
    Evidence: .sisyphus/evidence/task-4-skip-existing.txt
  ```

  **Commit**: YES (groups with Wave 1)
  - Message: `feat(antigravity): add LS binary downloader from Google CDN`
  - Files: `internal/runtime/executor/ls_downloader.go`, `internal/runtime/executor/ls_downloader_test.go`

- [ ] 5. state.vscdb Reader (SQLite 토큰 리더)

  **What to do**:
  - `internal/runtime/executor/ls_vscdb.go` 생성
  - Antigravity의 state.vscdb (SQLite) 에서 OAuth refresh token 읽기
  - 플랫폼별 기본 경로:
    - Linux: `~/.config/Antigravity/User/globalStorage/state.vscdb`
    - macOS: `~/Library/Application Support/Antigravity/User/globalStorage/state.vscdb`
    - Windows: `%APPDATA%\Antigravity\User\globalStorage\state.vscdb`
  - SQLite 읽기: `github.com/mattn/go-sqlite3` 또는 `modernc.org/sqlite` (CGO-free 추천)
  - state.vscdb 테이블 구조 탐색 → OAuth 토큰이 저장된 key/value 찾기
  - `ReadVSCDBToken(dbPath string) (*VscdbToken, error)` 반환 구조체에 access_token, refresh_token, expiry 포함
  - SQLite 읽기 전용 모드 (`?mode=ro`) 사용 — Antigravity IDE와 동시 접근 시 잠금 방지
  - 토큰 로깅 시 반드시 마스킹

  **Must NOT do**:
  - state.vscdb에 쓰기 (읽기 전용)
  - 토큰 갱신 로직 (Task 9에서 구현)
  - CGO 의존성 강제 (CGO-free SQLite 라이브러리 사용)

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: SQLite 통합 + 플랫폼별 경로 처리
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 2, 3, 4, 6)
  - **Blocks**: Task 9
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `/home/jc01rho/git/zerogravity/README.md:170-215` — state.vscdb 경로 (플랫폼별), 용도 설명, Docker 마운트 패턴

  **API/Type References**:
  - `CLIProxyAPIPlus/internal/runtime/executor/antigravity_executor.go:1420-1438` — `tokenExpiry()` 함수: 기존 토큰 만료 처리 패턴 참조. metadata에서 만료 시간 추출하는 방식을 state.vscdb 토큰에도 적용

  **External References**:
  - `modernc.org/sqlite` — CGO-free SQLite 라이브러리 (Docker Alpine에서 CGO 없이 빌드 가능)

  **Acceptance Criteria**:
  - [ ] `go build ./...` 성공 (CGO_ENABLED=0 포함)
  - [ ] `ls_vscdb.go` 파일 생성됨
  - [ ] `ReadVSCDBToken()` 함수 정의됨
  - [ ] state.vscdb 미존재 시 명확한 에러 반환
  - [ ] 토큰 값 로깅 시 마스킹 확인

  **QA Scenarios**:

  ```
  Scenario: state.vscdb 토큰 읽기 (파일 존재 시)
    Tool: Bash
    Preconditions: Antigravity 설치 + 로그인 상태 (state.vscdb 존재)
    Steps:
      1. go test -run TestReadVSCDBToken -v ./internal/runtime/executor/...
      2. 반환된 토큰 구조체 검증 (access_token 비어있지 않음)
    Expected Result: VscdbToken 구조체에 refresh_token 포함
    Failure Indicators: SQLite 열기 실패, 토큰 키 못찾음
    Evidence: .sisyphus/evidence/task-5-vscdb-read.txt

  Scenario: state.vscdb 미존재 시 에러
    Tool: Bash
    Preconditions: 존재하지 않는 경로 지정
    Steps:
      1. ReadVSCDBToken("/nonexistent/path/state.vscdb") 호출
    Expected Result: "state.vscdb not found" 또는 유사 에러 반환, 패닉 없음
    Failure Indicators: 패닉, nil pointer
    Evidence: .sisyphus/evidence/task-5-vscdb-notfound.txt
  ```

  **Commit**: YES (groups with Wave 1)
  - Message: `feat(antigravity): add state.vscdb SQLite token reader`
  - Files: `internal/runtime/executor/ls_vscdb.go`, `internal/runtime/executor/ls_vscdb_test.go`, `go.mod` (sqlite 의존성)

- [ ] 6. Port Allocator (동적 포트 할당)

  **What to do**:
  - `internal/runtime/executor/ls_port.go` 생성
  - `FindFreePort() (int, error)` — OS에서 사용 가능한 포트 할당
  - TCP 리스너를 열어 OS가 할당한 포트 가져오기 → 즉시 닫기 → 포트 번호 반환
  - 포트 범위 제한 (1024-65535)
  - 간단한 유틸리티 — 10줄 내외

  **Must NOT do**:
  - 포트 점유 유지 (할당만)
  - 복잡한 포트 관리 로직

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 매우 간단한 단일 함수, 10줄 내외
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 2, 3, 4, 5)
  - **Blocks**: Task 7
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - Go 표준 라이브러리 `net.Listen("tcp", ":0")` 패턴 — OS 동적 포트 할당의 관용적 방법

  **Acceptance Criteria**:
  - [ ] `go build ./...` 성공
  - [ ] `FindFreePort()` 함수가 1024-65535 범위의 포트 반환
  - [ ] 반환된 포트가 실제로 사용 가능 (리스닝 테스트)

  **QA Scenarios**:

  ```
  Scenario: 포트 할당 성공
    Tool: Bash
    Preconditions: 없음
    Steps:
      1. go test -run TestFindFreePort -v ./internal/runtime/executor/...
      2. 반환된 포트 번호가 1024-65535 범위인지 확인
      3. 해당 포트로 TCP 리스너 열기 가능한지 확인
    Expected Result: 유효한 포트 번호 반환, 실제 사용 가능
    Failure Indicators: 포트 0 반환, 범위 밖 포트
    Evidence: .sisyphus/evidence/task-6-port-alloc.txt
  ```

  **Commit**: YES (groups with Wave 1)
  - Message: `feat(antigravity): add dynamic port allocator`
  - Files: `internal/runtime/executor/ls_port.go`

- [ ] 7. LS Process Manager (서브프로세스 라이프사이클)

  **What to do**:
  - `internal/runtime/executor/ls_process.go` 생성
  - LS 바이너리를 서브프로세스로 시작/정지/모니터링하는 매니저:
    ```go
    type LSProcessManager struct {
        cfg       *config.Config
        mu        sync.Mutex
        cmd       *exec.Cmd
        port      int
        ready     chan struct{}
        stopped   bool
    }
    ```
  - `NewLSProcessManager(cfg)` — 생성자
  - `Start(ctx) error` — LS 바이너리 시작:
    1. Config에서 BinaryPath 확인 → 없으면 DetectLSBinary() → 없으면 DownloadLSBinary()
    2. FindFreePort()로 포트 할당 (Config.Port가 0이면)
    3. `exec.CommandContext(ctx, binaryPath, args...)` 로 서브프로세스 시작
    4. 환경 변수 설정: `ZEROGRAVITY_DATA_DIR`, 포트 관련
    5. stdout/stderr를 logrus로 파이프
    6. 헬스체크 대기 (HTTP GET to localhost:{port}, 최대 30초)
  - `Stop() error` — SIGTERM → SIGKILL 순차 정지
  - `Port() int` — 현재 포트 반환
  - `EnsureRunning(ctx) error` — 프로세스 살아있는지 확인, 죽었으면 재시작
  - 크래시 복구: 지수 백오프 (1s, 2s, 4s, 8s, 16s), 최대 5회 재시작
  - 좀비 프로세스 방지: `cmd.Wait()` 고루틴에서 호출
  - 동시성 안전: `sync.Mutex` 보호

  **Must NOT do**:
  - UID 격리 (zerogravity-ls user)
  - API 요청 처리 (Task 8)
  - 토큰 관리 (Task 9)

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 서브프로세스 라이프사이클, 크래시 복구, 동시성은 복잡한 로직
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 8, 9)
  - **Blocks**: Task 10
  - **Blocked By**: Tasks 1, 2, 3, 4, 6

  **References**:

  **Pattern References**:
  - `/home/jc01rho/git/zerogravity/Dockerfile:82-98` — LS 바이너리 실행 환경 (경로, 환경 변수, 데이터 디렉토리). Docker 내에서 LS가 어떤 환경으로 실행되는지 참조
  - `/home/jc01rho/git/zerogravity/README.md:230-260` — 환경 변수 목록: `ZEROGRAVITY_LS_PATH`, `ZEROGRAVITY_DATA_DIR`, `ZEROGRAVITY_APP_ROOT`. 프로세스 시작 시 이 환경 변수들을 설정해야 할 수 있음

  **API/Type References**:
  - `CLIProxyAPIPlus/internal/config/config.go` — `AntigravityLSConfig` (Task 2에서 추가): BinaryPath, DataDir, Port 읽기
  - `internal/runtime/executor/ls_detector.go` (Task 3) — `DetectLSBinary()` 호출하여 바이너리 경로 확보
  - `internal/runtime/executor/ls_downloader.go` (Task 4) — `DownloadLSBinary()` 호출하여 바이너리 다운로드
  - `internal/runtime/executor/ls_port.go` (Task 6) — `FindFreePort()` 호출

  **Acceptance Criteria**:
  - [ ] `go build ./...` 성공
  - [ ] `ls_process.go` 파일 생성됨
  - [ ] `Start()`, `Stop()`, `EnsureRunning()` 메서드 구현됨
  - [ ] 크래시 복구 로직 (지수 백오프) 구현됨

  **QA Scenarios**:

  ```
  Scenario: LS 프로세스 시작 + 헬스체크
    Tool: Bash
    Preconditions: LS 바이너리 사용 가능 (다운로드 또는 감지됨)
    Steps:
      1. go test -run TestLSProcessStart -v ./internal/runtime/executor/... -timeout 60s
      2. 프로세스 시작 → 포트 리스닝 확인
      3. HTTP 헬스체크 성공 확인
    Expected Result: 프로세스가 시작되고 30초 내 헬스체크 통과
    Failure Indicators: 시작 실패, 헬스체크 타임아웃
    Evidence: .sisyphus/evidence/task-7-process-start.txt

  Scenario: 크래시 복구
    Tool: Bash
    Preconditions: LS 프로세스 실행 중
    Steps:
      1. 프로세스 PID 확인
      2. kill -9 {PID}
      3. EnsureRunning() 호출
      4. 프로세스 재시작 확인
    Expected Result: 크래시 감지 → 자동 재시작 → 새 프로세스 실행 중
    Failure Indicators: 재시작 실패, 좀비 프로세스
    Evidence: .sisyphus/evidence/task-7-crash-recovery.txt
  ```

  **Commit**: YES (groups with Wave 2)
  - Message: `feat(antigravity): add LS process manager with crash recovery`
  - Files: `internal/runtime/executor/ls_process.go`, `internal/runtime/executor/ls_process_test.go`

- [x] 8. LS HTTP Client (LS 로컬 API 통신)

  **What to do**:
  - `internal/runtime/executor/ls_client.go` 생성
  - LS 바이너리의 로컬 HTTP API와 통신하는 클라이언트:
    ```go
    type LSClient struct {
        baseURL    string  // http://127.0.0.1:{port}
        httpClient *http.Client
    }
    ```
  - `NewLSClient(port int) *LSClient` — 로컬 HTTP 클라이언트 생성 (프록시 불필요, localhost 직접 연결)
  - `SendRequest(ctx, path string, body []byte, stream bool) (*http.Response, error)` — LS API 요청
  - `SetToken(ctx, token string) error` — LS에 토큰 설정 (API 엔드포인트 또는 환경 변수 방식)
  - `HealthCheck(ctx) error` — 헬스체크
  - 요청 형식: Task 1의 리서치 결과에 따라 구현
    - 현재 예상: 기존 Antigravity API 형식과 동일하되 base URL만 localhost로 변경
    - 토큰 전달: `metadata.apiKey` in JSON body (Authorization 헤더 대신)
  - 스트리밍 응답 처리: `text/event-stream` SSE 파싱 (기존 ExecuteStream 로직 재사용 가능)
  - 타임아웃: 연결 10s, 헤더 응답 60s (기존 `proxy_helpers.go` 상수 참조)

  **Must NOT do**:
  - 프로세스 관리 (Task 7)
  - 프로토콜 변환 (기존 translator 시스템 사용)

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: HTTP 클라이언트 + 스트리밍 처리는 중간 복잡도
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 7, 9)
  - **Blocks**: Task 10
  - **Blocked By**: Tasks 1, 2

  **References**:

  **Pattern References**:
  - `CLIProxyAPIPlus/internal/runtime/executor/proxy_helpers.go:24-39` — HTTP 클라이언트 타임아웃 상수. LS 클라이언트는 localhost이므로 더 짧은 타임아웃 사용 가능하지만 동일 패턴 따르기
  - `CLIProxyAPIPlus/internal/runtime/executor/antigravity_executor.go:1375-1386` — 현재 HTTP 요청 구성 패턴 (Content-Type, Accept 헤더 등). LS 클라이언트에도 동일 적용

  **API/Type References**:
  - `.sisyphus/notepads/ls-binary-api-spec.md` (Task 1 산출물) — LS API 형식, 엔드포인트, 토큰 전달 방식의 정확한 스펙

  **Acceptance Criteria**:
  - [ ] `go build ./...` 성공
  - [ ] `ls_client.go` 파일 생성됨
  - [ ] `SendRequest()`, `SetToken()`, `HealthCheck()` 메서드 구현됨

  **QA Scenarios**:

  ```
  Scenario: LS 헬스체크 성공
    Tool: Bash
    Preconditions: LS 프로세스 실행 중 (Task 7)
    Steps:
      1. go test -run TestLSClientHealthCheck -v ./internal/runtime/executor/...
    Expected Result: 헬스체크 성공 (nil 에러)
    Failure Indicators: 연결 거부, 타임아웃
    Evidence: .sisyphus/evidence/task-8-healthcheck.txt

  Scenario: LS에 요청 전송 실패 (프로세스 미실행)
    Tool: Bash
    Preconditions: LS 프로세스 미실행
    Steps:
      1. 존재하지 않는 포트로 SendRequest() 호출
    Expected Result: "connection refused" 에러 반환
    Failure Indicators: 패닉, 무한 대기
    Evidence: .sisyphus/evidence/task-8-connection-refused.txt
  ```

  **Commit**: YES (groups with Wave 2)
  - Message: `feat(antigravity): add LS HTTP client for local API`
  - Files: `internal/runtime/executor/ls_client.go`, `internal/runtime/executor/ls_client_test.go`

- [ ] 9. Token Provider (통합 토큰 소스)

  **What to do**:
  - `internal/runtime/executor/ls_token.go` 생성
  - 토큰 취득 우선순위를 관리하는 통합 제공자:
    1. Auth 객체의 기존 access_token (config/OAuth 로그인으로 취득)
    2. state.vscdb의 refresh_token → access_token 갱신
    3. 기존 OAuth 갱신 플로우 (현재 `ensureAccessToken` 로직)
  - `TokenProvider` 구조체:
    ```go
    type TokenProvider struct {
        cfg      *config.Config
        vscdb    *VscdbReader // Task 5
        mu       sync.RWMutex
        cached   string       // 캐시된 access token
        expiry   time.Time
    }
    ```
  - `GetToken(ctx, auth) (string, error)` — 우선순위에 따라 유효한 토큰 반환
  - 기존 `ensureAccessToken()` (line 820-900)의 토큰 갱신 로직을 이 모듈로 이동/통합
  - state.vscdb에서 읽은 refresh_token으로 Google OAuth를 통해 access_token 갱신
  - 토큰 만료 3000초(refreshSkew) 전 자동 갱신

  **Must NOT do**:
  - OAuth 웹 로그인 플로우 (기존 auth/ 모듈이 담당)
  - 토큰 저장 (state.vscdb 읽기 전용)

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: 토큰 캐싱 + 갱신 + 멀티소스 통합은 중간 복잡도
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 7, 8)
  - **Blocks**: Task 10
  - **Blocked By**: Tasks 2, 5

  **References**:

  **Pattern References**:
  - `CLIProxyAPIPlus/internal/runtime/executor/antigravity_executor.go:820-900` — 현재 `ensureAccessToken()` 함수: 토큰 만료 확인, OAuth refresh, 캐싱 패턴. 이 로직을 Token Provider로 이동/통합
  - `CLIProxyAPIPlus/internal/runtime/executor/antigravity_executor.go:1420-1476` — `tokenExpiry()`, `metaStringValue()`, `int64Value()`: 토큰 메타데이터 파싱 유틸. 재사용 또는 이동

  **API/Type References**:
  - `internal/runtime/executor/ls_vscdb.go` (Task 5) — `ReadVSCDBToken()` 호출하여 refresh_token 획득
  - `CLIProxyAPIPlus/internal/auth/antigravity/auth.go` — OAuth 토큰 갱신 로직 (ExchangeCodeForTokens 등). 필요 시 호출

  **Acceptance Criteria**:
  - [ ] `go build ./...` 성공
  - [ ] `ls_token.go` 파일 생성됨
  - [ ] `GetToken()` 함수가 auth 토큰, state.vscdb, OAuth 순으로 시도
  - [ ] 토큰 캐싱 + 만료 전 갱신 동작

  **QA Scenarios**:

  ```
  Scenario: Auth 토큰 우선 사용
    Tool: Bash
    Preconditions: Auth 객체에 유효한 access_token 존재
    Steps:
      1. go test -run TestTokenProviderAuthFirst -v ./internal/runtime/executor/...
    Expected Result: Auth의 access_token 반환, state.vscdb 조회 안 함
    Failure Indicators: state.vscdb 불필요하게 조회
    Evidence: .sisyphus/evidence/task-9-auth-first.txt

  Scenario: state.vscdb 폴백
    Tool: Bash
    Preconditions: Auth 토큰 만료, state.vscdb에 유효한 refresh_token 존재
    Steps:
      1. go test -run TestTokenProviderVscdbFallback -v ./internal/runtime/executor/...
    Expected Result: state.vscdb의 refresh_token으로 새 access_token 획득
    Failure Indicators: 토큰 갱신 실패, 에러 미반환
    Evidence: .sisyphus/evidence/task-9-vscdb-fallback.txt
  ```

  **Commit**: YES (groups with Wave 2)
  - Message: `feat(antigravity): add unified token provider (auth + vscdb + OAuth)`
  - Files: `internal/runtime/executor/ls_token.go`, `internal/runtime/executor/ls_token_test.go`

- [x] 10. Antigravity Executor 재작성 (LS 기반)

  **What to do**:
  - `internal/runtime/executor/antigravity_executor.go` 대규모 리팩터링 (1,732줄 → LS 기반)
  - **핵심 변경사항**:
    1. `AntigravityExecutor` 구조체에 LS 모듈 통합:
       ```go
       type AntigravityExecutor struct {
           cfg          *config.Config
           processManager *LSProcessManager
           lsClient     *LSClient
           tokenProvider *TokenProvider
           mu           sync.Mutex
           initialized  bool
       }
       ```
    2. `NewAntigravityExecutor(cfg)` — LS 모듈 초기화 (lazy init)
    3. `Execute()` 재작성:
       - `processManager.EnsureRunning(ctx)` — LS 프로세스 확인/시작
       - `tokenProvider.GetToken(ctx, auth)` — 토큰 획득
       - `lsClient.SetToken(ctx, token)` — LS에 토큰 설정
       - 기존 translator 변환 로직 유지 (from → to antigravity 변환)
       - `lsClient.SendRequest()` — LS 로컬 API로 요청 전송
       - 응답 처리: 기존 응답 파싱 + 사용량 리포팅 유지
    4. `ExecuteStream()` 재작성 — 동일 패턴, 스트리밍 모드
    5. `buildRequest()` 수정:
       - base URL: `http://127.0.0.1:{port}` (LS 로컬)
       - 토큰: `metadata.apiKey` in body (Authorization 헤더 대신, Task 1 리서치 결과에 따라)
       - User-Agent, Client ID/Secret: LS가 처리하므로 제거
    6. `HttpRequest()`, `PrepareRequest()` — LS 경유로 변경
  - **제거할 항목**:
    - 하드코딩된 상수: `antigravityBaseURLDaily`, `antigravitySandboxBaseURLDaily`, `antigravityClientID`, `antigravityClientSecret`, `defaultAntigravityAgent`
    - `antigravityBaseURLFallbackOrder()` — LS가 처리
    - 직접 HTTP 클라이언트 생성 (`newProxyAwareHTTPClient` for upstream calls) — LS가 처리
    - OAuth 토큰 갱신 로직 (TokenProvider로 이동됨)
  - **유지할 항목**:
    - `geminiToAntigravity()` — payload 변환 로직 (LS API 형식에 맞게 미세 조정 가능)
    - `generateRequestID()`, `generateStableSessionID()`, `generateProjectID()` — ID 생성
    - `antigravityRetryAttempts()`, `antigravityWait()` — 리트라이 로직
    - translator 통합 (`sdktranslator.TranslateRequest`)
    - usage reporter 통합

  **Must NOT do**:
  - 기존 Go HTTP 직접 호출과 병행 (완전 교체만)
  - translator 시스템 변경
  - 새 API 기능 추가
  - 과도한 추상화 — 기존 코드 패턴 유지

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 1,732줄 파일의 핵심 로직 재작성, 다수 모듈 통합, 높은 복잡도
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 3 (with Task 11)
  - **Blocks**: Tasks 11, 12, 14
  - **Blocked By**: Tasks 7, 8, 9

  **References**:

  **Pattern References**:
  - `CLIProxyAPIPlus/internal/runtime/executor/antigravity_executor.go` (전체) — 현재 executor의 전체 구조. Execute(), ExecuteStream(), buildRequest(), executeClaudeNonStream(), executeClaudeStream()의 흐름을 LS 기반으로 변환
  - `CLIProxyAPIPlus/internal/runtime/executor/claude_executor.go` — 다른 executor 구현 참조 (더 간단한 패턴)

  **API/Type References**:
  - `internal/runtime/executor/ls_process.go` (Task 7) — `LSProcessManager.EnsureRunning()`, `Port()`
  - `internal/runtime/executor/ls_client.go` (Task 8) — `LSClient.SendRequest()`, `SetToken()`
  - `internal/runtime/executor/ls_token.go` (Task 9) — `TokenProvider.GetToken()`
  - `sdk/cliproxy/executor/interfaces.go` — Executor 인터페이스: `Execute()`, `ExecuteStream()` 시그니처 유지 필수
  - `.sisyphus/notepads/ls-binary-api-spec.md` (Task 1 산출물) — LS API 상세 스펙

  **Acceptance Criteria**:
  - [ ] `go build ./...` 성공
  - [ ] 하드코딩된 상수 (`antigravityClientID`, `antigravityClientSecret`, `antigravityBaseURLDaily`) 제거됨
  - [ ] Execute/ExecuteStream이 LS 프로세스 경유로 동작
  - [ ] 기존 테스트 (`antigravity_executor_buildrequest_test.go`) 업데이트 + 통과
  - [ ] translator 호출 패턴 유지됨

  **QA Scenarios**:

  ```
  Scenario: Execute (non-streaming) via LS
    Tool: Bash
    Preconditions: LS 바이너리 사용 가능, 유효한 토큰 설정
    Steps:
      1. cd CLIProxyAPIPlus && go test -run TestAntigravityExecute -v ./internal/runtime/executor/... -timeout 120s
      2. 또는 서버 시작 후: curl -s -X POST http://localhost:8317/v1/chat/completions -H "Authorization: Bearer {mgmt-key}" -H "Content-Type: application/json" -d '{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"say hi"}]}'
    Expected Result: LS 경유로 응답 수신, choices[0].message.content 비어있지 않음
    Failure Indicators: "connection refused" to LS, 토큰 에러, translator 에러
    Evidence: .sisyphus/evidence/task-10-execute-nonstream.txt

  Scenario: ExecuteStream (streaming) via LS
    Tool: Bash
    Preconditions: LS 바이너리 실행 중, 유효한 토큰
    Steps:
      1. curl -s -N -X POST http://localhost:8317/v1/chat/completions -H "Authorization: Bearer {mgmt-key}" -H "Content-Type: application/json" -d '{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"count 1 to 5"}],"stream":true}'
      2. SSE 이벤트 수신 확인
    Expected Result: "data: {..." 형태의 SSE 이벤트 스트림 수신
    Failure Indicators: 빈 응답, 연결 끊김, SSE 파싱 에러
    Evidence: .sisyphus/evidence/task-10-execute-stream.txt

  Scenario: 하드코딩 상수 제거 확인
    Tool: Bash
    Preconditions: 리팩터링 완료
    Steps:
      1. grep -n "antigravityClientID\|antigravityClientSecret\|defaultAntigravityAgent\|daily-cloudcode-pa" CLIProxyAPIPlus/internal/runtime/executor/antigravity_executor.go
    Expected Result: 매칭 없음 (exit code 1)
    Failure Indicators: 하드코딩된 상수가 여전히 존재
    Evidence: .sisyphus/evidence/task-10-no-hardcoded-constants.txt
  ```

  **Commit**: YES (Wave 3)
  - Message: `refactor(antigravity): rewrite executor to use LS binary subprocess`
  - Files: `internal/runtime/executor/antigravity_executor.go`, `internal/runtime/executor/antigravity_executor_buildrequest_test.go`
  - Pre-commit: `cd CLIProxyAPIPlus && go build ./... && go test ./internal/runtime/executor/...`

- [x] 11. Usage Helpers 적응

  **What to do**:
  - `internal/runtime/executor/usage_helpers.go` 수정
  - LS 바이너리 응답 형식에 맞게 usage 파싱 적응:
    - `parseAntigravityUsage()` — LS 응답의 usage 필드 위치/형식 확인 및 조정
    - `parseAntigravityStreamUsage()` — 스트리밍 응답의 usage 파싱 조정
  - Task 1 리서치 결과에 따라 변경 범위 결정 (LS 응답이 기존과 동일하면 변경 최소)

  **Must NOT do**:
  - usage 보고 구조 변경
  - 새로운 usage 메트릭 추가

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 기존 코드 미세 조정, Task 1 결과에 따라 변경 없을 수 있음
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 3 (with Task 10, but depends on Task 10)
  - **Blocks**: None
  - **Blocked By**: Task 10

  **References**:

  **Pattern References**:
  - `CLIProxyAPIPlus/internal/runtime/executor/usage_helpers.go` — 현재 `parseAntigravityUsage()`, `parseAntigravityStreamUsage()` 함수: gjson으로 JSON 응답에서 usage 추출
  - `CLIProxyAPIPlus/internal/runtime/executor/usage_helpers_test.go` — 기존 테스트 케이스

  **Acceptance Criteria**:
  - [ ] `go test ./internal/runtime/executor/... -run TestUsage` 통과
  - [ ] LS 응답에서 usage 정보 정상 추출

  **QA Scenarios**:

  ```
  Scenario: Usage 파싱 테스트
    Tool: Bash
    Steps:
      1. go test -run TestParseAntigravityUsage -v ./internal/runtime/executor/...
    Expected Result: 모든 usage 테스트 통과
    Evidence: .sisyphus/evidence/task-11-usage-tests.txt
  ```

  **Commit**: YES (groups with Wave 3)
  - Message: `refactor(antigravity): adapt usage helpers for LS binary responses`
  - Files: `internal/runtime/executor/usage_helpers.go`, `internal/runtime/executor/usage_helpers_test.go`

- [x] 12. config.example.yaml 업데이트

  **What to do**:
  - `CLIProxyAPIPlus/config.example.yaml`에 `antigravity-ls` 섹션 추가:
    ```yaml
    # Antigravity LS Binary Configuration
    # Uses the actual Antigravity Language Server binary for authentic traffic patterns.
    antigravity-ls:
      enabled: true                          # Enable LS binary mode (default: true)
      # binary-path: ""                      # Manual LS binary path (auto-detected if empty)
      version: "1.18.3-4739469533380608"     # LS binary version to download
      data-dir: "/tmp/.agcache"              # LS data directory
      # state-db-path: ""                    # state.vscdb path (auto-detected if empty)
      port: 0                                # LS port (0 = dynamic allocation)
      auto-download: true                    # Auto-download if not found
    ```

  **Must NOT do**:
  - 기존 설정 항목 변경

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 단일 파일, YAML 섹션 추가
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 4 (with Task 13)
  - **Blocks**: None
  - **Blocked By**: Task 2

  **References**:

  **Pattern References**:
  - `CLIProxyAPIPlus/config.example.yaml` — 기존 설정 예시 스타일 (주석 포맷, 들여쓰기)

  **Acceptance Criteria**:
  - [ ] `antigravity-ls` 섹션이 config.example.yaml에 추가됨
  - [ ] 주석이 충분히 설명적

  **QA Scenarios**:

  ```
  Scenario: YAML 구문 유효성
    Tool: Bash
    Steps:
      1. python3 -c "import yaml; yaml.safe_load(open('CLIProxyAPIPlus/config.example.yaml'))"
    Expected Result: 파싱 성공, 에러 없음
    Failure Indicators: YAML 구문 오류
    Evidence: .sisyphus/evidence/task-12-yaml-valid.txt
  ```

  **Commit**: YES (groups with Wave 4)
  - Message: `docs(config): add antigravity-ls configuration example`
  - Files: `CLIProxyAPIPlus/config.example.yaml`

- [x] 13. AGENTS.md 문서 업데이트

  **What to do**:
  - `CLIProxyAPIPlus/AGENTS.md` 및 `CLIProxyAPIPlus/internal/AGENTS.md` 업데이트
  - LS 바이너리 아키텍처 설명 추가:
    - 새 파일 목록: `ls_process.go`, `ls_client.go`, `ls_token.go`, `ls_detector.go`, `ls_downloader.go`, `ls_vscdb.go`, `ls_port.go`
    - executor 시스템 문서 업데이트: Antigravity executor가 LS 바이너리 서브프로세스 경유
    - WHERE TO LOOK 테이블 업데이트
  - `AGENTS.md` (루트) 업데이트: Antigravity 아키텍처 변경 요약

  **Must NOT do**:
  - README.md 변경
  - 과도한 문서화

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 마크다운 문서 업데이트, 간단
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 4 (with Task 12)
  - **Blocks**: None
  - **Blocked By**: Task 10

  **References**:

  **Pattern References**:
  - `CLIProxyAPIPlus/AGENTS.md` — 현재 문서 구조 및 스타일
  - `CLIProxyAPIPlus/internal/AGENTS.md` — internal 패키지 문서 구조

  **Acceptance Criteria**:
  - [ ] LS 관련 파일 목록이 AGENTS.md에 추가됨
  - [ ] executor 시스템 설명이 LS 기반으로 업데이트됨

  **QA Scenarios**:

  ```
  Scenario: 문서 일관성 확인
    Tool: Bash
    Steps:
      1. grep "ls_process\|ls_client\|ls_token\|ls_detector\|ls_downloader\|ls_vscdb" CLIProxyAPIPlus/internal/AGENTS.md
    Expected Result: 모든 LS 관련 파일이 문서에 언급됨
    Failure Indicators: 파일 누락
    Evidence: .sisyphus/evidence/task-13-docs-complete.txt
  ```

  **Commit**: YES (groups with Wave 4)
  - Message: `docs: update AGENTS.md for LS binary architecture`
  - Files: `CLIProxyAPIPlus/AGENTS.md`, `CLIProxyAPIPlus/internal/AGENTS.md`, `AGENTS.md`

---

## Final Verification Wave (MANDATORY — after ALL implementation tasks)

> 4 review agents run in PARALLEL. ALL must APPROVE. Rejection → fix → re-run.

- [x] F1. **Plan Compliance Audit** — `oracle`
  Read the plan end-to-end. For each "Must Have": verify implementation exists (read file, run command). For each "Must NOT Have": search codebase for forbidden patterns — reject with file:line if found. Check evidence files exist in .sisyphus/evidence/. Compare deliverables against plan.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [x] F2. **Code Quality Review** — `unspecified-high`
  Run `go build ./...` + `go vet ./...` + `go test ./...`. Review all changed files for: empty error handling, `http.DefaultClient` usage, unmasked token logging, unused imports. Check AI slop: excessive comments, over-abstraction, generic names.
  Output: `Build [PASS/FAIL] | Vet [PASS/FAIL] | Tests [N pass/N fail] | Files [N clean/N issues] | VERDICT`

- [x] F3. **Real QA** — `unspecified-high`
  Start from clean state. LS 바이너리 다운로드 → 시작 → 토큰 설정 → API 요청 → 응답 확인. 크래시 복구 테스트. state.vscdb 토큰 갱신 테스트. Save to `.sisyphus/evidence/final-qa/`.
  Output: `Scenarios [N/N pass] | Integration [N/N] | Edge Cases [N tested] | VERDICT`

- [x] F4. **Scope Fidelity Check** — `deep`
  For each task: read "What to do", read actual diff (git log/diff). Verify 1:1 — everything in spec was built, nothing beyond spec was built. Check "Must NOT do" compliance. Flag unaccounted changes.
  Output: `Tasks [N/N compliant] | Contamination [CLEAN/N issues] | Unaccounted [CLEAN/N files] | VERDICT`

---

## Commit Strategy

| Wave | Commit Message | Files |
|------|---------------|-------|
| 1 | `feat(antigravity): add LS binary foundation modules` | ls_config changes, ls_detector.go, ls_downloader.go, ls_vscdb.go, ls_port.go |
| 2 | `feat(antigravity): add LS process manager and client` | ls_process.go, ls_client.go, ls_token.go |
| 3 | `refactor(antigravity): rewrite executor to use LS binary` | antigravity_executor.go, usage_helpers.go |
| 4 | `docs: update config example and AGENTS.md for LS architecture` | config.example.yaml, AGENTS.md |

---

## Success Criteria

### Verification Commands
```bash
go build ./cmd/server/                    # Expected: clean build
go test ./internal/runtime/executor/...   # Expected: all pass
go vet ./...                              # Expected: no issues
```

### Final Checklist
- [x] LS 바이너리가 서브프로세스로 시작되고 헬스체크 통과
- [ ] OpenAI 호환 API → LS 바이너리 경유 → Google API 플로우 동작 (통합 테스트 필요)
- [x] state.vscdb 토큰 자동 갱신 동작
- [x] LS 바이너리 크래시 시 자동 재시작
- [x] 기존 Go HTTP 직접 호출 코드 완전 제거
- [x] `http.DefaultClient` 사용 없음
- [x] 토큰 로깅 시 마스킹 적용
- [ ] 기존 빌드/실행 방식 그대로 동작 (`go build`, `./cliproxy -c config.yaml`)
