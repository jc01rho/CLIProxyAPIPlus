# PROJECT KNOWLEDGE BASE

**Generated:** 2026-05-04
**Commit:** 813c04eb
**Branch:** master

## OVERVIEW

CLI Proxy monorepo: Go proxy server (`CLIProxyAPIPlus`), React Management Center (`Cli-Proxy-API-Management-Center`), and Python/React usage dashboard (`CLIProxyAPI-Dashboard`). Root tracks nested projects as gitlinks; `cpa-usage-keeper/` is an existing untracked sibling and is not part of this hierarchy.

## STRUCTURE

```text
cli-proxy/
├── CLIProxyAPIPlus/                  # Go proxy, OAuth/auth files, executors, translators, SDK
├── Cli-Proxy-API-Management-Center/  # React 19 + Vite management UI; builds single-file management.html
└── CLIProxyAPI-Dashboard/            # Flask collector, Postgres/PostgREST/Supabase schemas, React dashboard
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Proxy boot flags / server mode | `CLIProxyAPIPlus/cmd/server/main.go` | Single binary entry point. |
| Provider auth/execution | `CLIProxyAPIPlus/internal/auth/`, `CLIProxyAPIPlus/internal/runtime/executor/` | Auth and executor responsibilities stay separate. |
| Protocol conversion | `CLIProxyAPIPlus/internal/translator/` | Transform only; no network calls. |
| Management API backend | `CLIProxyAPIPlus/internal/api/handlers/management/` | Frontend contract lives in Center `src/services/api/`. |
| Management UI routes | `Cli-Proxy-API-Management-Center/src/router/MainRoutes.tsx`, `src/pages/` | HashRouter paths under `management.html#/*`. |
| Management UI API calls | `Cli-Proxy-API-Management-Center/src/services/api/` | Browser components do not call endpoints directly. |
| Dashboard collector/data flow | `CLIProxyAPI-Dashboard/collector/main.py`, `collector/db.py` | Scheduler + Flask + migration runner. |
| Dashboard schema | `CLIProxyAPI-Dashboard/init-db/schema.sql`, `collector/migrations/` | Fresh install and existing DB must match. |

## CODE MAP

| Symbol / Entry | Type | Location | Role |
|----------------|------|----------|------|
| `main` | Go entry | `CLIProxyAPIPlus/cmd/server/main.go` | CLI flags, login flows, proxy service boot. |
| `Service` | Go struct | `CLIProxyAPIPlus/sdk/cliproxy/service.go` | Runtime wiring: auth updates, executors, registry, server lifecycle. |
| `AiProvidersPage` | React page | `Cli-Proxy-API-Management-Center/src/pages/AiProvidersPage.tsx` | Provider list and enable/delete orchestration. |
| `MainRoutes` | React router | `Cli-Proxy-API-Management-Center/src/router/MainRoutes.tsx` | All management hash routes. |
| `main.py` module | Python app | `CLIProxyAPI-Dashboard/collector/main.py` | Flask endpoints, scheduler jobs, usage/event sync. |
| `PostgreSQLClient` | Python class | `CLIProxyAPI-Dashboard/collector/db.py` | Supabase-like local Postgres query layer. |

## CROSS-PROJECT CONVENTIONS

- Backend logs must mask tokens, cookies, API keys, and auth headers; use existing masking utilities first.
- Translators are pure format conversion. Do not add HTTP calls, credential selection, or upstream execution there.
- Management UI state changes go through Zustand actions; components must not mutate store state directly.
- Management UI HTTP calls go through `src/services/api/`; no raw component-level `/v0/management/*` calls.
- `management.html` changes require rebuilding Center and copying the single-file bundle into `CLIProxyAPIPlus/management.html` when embedding locally.
- Dashboard schema changes require both `init-db/schema.sql` and `collector/migrations/*.sql` updates.

## ANTI-PATTERNS

- Do not reintroduce Trae-specific provider flows; Trae support is intentionally removed.
- Do not put private endpoints in user-visible placeholders, fixtures, bundles, or reachable git history.
- Do not use `as any`, `@ts-ignore`, empty catch blocks, or test deletion to hide failures.
- Do not commit root gitlinks before committing nested repo changes they point to.

## COMMANDS

```bash
# CLIProxyAPIPlus
cd CLIProxyAPIPlus
go build ./cmd/server
go test ./...

# Management Center
cd Cli-Proxy-API-Management-Center
npm run type-check
npm run build

# Dashboard
cd CLIProxyAPI-Dashboard/collector
python3 -m py_compile main.py
python3 -m unittest test_model_usage_compaction.py test_main_retention.py test_redis_queue_sync.py test_request_events_api.py
cd ../frontend && npm run build
```

## ACTIVE SUB-DOCUMENTS

```text
CLIProxyAPIPlus/AGENTS.md
Cli-Proxy-API-Management-Center/AGENTS.md
CLIProxyAPI-Dashboard/AGENTS.md
```

## NOTES

- Root `git status` is the source of truth for nested project pointers; `.gitmodules` is absent.
- Search tools may miss nested gitlink files from root. Re-run file discovery inside nested repos when editing subproject AGENTS.md.
