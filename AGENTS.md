# PROJECT KNOWLEDGE BASE

**Generated:** 2026-05-19
**Commit:** cc3f239c
**Branch:** master

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
- Current observed latest tags: `CLIProxyAPIPlus: v7.1.15`, `Cli-Proxy-API-Management-Center: v1.11.1-4`, `cpa-usage-keeper: v1.7.4`. Re-check before tagging; each subrepository advances independently.
- For follow-up releases on the same base version inside a subdirectory repository, prefer incrementing the suffix (`v<major>.<minor>.<patch>-<sequence>`) instead of inventing a root-level tag.
- Only propose a new base tag inside the target subdirectory repository when the user explicitly wants a new release line or that repository's recent tag history clearly starts a new base series.
- buildConfigModels dedup key changed to (alias|name) to support the same alias (e.g. higher-coding) mapped to many upstream models without dropping later entries from registry/round-robin.
- applyOAuthModelAlias dedup key also changed to (alias|upstreamID) for the same reason — OAuth provider alias collision.
- OAuthModelAliasChannel supported channels: removed ollama (API-key only).
- conductor.go statusCode switches: added case 400 with 30min cooldown and suspendReason "bad_request" to prevent repeated failed requests re-entering round-robin.

## RECENT CHANGES

- **CLIProxyAPIPlus v7.1.17-3**: Fixed double /api prefix in FetchOllamaModels URL construction (ollama.com/api + /api/tags → ollama.com/api/api/tags). Now strips /api suffix before appending endpoint paths.
- **CLIProxyAPIPlus v7.1.17-2**: Fixed Ollama models fetch URL fallback (tries /v1/tags then /api/tags for ollama.com) and stripped unsupported tool_calls/tool_call_id fields from messages for /api/chat compatibility.
- **CLIProxyAPIPlus v7.1.17-1**: merge upstream/main (v7.1.17) - test cleanup, README updates, Redis timeout/subscription failover.
- **CLIProxyAPIPlus v7.1.16-1**: merge upstream/main (v7.1.16) - Redis timeout handling, sponsor docs improvements.
- **CLIProxyAPIPlus v7.1.15-3**: Fixed Ollama JSON parsing error — convert OpenAI array-based content to Ollama string format; improved error logging by masking message content instead of removing entire messages field.
- **CLIProxyAPIPlus v7.1.15-2**: Fixed Ollama streaming — implemented ExecuteStream with NDJSON line-by-line reading, Ollama→OpenAI SSE chunk conversion, thinking/reasoning support, and usage tracking on done:true chunk.
- **CLIProxyAPIPlus v7.1.15**: Merged upstream/main. Added Home CA fingerprint verification, OpenAI image model compatibility, xAI reasoning.effort support, upstream response header tracking, Antigravity Gemini thought signature fixes, and Gemini max output token cap. Conflicts resolved in usage_helpers.go (kept storeUsageDetailInContext), openai_compat_executor.go (merged image constants), openai_compat_executor_compact_test.go (kept both reasoning and image tests), and handlers.go (kept attachRouteFallback + routeModelBaseName).
- **CLIProxyAPIPlus v7.1.11-8**: Added 400 bad_request cooldown (30min) in conductor; changed applyOAuthModelAlias dedup key to (alias|upstreamID); fixed API key model alias resolution via apiKeyModelAlias table; removed ollama from OAuth alias channels; enforced 200 tools cap in normalizeXAITools; changed buildConfigModels dedup key to (alias|name).
- **Management Center v1.11.1-4**: Merged upstream/main. Added filter controls with search, Home control plane config removal, auth file HTML challenge handling, ConfigSection style improvements, Antigravity Credits localization, dependency updates. Conflicts resolved in 7 files (VisualConfigEditor, useVisualConfig, visualConfig types, ru.json).
- **Management Center v1.11.1-3**: Added OllamaSection as independent provider section; added Ollama icon and display in providers list; dark mode support for Grok icon; normalized provider keys across components.
