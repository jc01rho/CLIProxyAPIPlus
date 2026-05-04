# SDK CLIPROXY CORE

> Parent: [../AGENTS.md](../AGENTS.md)

## OVERVIEW

`sdk/cliproxy/` is the embeddable service core: Builder, lifecycle wiring, config-backed auth synthesis, executor binding, model registration, auth conductor, and usage plugins.

## STRUCTURE

```text
cliproxy/
├── builder.go
├── service.go
├── providers.go
├── watcher.go
├── model_registry.go
├── auth/
│   ├── conductor.go / manager.go / selector.go / auth.go
└── usage/
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Embed setup | `builder.go` | `With*` chain. |
| Runtime wiring | `service.go` | Auth updates, executors, registry, server lifecycle. |
| Config-backed auths | `providers.go`, `service.go` | `resolveConfig*Key` and model registration. |
| Auth selection | `auth/selector.go`, `auth/conductor.go` | RoundRobin/FillFirst, fallback logging. |
| Model registry bridge | `model_registry.go` | Client availability for routing. |
| Usage extension | `usage/` | Plugin hooks. |

## CONVENTIONS

- Do not bypass watcher/hot reload with manual service reinitialization.
- Auth selector changes require route/model/fallback behavior review.
- Config-backed providers must register executors and model catalogs with provider-scoped IDs.
- Fallback logging should expose requested model, fallback model/source, upstream status, reason, and request id.

## ANTI-PATTERNS

- Do not directly instantiate `Service` in public examples.
- Do not type-assert `Auth.Runtime` casually; prefer typed helpers or attributes.
- Do not add SDK examples that rely on private `internal/*` packages.

## SUB-DOCUMENTS

```text
auth/AGENTS.md
```
