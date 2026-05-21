# PROJECT KNOWLEDGE BASE

**Generated:** 2026-05-21
**Commit:** 266cfcf1
**Branch:** main

## OVERVIEW

CLI Proxy monorepo: Go proxy server (`CLIProxyAPIPlus`), React Management Center (`Cli-Proxy-API-Management-Center`), and usage keeper (`cpa-usage-keeper`). Root tracks nested projects as gitlinks without `.gitmodules`. Dashboard (`CLIProxyAPI-Dashboard`) was intentionally removed from this hierarchy.

## STRUCTURE

```text
cli-proxy/
├── CLIProxyAPIPlus/                  # Go proxy, OAuth/auth, executors, translators, SDK
├── Cli-Proxy-API-Management-Center/  # React 19 + Vite management UI; builds management.html
└── cpa-usage-keeper/                 # Go usage tracking service
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
| Usage tracking | `cpa-usage-keeper/internal/poller/`, `cpa-usage-keeper/internal/service/` | Redis queue polling + SQLite persistence. |

## CODE MAP

| Symbol / Entry | Type | Location | Role |
|----------------|------|----------|------|
| `main` | Go entry | `CLIProxyAPIPlus/cmd/server/main.go` | CLI flags, login flows, proxy service boot. |
| `Service` | Go struct | `CLIProxyAPIPlus/sdk/cliproxy/service.go` | Runtime wiring: auth updates, executors, registry, server lifecycle. |
| `AiProvidersPage` | React page | `Cli-Proxy-API-Management-Center/src/pages/AiProvidersPage.tsx` | Provider list and enable/delete orchestration. |
| `MainRoutes` | React router | `Cli-Proxy-API-Management-Center/src/router/MainRoutes.tsx` | All management hash routes. |

## CROSS-PROJECT CONVENTIONS

- Backend logs must mask tokens, cookies, API keys, and auth headers; use existing masking utilities first.
- Translators are pure format conversion. Do not add HTTP calls, credential selection, or upstream execution there.
- Management UI state changes go through Zustand actions; components must not mutate store state directly.
- Management UI HTTP calls go through `src/services/api/`; no raw component-level `/v0/management/*` calls.
- `management.html` changes require rebuilding Center and copying the single-file bundle into `CLIProxyAPIPlus/management.html` when embedding locally.

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

# Usage Keeper
cd cpa-usage-keeper
go build ./cmd/keeper
go test ./...
```

## ACTIVE SUB-DOCUMENTS

```text
CLIProxyAPIPlus/AGENTS.md
CLIProxyAPIPlus/internal/AGENTS.md
CLIProxyAPIPlus/internal/api/AGENTS.md
CLIProxyAPIPlus/internal/api/handlers/management/AGENTS.md
CLIProxyAPIPlus/internal/auth/kiro/AGENTS.md
CLIProxyAPIPlus/internal/config/AGENTS.md
CLIProxyAPIPlus/internal/registry/AGENTS.md
CLIProxyAPIPlus/internal/runtime/executor/AGENTS.md
CLIProxyAPIPlus/internal/translator/AGENTS.md
CLIProxyAPIPlus/internal/util/AGENTS.md
CLIProxyAPIPlus/sdk/AGENTS.md
CLIProxyAPIPlus/sdk/cliproxy/AGENTS.md
CLIProxyAPIPlus/sdk/cliproxy/auth/AGENTS.md
Cli-Proxy-API-Management-Center/AGENTS.md
Cli-Proxy-API-Management-Center/src/components/providers/AGENTS.md
Cli-Proxy-API-Management-Center/src/components/ui/AGENTS.md
Cli-Proxy-API-Management-Center/src/pages/AGENTS.md
Cli-Proxy-API-Management-Center/src/services/api/AGENTS.md
Cli-Proxy-API-Management-Center/src/stores/AGENTS.md
cpa-usage-keeper/AGENTS.md
```

## NOTES

- Root `git status` is the source of truth for nested project pointers; `.gitmodules` is absent.
- Search tools may miss nested gitlink files from root. Re-run file discovery inside nested repos when editing subproject AGENTS.md.
- Do not create release tags from the repository root. Create tags only inside the relevant subdirectory repository.
- Before creating or recommending a tag, inspect the latest tags of the target subdirectory repository and continue that repository's own version line.
- For follow-up releases on the same base version, prefer incrementing the suffix (`v<major>.<minor>.<patch>-<sequence>`) instead of inventing a root-level tag.
- Only propose a new base tag when the user explicitly wants a new release line or that repository's recent tag history clearly starts a new base series.
- Upstream merges: always check `server.go` for duplicate route registration after merging.
- Re-tagging: delete GitHub release assets first, then re-run goreleaser (otherwise `422 already_exists`).
- Latest tags: `CLIProxyAPIPlus: v7.1.18-2`, `Cli-Proxy-API-Management-Center: v1.11.1-4`, `cpa-usage-keeper: v1.7.4`. Re-check before tagging.

## RECENT CHANGES

- **CLIProxyAPIPlus v7.1.18-2**: fix missing `/v0/management/request-log-success-body` route.
- **CLIProxyAPIPlus v7.1.18-1**: merged upstream/main v7.1.18 (reasoning effort metadata, Gemini 3.5 Flash, Redis enhancements). 3 handler conflicts resolved keeping maybeAttachEstimatedInputTokens.
- **CLIProxyAPIPlus v7.1.17-3**: Ollama models fetch URL fix (double /api prefix). Merged upstream v7.1.16–v7.1.17.
- **CLIProxyAPIPlus v7.1.15 series**: Full Ollama provider support — streaming (ExecuteStream), JSON content conversion, tool_calls stripping, models fetch. Merged upstream v7.1.15 with Home CA, image models, xAI reasoning.effort.
