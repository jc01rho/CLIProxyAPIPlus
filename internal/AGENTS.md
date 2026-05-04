# INTERNAL SYSTEM

> Parent: [../AGENTS.md](../AGENTS.md)

## OVERVIEW

`internal/` is the private server core: Gin API, auth/OAuth, executors, translators, config, registry, watcher, logging, usage queue, and utilities.

## STRUCTURE

```text
internal/
├── api/                  # Gin routing, management handlers, middleware, Amp module
├── auth/                 # provider OAuth/token flows
├── runtime/executor/     # upstream calls
├── translator/           # format transforms only
├── config/               # YAML config and compatibility defaults
├── registry/             # model catalog and availability
├── watcher/              # config/auth file synthesis and hot reload
├── logging/              # request/gin/global logs
└── util/                 # masking, proxy, headers, provider helpers
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Route/middleware order | `api/server.go` | Scanner-probe blocking must happen before Gin routing/logging. |
| Management payloads | `api/handlers/management/` | Keep UI service contracts aligned. |
| Config-backed credentials | `watcher/synthesizer/`, `watcher/clients.go` | Provider config becomes runtime auth. |
| Provider execution | `runtime/executor/` | Use `helps/` for shared request/logging behavior. |
| Translation | `translator/` | Stateless request/response transforms. |
| Model visibility | `registry/` | Static fallback + dynamic discovery + aliases. |

## CONVENTIONS

- Keep handler = HTTP boundary, executor = upstream call, translator = format transform.
- Add provider-specific exceptions near the provider, not in broad utility paths.
- Config changes require checking watcher hot reload and management UI serialization.
- Usage queue and detailed logging paths must mask API keys before management exposure.

## ANTI-PATTERNS

- No network calls in `translator/`.
- No protocol transform logic inside API handlers.
- No plaintext token/session/API-key logging.
- No provider-specific routing logic duplicated across handler, executor, and UI.

## SUB-DOCUMENTS

```text
api/AGENTS.md
auth/kiro/AGENTS.md
config/AGENTS.md
registry/AGENTS.md
runtime/executor/AGENTS.md
translator/AGENTS.md
util/AGENTS.md
```
