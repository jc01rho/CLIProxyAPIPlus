# API SYSTEM

> Parent: [../AGENTS.md](../AGENTS.md)

## OVERVIEW

`internal/api/` is the Gin server boundary: root API routes, management endpoints, middleware, logs, usage queue drain, auth-file operations, and Amp proxy module.

## STRUCTURE

```text
api/
├── server.go
├── handlers/management/
│   ├── auth_files.go
│   ├── config_*.go
│   ├── usage_queue.go / logs.go / quota.go
│   ├── oauth_*.go
│   └── model_definitions.go
├── middleware/
└── modules/amp/
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Route tree / middleware | `server.go` | Scanner file probes must return 403 before 404 WARN logging. |
| Config management | `handlers/management/config_*.go` | Persist then apply runtime config. |
| Auth files/models | `handlers/management/auth_files.go` | Filename basename matching matters. |
| Usage queue drain | `handlers/management/usage_queue.go` | Mask `api_key` in responses. |
| Logs endpoints | `handlers/management/logs.go` | Request/error log UX contracts. |
| Amp module | `modules/amp/` | Separate route/mapping system. |

## CONVENTIONS

- Management endpoint changes require matching Center `src/services/api/*` updates.
- Keep provider-specific logic small; move reusable behavior to helpers or provider packages.
- Tests live beside API packages; add focused route/contract tests for new behavior.

## ANTI-PATTERNS

- Do not call `http.ListenAndServe` outside the server setup path.
- Do not write secrets or raw auth files to logs/responses.
- Do not let scanner `.env`, `.git/config`, backup/sql/php probes fall through to normal 404 logging.

## SUB-DOCUMENTS

```text
handlers/management/AGENTS.md
```
