# UTILITIES

> Parent: [../AGENTS.md](../AGENTS.md)

## OVERVIEW

`internal/util/` holds small cross-cutting helpers for proxy setup, masking, provider detection, headers, image/path handling, and SSH helpers.

## STRUCTURE

```text
util/
├── util.go
├── proxy.go
├── provider.go
├── header_helpers.go
├── image.go
└── ssh_helper.go
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| HTTP/SOCKS proxy | `proxy.go` | Keep client construction consistent. |
| Secret masking | `provider.go` and masking helpers | Logs and management responses. |
| Provider detection | `provider.go` | Avoid ad hoc string checks. |
| Header helpers | `header_helpers.go` | Request forwarding and passthrough rules. |

## CONVENTIONS

- Search util masking before adding any diagnostic logging with headers/body/query data.
- Keep provider mapping helpers centralized.
- Proxy-aware transport creation should stay reusable; no `http.DefaultClient` shortcuts.

## ANTI-PATTERNS

- Do not log Authorization/API-key/header secrets raw.
- Do not scatter provider-name heuristics across executors and handlers.
- Do not create per-provider proxy helper copies.
