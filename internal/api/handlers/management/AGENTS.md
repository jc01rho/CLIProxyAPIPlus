# MANAGEMENT HANDLERS

> Parent: [../../AGENTS.md](../../AGENTS.md)

## OVERVIEW

`internal/api/handlers/management/` is the backend contract for Management Center: config CRUD, auth-file operations, OAuth callbacks, model lists, logs, quota, usage queue drain, and API-call proxy tools.

## STRUCTURE

```text
management/
├── handler.go
├── auth_files.go
├── config_*.go / config_lists.go
├── model_definitions.go / auth_models.go
├── usage_queue.go / logs.go / quota.go
├── oauth_*.go
├── api_tools.go / cbor_*.go
└── *_test.go
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Handler wiring | `handler.go` | `NewHandler`, auth manager/config setters, persist/apply. |
| Config mutation | `config_*.go`, `config_lists.go` | Save comments, then apply runtime config. |
| Auth files/models | `auth_files.go`, `auth_models.go` | Filename basename matching and provider filtering. |
| Provider model definitions | `model_definitions.go` | UI model endpoints. |
| Usage queue | `usage_queue.go` | Drain endpoint; mask API keys. |
| Generic upstream calls | `api_tools.go` | Management proxy request/response shape. |

## CONVENTIONS

- Config writes go through `h.persist(c)` so YAML persistence and `applyRuntimeConfig(...)` stay coupled.
- Use `config.SaveConfigPreserveComments` paths for file-backed config edits.
- Auth accepts Bearer or `X-Management-Key`; keep constant-time comparison and IP ban semantics intact.
- `SetConfig()` / `SetAuthManager()` are hot-reload paths; refresh OAuth aliases, fallback models, and provider mappings there.
- Frontend contract changes require matching Center `src/services/api/*` types and transformers.

## TESTS

```bash
go test ./internal/api/handlers/management/... -count=1
go test ./internal/api/handlers/management -run 'TestGetAuthFileModels|TestUsageQueue|TestConfig' -count=1
```

## ANTI-PATTERNS

- Do not mutate config in memory without persisting/applying through the handler path.
- Do not return raw auth-file secrets, provider API keys, or request Authorization headers.
- Do not bury provider auth business logic here; move it to `internal/auth/` or executor helpers.
- Do not let Management Center and backend response shapes drift.
