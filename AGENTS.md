# CLIPROXYAPIPLUS KNOWLEDGE BASE

**Generated:** 2026-05-04
**Commit:** 9bef8022
**Branch:** main

## OVERVIEW

Go 1.26 AI proxy server. It combines CLI auth flows, OpenAI-compatible API serving, provider executors, protocol translators, runtime model registry, management API, and public SDK.

## STRUCTURE

```text
CLIProxyAPIPlus/
├── cmd/server/main.go          # binary entry: flags, login flows, service boot
├── internal/                   # private server implementation
├── sdk/                        # embeddable public API
├── auths/                      # default auth-file directory
├── test/                       # cross-package integration/sentinel tests
├── management.html             # embedded Management Center bundle
└── config.example.yaml         # config surface reference
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Server boot / flags | `cmd/server/main.go` | `--config`, `--tui`, login flags, local-model mode. |
| Management routes | `internal/api/` | Gin server + `/v0/management/*`. |
| Provider auth | `internal/auth/` | OAuth/token storage per provider. |
| Upstream execution | `internal/runtime/executor/` | HTTP/WebSocket calls after translation. |
| Protocol translation | `internal/translator/` | source/target registration and JSON/SSE transforms. |
| Model catalog/routing | `internal/registry/` | static fallback, dynamic discovery, provider scoping. |
| Config/auth synthesis | `internal/config/`, `internal/watcher/` | YAML fields, hot reload, config-backed auths. |
| SDK embedding | `sdk/cliproxy/` | Builder and service lifecycle. |

## COMMANDS

```bash
go build ./cmd/server
go run ./cmd/server --config config.yaml
go test ./...
goreleaser build --snapshot --clean
```

## CONVENTIONS

- YAML keys are kebab-case; Go fields stay CamelCase.
- Provider additions usually touch config, watcher/synthesizer, registry/model discovery, executor, management API, and Center UI.
- Executor logs for upstream failures must include masked request and response context when diagnosing 4xx/5xx.
- `management.html` is served by Plus; local UI edits need Center build output copied back.

## ANTI-PATTERNS

- Do not use `http.DefaultClient`; use configured clients/proxy-aware helpers.
- Do not log Authorization, cookies, refresh tokens, API keys, or raw auth files.
- Do not terminate handlers with `panic` or `log.Fatal`.
- Do not scatter model allowlists in handlers/executors; centralize via registry/config paths.

## SUB-DOCUMENTS

```text
internal/AGENTS.md
sdk/AGENTS.md
```
