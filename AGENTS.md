# PROJECT KNOWLEDGE BASE

**Updated:** 2026-04-15
**Commit:** 9be69e5f
**Branch:** master
**Type:** Monorepo (Go backend + React management center + Python/React dashboard)

## OVERVIEW

CLI Proxy monorepo. `CLIProxyAPIPlus`가 다수의 AI provider를 OpenAI 호환 프록시로 통합하고, `Cli-Proxy-API-Management-Center`가 Management API용 운영 UI를 제공하며, `CLIProxyAPI-Dashboard`가 사용량/비용/자격증명 상태를 수집·시각화한다.

## STRUCTURE

```text
cli-proxy/
├── CLIProxyAPIPlus/                  # Go 프록시 서버, OAuth, translator, SDK
├── Cli-Proxy-API-Management-Center/  # React 19 관리 UI (Vite, Zustand, Axios)
└── CLIProxyAPI-Dashboard/            # Python collector + React dashboard + local DB stack
```

## WHERE TO LOOK

| Task | Primary Location | Notes |
|------|------------------|-------|
| 새 provider 인증/실행 추가 | `CLIProxyAPIPlus/internal/auth/`, `internal/runtime/executor/` | 인증과 실행을 분리 유지 |
| 프로토콜 변환 수정 | `CLIProxyAPIPlus/internal/translator/` | translator는 HTTP 호출 금지 |
| 관리 API/설정 수정 | `CLIProxyAPIPlus/internal/api/handlers/management/`, `internal/config/` | `/v0/management/*` 중심 |
| 관리 UI 페이지/상태 수정 | `Cli-Proxy-API-Management-Center/src/pages/`, `src/stores/` | API 호출은 `src/services/api/`만 사용 |
| 대시보드 수집/집계 수정 | `CLIProxyAPI-Dashboard/collector/` | schema 변경 시 migration 동반 |
| 대시보드 시각화/로그인 수정 | `CLIProxyAPI-Dashboard/frontend/src/` | dev/prod auth 흐름 동일성 주의 |

## CROSS-PROJECT RULES

- 백엔드 토큰/쿠키/비밀값은 로그에 그대로 남기지 않는다. 마스킹 유틸을 우선 사용한다.
- translator에서는 네트워크 호출을 하지 않는다. 입력/출력 포맷 변환만 담당한다.
- 관리 UI에서는 브라우저에서 직접 endpoint를 호출하지 않는다. `src/services/api/` 레이어를 거친다.
- Zustand store는 액션으로만 갱신한다. 컴포넌트에서 전역 상태를 직접 변형하지 않는다.
- 대시보드 DB 스키마를 바꿀 때는 `init-db/schema.sql`과 `collector/migrations/*.sql`를 함께 갱신한다.

## COMMANDS

```bash
# Backend
cd CLIProxyAPIPlus
go build ./cmd/server
go run ./cmd/server --config config.yaml
go test ./...

# Management Center
cd Cli-Proxy-API-Management-Center
npm install
npm run dev
npm run build
npm run lint
npm run type-check

# Dashboard
cd CLIProxyAPI-Dashboard
COMPOSE_PROFILES=localdb docker compose up -d
cd frontend && npm install && npm run dev
cd ../collector && python3 -m venv venv && . venv/bin/activate && pip install -r requirements.txt && python main.py
```

## ACTIVE SUB-DOCUMENTS

### Backend
- `CLIProxyAPIPlus/AGENTS.md`
- `CLIProxyAPIPlus/internal/AGENTS.md`
- `CLIProxyAPIPlus/internal/api/AGENTS.md`
- `CLIProxyAPIPlus/internal/auth/kiro/AGENTS.md`
- `CLIProxyAPIPlus/internal/config/AGENTS.md`
- `CLIProxyAPIPlus/internal/registry/AGENTS.md`
- `CLIProxyAPIPlus/internal/runtime/executor/AGENTS.md`
- `CLIProxyAPIPlus/internal/translator/AGENTS.md`
- `CLIProxyAPIPlus/internal/util/AGENTS.md`
- `CLIProxyAPIPlus/sdk/AGENTS.md`
- `CLIProxyAPIPlus/sdk/cliproxy/AGENTS.md`

### Management Center
- `Cli-Proxy-API-Management-Center/AGENTS.md`
- `Cli-Proxy-API-Management-Center/src/pages/AGENTS.md`
- `Cli-Proxy-API-Management-Center/src/services/api/AGENTS.md`
- `Cli-Proxy-API-Management-Center/src/stores/AGENTS.md`
- `Cli-Proxy-API-Management-Center/src/components/providers/AGENTS.md`
- `Cli-Proxy-API-Management-Center/src/components/ui/AGENTS.md`

### Dashboard
- `CLIProxyAPI-Dashboard/AGENTS.md`
- `CLIProxyAPI-Dashboard/collector/AGENTS.md`
- `CLIProxyAPI-Dashboard/frontend/AGENTS.md`

## NOTES

- Trae provider 지원은 제거된 상태다. 새 문서나 코드에서 Trae 전용 흐름을 되살리지 않는다.
- 초기 glob 검색은 일부 중첩 `AGENTS.md`를 놓칠 수 있다. 실제 인벤토리는 리포지토리 루트 기준 재검증한다.


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

# context-mode — MANDATORY routing rules

You have context-mode MCP tools available. These rules are NOT optional — they protect your context window from flooding. A single unrouted command can dump 56 KB into context and waste the entire session.

## BLOCKED commands — do NOT attempt these

### curl / wget — BLOCKED
Any shell command containing `curl` or `wget` will be intercepted and blocked by the context-mode plugin. Do NOT retry.
Instead use:
- `context-mode_ctx_fetch_and_index(url, source)` to fetch and index web pages
- `context-mode_ctx_execute(language: "javascript", code: "const r = await fetch(...)")` to run HTTP calls in sandbox

### Inline HTTP — BLOCKED
Any shell command containing `fetch('http`, `requests.get(`, `requests.post(`, `http.get(`, or `http.request(` will be intercepted and blocked. Do NOT retry with shell.
Instead use:
- `context-mode_ctx_execute(language, code)` to run HTTP calls in sandbox — only stdout enters context

### Direct web fetching — BLOCKED
Do NOT use any direct URL fetching tool. Use the sandbox equivalent.
Instead use:
- `context-mode_ctx_fetch_and_index(url, source)` then `context-mode_ctx_search(queries)` to query the indexed content

## REDIRECTED tools — use sandbox equivalents

### Shell (>20 lines output)
Shell is ONLY for: `git`, `mkdir`, `rm`, `mv`, `cd`, `ls`, `npm install`, `pip install`, and other short-output commands.
For everything else, use:
- `context-mode_ctx_batch_execute(commands, queries)` — run multiple commands + search in ONE call
- `context-mode_ctx_execute(language: "shell", code: "...")` — run in sandbox, only stdout enters context

### File reading (for analysis)
If you are reading a file to **edit** it → reading is correct (edit needs content in context).
If you are reading to **analyze, explore, or summarize** → use `context-mode_ctx_execute_file(path, language, code)` instead. Only your printed summary enters context.

### grep / search (large results)
Search results can flood context. Use `context-mode_ctx_execute(language: "shell", code: "grep ...")` to run searches in sandbox. Only your printed summary enters context.

## Tool selection hierarchy

1. **GATHER**: `context-mode_ctx_batch_execute(commands, queries)` — Primary tool. Runs all commands, auto-indexes output, returns search results. ONE call replaces 30+ individual calls.
2. **FOLLOW-UP**: `context-mode_ctx_search(queries: ["q1", "q2", ...])` — Query indexed content. Pass ALL questions as array in ONE call.
3. **PROCESSING**: `context-mode_ctx_execute(language, code)` | `context-mode_ctx_execute_file(path, language, code)` — Sandbox execution. Only stdout enters context.
4. **WEB**: `context-mode_ctx_fetch_and_index(url, source)` then `context-mode_ctx_search(queries)` — Fetch, chunk, index, query. Raw HTML never enters context.
5. **INDEX**: `context-mode_ctx_index(content, source)` — Store content in FTS5 knowledge base for later search.

## Output constraints

- Keep responses under 500 words.
- Write artifacts (code, configs, PRDs) to FILES — never return them as inline text. Return only: file path + 1-line description.
- When indexing content, use descriptive source labels so others can `search(source: "label")` later.

## ctx commands

| Command | Action |
|---------|--------|
| `ctx stats` | Call the `stats` MCP tool and display the full output verbatim |
| `ctx doctor` | Call the `doctor` MCP tool, run the returned shell command, display as checklist |
| `ctx upgrade` | Call the `upgrade` MCP tool, run the returned shell command, display as checklist |
