# PROJECT KNOWLEDGE BASE

**Generated:** 2026-03-03
**Commit:** 18e06f0f
**Branch:** master
**Type:** Monorepo (Backend + Frontend + Dashboard)

## OVERVIEW

CLI Proxy API: AI 모델 프록시 시스템. Go 백엔드가 15+ AI 프로바이더(Claude, Gemini, OpenAI, Codex, Kiro, Antigravity, iFlow, Cline, Qwen, Kimi 등)를 OpenAI 호환 API로 통합. React 프론트엔드가 관리 대시보드 제공, Python/React 대시보드가 사용량 모니터링.

## REMOVED PROVIDERS

### Trae Provider (Removed 2026-02-09)
The Trae provider has been completely removed from the codebase. All Trae-related authentication, translation, and configuration code has been deleted from the backend. The provider is no longer supported or available in the API.

## STRUCTURE

```
cli-proxy/
├── CLIProxyAPIPlus/                  # Go 백엔드 (API 프록시 서버)
│   ├── cmd/server/                   # CLI 진입점
│   ├── internal/                     # 핵심 구현 (비공개)
│   │   ├── api/                      # HTTP 서버, 라우팅
│   │   ├── auth/                     # 프로바이더별 OAuth
│   │   ├── translator/               # 프로토콜 변환 엔진
│   │   └── runtime/executor/         # 요청 실행기
│   ├── sdk/                          # Public SDK
│   └── AGENTS.md                     # 백엔드 상세 문서
│
├── Cli-Proxy-API-Management-Center/  # React 프론트엔드 (관리 UI)
│   ├── src/
│   │   ├── pages/                    # 라우트별 페이지
│   │   ├── components/               # UI 컴포넌트
│   │   ├── services/api/             # 백엔드 API 클라이언트
│   │   ├── stores/                   # Zustand 상태 관리
│   │   └── types/                    # TypeScript 타입
│   └── AGENTS.md                     # 프론트엔드 상세 문서
│
└── CLIProxyAPI-Dashboard/            # 모니터링 대시보드
    ├── collector/                    # Python 수집기 (Flask + APScheduler)
    ├── frontend/                     # React 대시보드 (Vite)
    └── AGENTS.md                     # 대시보드 상세 문서
```

## BACKEND-FRONTEND INTEGRATION

### API 연결

| Frontend | Backend Endpoint | Purpose |
|----------|------------------|---------|
| `services/api/providers.ts` | `/v0/management/auths/*` | 프로바이더 인증 관리 |
| `services/api/apiKeys.ts` | `/v0/management/api-keys/*` | API 키 CRUD |
| `services/api/config.ts` | `/v0/management/config/*` | 서버 설정 |
| `services/api/logs.ts` | `/v0/management/logs/*` | 로그 조회 |
| `services/api/usage.ts` | `/v0/management/usage/*` | 사용량 통계 |
| `services/api/oauth.ts` | `/v0/management/oauth/*` | OAuth 콜백 |

### 인증 흐름

```
Frontend (React)                    Backend (Go)
     │                                   │
     │ ── Management Key ──────────────> │ middleware/auth.go
     │ <── Bearer Token ─────────────────│
     │                                   │
     │ ── /v0/management/* ────────────> │ handlers/management/
     │                                   │
```

### 공유 타입 매핑

| Frontend Type | Backend Type | Location |
|---------------|--------------|----------|
| `Auth` (types/auth.ts) | `auth.Auth` | sdk/cliproxy/auth/ |
| `Config` (types/config.ts) | `config.Config` | internal/config/ |
| `UsageStats` (types/usage.ts) | `usage.Event` | sdk/cliproxy/usage/ |

## WHERE TO LOOK

| Task | Backend | Frontend |
|------|---------|----------|
| 새 AI 프로바이더 추가 | `internal/auth/{provider}/` | `components/providers/{Provider}Section/` |
| API 엔드포인트 추가 | `internal/api/handlers/management/` | `services/api/` |
| 프로토콜 변환 | `internal/translator/{src}/{tgt}/` | N/A |
| UI 컴포넌트 추가 | N/A | `components/` |
| 상태 관리 | N/A | `stores/` |
| 설정 필드 추가 | `internal/config/config.go` | `types/config.ts` |

## TECH STACK

| Layer | Technology |
|-------|------------|
| **Backend** | Go 1.26, Gin Framework, logrus |
| **Frontend** | React 19, TypeScript, Vite 7, Zustand, axios |
| **i18n** | react-i18next (en, zh-CN, ru) |
| **Charts** | Recharts (dashboard), chart.js (frontend) |

## COMMANDS

```bash
# Backend
cd CLIProxyAPIPlus
go build -o cliproxy ./cmd/server
./cliproxy -c config.yaml
go test ./...

# Frontend
cd Cli-Proxy-API-Management-Center
npm install
npm run dev      # 개발 서버 (Vite)
npm run build    # 프로덕션 빌드
npm run lint     # ESLint 검사
```

## ANTI-PATTERNS (GLOBAL)

### Backend
- **NEVER** `http.DefaultClient` 사용 → `util.NewProxyClient()` 사용
- **NEVER** 토큰 직접 로깅 → 마스킹 필수
- **NEVER** translator에서 HTTP 호출 → 변환만 담당

### Frontend
- **NEVER** API 직접 호출 → `services/api/` 통해 호출
- **NEVER** 전역 상태 직접 수정 → Zustand store 사용
- **NEVER** `as any` 타입 단언 → 적절한 타입 정의

## CONVENTIONS

### 파일 명명
- Backend: `{provider}_auth.go`, `{src}_{tgt}_request.go`
- Frontend: `{Name}Page.tsx`, `use{Name}.ts`, `{name}.ts` (서비스)

### 코드 스타일
- Backend: Go 표준, logrus 로깅
- Frontend: ESLint + Prettier, 함수형 컴포넌트 + hooks

## SUB-DOCUMENTS

| Path | Scope |
|------|-------|
| [CLIProxyAPIPlus/AGENTS.md](./CLIProxyAPIPlus/AGENTS.md) | 백엔드 전체 |
| [CLIProxyAPIPlus/internal/AGENTS.md](./CLIProxyAPIPlus/internal/AGENTS.md) | internal 패키지 |
| [CLIProxyAPIPlus/sdk/AGENTS.md](./CLIProxyAPIPlus/sdk/AGENTS.md) | SDK 패키지 |
| [Cli-Proxy-API-Management-Center/AGENTS.md](./Cli-Proxy-API-Management-Center/AGENTS.md) | 프론트엔드 전체 |
| [CLIProxyAPI-Dashboard/AGENTS.md](./CLIProxyAPI-Dashboard/AGENTS.md) | 대시보드 전체 |
| [CLIProxyAPI-Dashboard/collector/AGENTS.md](./CLIProxyAPI-Dashboard/collector/AGENTS.md) | Python 수집기 |


# Memorix — Automatic Memory Rules

You have access to Memorix memory tools. Follow these rules to maintain persistent context across sessions.

## RULE 1: Session Start — Load Context

At the **beginning of every conversation**, BEFORE responding to the user:

1. Call `memorix_session_start` to get the previous session summary and key memories (this is a direct read, not a search — no fragmentation risk)
2. Then call `memorix_search` with a query related to the user's first message for additional context
3. If search results are found, use `memorix_detail` to fetch the most relevant ones
4. Reference relevant memories naturally — the user should feel you "remember" them

## RULE 2: Store Important Context

**Proactively** call `memorix_store` when any of the following happen:

### What MUST be recorded:
- Architecture/design decisions → type: `decision`
- Bug identified and fixed → type: `problem-solution`
- Unexpected behavior or gotcha → type: `gotcha`
- Config changed (env vars, ports, deps) → type: `what-changed`
- Feature completed or milestone → type: `what-changed`
- Trade-off discussed with conclusion → type: `trade-off`

### What should NOT be recorded:
- Simple file reads, greetings, trivial commands (ls, pwd, git status)

### Use topicKey for evolving topics:
For decisions, architecture docs, or any topic that evolves over time, ALWAYS use `topicKey` parameter.
This ensures the memory is UPDATED instead of creating duplicates.
Use `memorix_suggest_topic_key` to generate a stable key.

Example: `topicKey: "architecture/auth-model"` — subsequent stores with the same key update the existing memory.

### Track progress with the progress parameter:
When working on features or tasks, include the `progress` parameter:
```json
{
  "progress": {
    "feature": "user authentication",
    "status": "in-progress",
    "completion": 60
  }
}
```
Status values: `in-progress`, `completed`, `blocked`

## RULE 3: Resolve Completed Memories

When a task is completed, a bug is fixed, or information becomes outdated:

1. Call `memorix_resolve` with the observation IDs to mark them as resolved
2. Resolved memories are hidden from default search, preventing context pollution

This is critical — without resolving, old bug reports and completed tasks will keep appearing in future searches.

## RULE 4: Session End — Store Decision Chain Summary

When the conversation is ending, create a **decision chain summary** (not just a checklist):

1. Call `memorix_store` with type `session-request` and `topicKey: "session/latest-summary"`:

   **Required structure:**
   ```
   ## Goal
   [What we were working on — specific, not vague]

   ## Key Decisions & Reasoning
   - Chose X because Y. Rejected Z because [reason].
   - [Every architectural/design decision with WHY]

   ## What Changed
   - [File path] — [what changed and why]

   ## Current State
   - [What works now, what's pending]
   - [Any blockers or risks]

   ## Next Steps
   - [Concrete next actions, in priority order]
   ```

   **Critical: Include the "Key Decisions & Reasoning" section.** Without it, the next AI session will lack the context to understand WHY things were done a certain way and may suggest conflicting approaches.

2. Call `memorix_resolve` on any memories for tasks completed in this session

## RULE 5: Compact Awareness

Memorix automatically compacts memories on store:
- **With LLM API configured:** Smart dedup — extracts facts, compares with existing, merges or skips duplicates
- **Without LLM (free mode):** Heuristic dedup — uses similarity scores to detect and merge duplicate memories
- **You don't need to manually deduplicate.** Just store naturally and compact handles the rest.
- If you notice excessive duplicate memories, call `memorix_deduplicate` for batch cleanup.

## Guidelines

- **Use concise titles** (~5-10 words) and structured facts
- **Include file paths** in filesModified when relevant
- **Include related concepts** for better searchability
- **Always use topicKey** for recurring topics to prevent duplicates
- **Always resolve** completed tasks and fixed bugs
- **Always include reasoning** — "chose X because Y" is 10x more valuable than "did X"
- Search defaults to `status="active"` — use `status="all"` to include resolved memories
