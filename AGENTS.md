# CLIPROXYAPIPLUS KNOWLEDGE BASE

**Updated:** 2026-04-15
**Commit:** 9be69e5f
**Branch:** master

## OVERVIEW

Go 1.26 기반 AI 프록시 서버. CLI/웹 OAuth, 다수 provider executor, translator, 관리 API, SDK를 한 저장소에서 유지한다.

## STRUCTURE

```text
CLIProxyAPIPlus/
├── cmd/                  # 서버/유틸 CLI 진입점
├── internal/             # 비공개 핵심 구현
├── sdk/                  # 임베딩 가능한 공개 SDK
├── auths/                # 기본 인증 저장 위치
├── test/                 # 통합 테스트
└── config.yaml           # 런타임 설정
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| 서버 부팅/플래그 | `cmd/server/main.go` | login 플래그, TUI, local-model |
| 관리 API | `internal/api/` | `/v0/management/*` |
| provider 인증 | `internal/auth/` | provider별 OAuth 구현 |
| 요청 실행 | `internal/runtime/executor/` | provider별 executor |
| 포맷 변환 | `internal/translator/` | source→target 프로토콜 변환 |
| 모델 라우팅 | `internal/registry/` | 정적 정의 + remote updater |
| 공개 임베딩 API | `sdk/` | Builder 기반 사용 |

## COMMANDS

```bash
gofmt -w .
go build ./cmd/server
go run ./cmd/server --config config.yaml
go test ./...
go test -run TestName ./path/to/pkg
```

주요 플래그: `--config`, `--tui`, `--standalone`, `--local-model`, `--no-browser`, `--oauth-callback-port`.

## CONVENTIONS

- Go 코드는 `gofmt`/goimports 스타일을 유지한다.
- 로그는 logrus structured logging을 사용하고, 토큰·쿠키·키는 마스킹한다.
- `internal/runtime/executor/`의 공용 헬퍼는 `helps/` 아래에 둔다.
- translator 단독 수정은 피하고, 관련 config/executor/handler와 함께 변경 의도를 맞춘다.
- 사용자 가시 문자열은 해당 영역의 기존 언어를 따른다. 코드 주석은 영어를 유지한다.

## ANTI-PATTERNS

- `http.DefaultClient` 직접 사용 금지.
- `log.Fatal`/`log.Fatalf`로 프로세스를 종료하지 않는다.
- HTTP handler에서 panic으로 흐름을 끊지 않는다.
- upstream 연결 이후 임의 타임아웃을 추가하지 않는다. 예외는 현재 구현이 명시한 websocket/liveness/management timeout 범위만 허용한다.

## SUB-DOCUMENTS

- `internal/AGENTS.md`
- `internal/api/AGENTS.md`
- `internal/auth/kiro/AGENTS.md`
- `internal/config/AGENTS.md`
- `internal/registry/AGENTS.md`
- `internal/runtime/executor/AGENTS.md`
- `internal/translator/AGENTS.md`
- `internal/util/AGENTS.md`
- `sdk/AGENTS.md`
- `sdk/cliproxy/AGENTS.md`
