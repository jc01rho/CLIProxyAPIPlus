# PROJECT KNOWLEDGE BASE

**Generated:** 2026-05-29
**Commit:** daf31abc
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
go build ./cmd/server
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
- **Tag versioning workflow**: 1) `git fetch --tags upstream` to fetch upstream latest tags. 2) `git ls-remote --tags upstream | grep -E 'refs/tags/v[0-9]' | sed 's|.*refs/tags/||' | sort -V | tail -10` to identify the latest upstream base version. 3) Use that base version with a sequence suffix: `v<upstream_base>-<sequence>`. Sequence starts at 1 for each new base.
- **Post-merge README.md cleanup (MANDATORY)**: After every `git merge upstream/main`, remove the upstream Sponsor section from `README.md`, `README_CN.md`, and `README_JA.md`. The upstream's `## Sponsor` block (PackyCode, AICodeMirror, BmoPlus, VisionCoder, APIKEY.FUN, RunAPI) must NOT appear in the fork. Keep only the `## Overview` and other content. Verify with: `grep -n "## Sponsor" README.md README_CN.md README_JA.md` should return nothing.
- For follow-up releases on the same base version, increment the suffix (e.g., `v7.1.22-1` → `v7.1.22-2`).
- When upstream releases a new base version (e.g., `v7.1.22`), create a new tag starting at suffix `-1` (e.g., `v7.1.22-1`) even if the previous base had higher suffixes.
- Only propose a new base tag when the user explicitly wants a new release line or that repository's recent tag history clearly starts a new base series.
- **Always verify upstream latest tags before tagging.** The repo AGENTS.md `Latest tags` line may be stale; run the fetch command above to get the actual latest.
- Upstream merges: always check `server.go` for duplicate route registration after merging.
- **Post-merge local-feature check (MANDATORY)**: upstream merges restructure `sdk/cliproxy/auth/conductor.go` (split into `conductor_execution.go`/`conductor_models.go`/etc.) and repeatedly drop local-only features (provider failover budget, threshold routing/billing class, alias-pool session pinning, route fallback, failure logging, quota-backoff constants, OAuth alias bypass, cooldown symbols, Antigravity runtime handoff, weighted-robin global-cycle path). After every `git merge upstream/main`, run `go test ./sdk/cliproxy/auth/` — these tests encode the documented fork behavior (`README.md` ~lines 6-7). **BUT go test is necessary, not sufficient**: it only encodes TESTED behaviors, so untested local-only features (Antigravity handoff, weighted-robin conductor path, OAuth alias resolution, `effectiveProviderKey`, `seedModelStateKey`, `authDecisionLabel`) are dropped while tests stay green. Also run a symbol-level diff (`comm -23` of `git show 4ccff390:sdk/cliproxy/auth/conductor.go` func-names vs current func-names) and restore any genuinely-missing local-only function from known-good commit `4ccff390`; do NOT edit or delete the tests. Full feature list, the diff command, and the merge procedure live in the umbrella `~/git/cli-proxy/AGENTS.md` ("CLIProxyAPIPlus local-only 기능 보존").
- **Provider-auth access-log guard (MANDATORY)**: after every conductor merge/split, verify `SetProviderAuthInContext` is consumed after auth selection in non-stream, count-token, stream, Home, and Antigravity credits fallback execution paths. `go test ./sdk/cliproxy/auth/ -run 'TestManagerSetsProviderAuthContextForMixedExecutionPaths|TestGinLoggerEndToEndIncludesSelectedProviderAuth'` must pass; otherwise access logs silently lose the upstream provider/auth label while requests still succeed.
- Re-tagging: delete GitHub release assets first, then re-run goreleaser (otherwise `422 already_exists`).
- **Every push requires a version bump.** When pushing code changes to any subdirectory repository, increment the tag suffix and create a new tag. No code should remain untagged on origin.
- Latest tags: `CLIProxyAPIPlus: v7.1.28-1`, `Cli-Proxy-API-Management-Center: v1.14.0-9`, `cpa-usage-keeper: v1.8.6-1`. Re-check before tagging.

## RECENT CHANGES

- **CLIProxyAPIPlus v7.1.28-1**: Token usage logging, gpt-5.5 restore for codex free tier.
- **CLIProxyAPIPlus v7.1.27-1**: Mistral base_url /v1 suffix stripping fix.
- **CLIProxyAPIPlus v7.1.25-2**: Mistral standalone provider, management API key CRUD, TTFT tracking, service tier usage, cache token reporting.
- **CLIProxyAPIPlus v7.1.23-8**: CommandCode null content block fix (400 BAD_REQUEST on tool-heavy conversations).
- **Management Center v1.14.0-9**: UI updates through v1.14.0-4 (priority column, CommandCode provider integration).
- **cpa-usage-keeper v1.8.6-1**: Usage keeper updates.
- Previous: CommandCode synthesizer/diff/SDK/watcher integration, upstream merges (v7.1.24 TTFT, helps migration, claudeCredsForAuthLookup restoration), CommandCode management API handler, config API key, model registration. See git log for full history.
## Code Conventions
- Keep changes small and simple (KISS)
- Comments in English only
- If editing code that already contains non-English comments, translate them to English (don’t add new non-English comments)
- For user-visible strings, keep the existing language used in that file/area
- New Markdown docs should be in English unless the file is explicitly language-specific (e.g. `README_CN.md`)
- As a rule, do not make standalone changes to `internal/translator/`. You may modify it only as part of broader changes elsewhere.
- If a task requires changing only `internal/translator/`, run `gh repo view --json viewerPermission -q .viewerPermission` to confirm you have `WRITE`, `MAINTAIN`, or `ADMIN`. If you do, you may proceed; otherwise, file a GitHub issue including the goal, rationale, and the intended implementation code, then stop further work.
- `internal/runtime/executor/` should contain executors and their unit tests only. Place any helper/supporting files under `internal/runtime/executor/helps/`.
- Follow `gofmt`; keep imports goimports-style; wrap errors with context where helpful
- Do not use `log.Fatal`/`log.Fatalf` (terminates the process); prefer returning errors and logging via logrus
- Shadowed variables: use method suffix (`errStart := server.Start()`)
- Wrap defer errors: `defer func() { if err := f.Close(); err != nil { log.Errorf(...) } }()`
- Use logrus structured logging; avoid leaking secrets/tokens in logs
- Avoid panics in HTTP handlers; prefer logged errors and meaningful HTTP status codes
- Timeouts are allowed only during credential acquisition; after an upstream connection is established, do not set timeouts for any subsequent network behavior. Intentional exceptions that must remain allowed are the Codex websocket liveness deadlines in `internal/runtime/executor/codex_websockets_executor.go`, the wsrelay session deadlines in `internal/wsrelay/session.go`, the management APICall timeout in `internal/api/handlers/management/api_tools.go`, and the `cmd/fetch_antigravity_models` utility timeouts
- Avoid wall-clock `time.Sleep` in TTL, expiration, ordering, or cache-eviction unit tests due to platform timer granularity (e.g. Windows default timer resolution of ~15.6ms) and CI jitter under load; prefer controllable clocks (`nowFunc` / mock clock), explicit timestamp manipulation, or deterministic synchronization primitives.
