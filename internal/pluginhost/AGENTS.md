# PLUGIN HOST

> Parent: [../AGENTS.md](../AGENTS.md)

## OVERVIEW

`internal/pluginhost/` owns plugin lifecycle, RPC bridges, plugin-provided models/executors, management/resource routes, and callback contexts.

## STRUCTURE

```text
pluginhost/
├── host.go                 # lifecycle state, loaded/retired plugin registry, snapshots
├── adapters.go             # plugin capabilities → CPA registries/executors/translators
├── rpc_client.go           # plugin RPC invocation and stream/error handling
├── rpc_schema.go           # ABI schema boundary
├── management.go           # plugin management routes
├── executor_route.go       # plugin executor route bridge
├── *_bridge.go             # stream/http/model callback bridges
├── loader_*.go             # platform loader behavior
└── *_test.go               # lifecycle, RPC, route, stream, Windows loader tests
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Plugin load/apply/unload | `host.go`, `config.go`, `loader_*.go` | Preserve `loaded`, `retired`, `loading`, snapshot state. |
| Capability registration | `adapters.go` | Models, executors, translators, access keys, command flags. |
| RPC failure/streaming | `rpc_client.go`, `rpc_client_stream.go`, `stream_bridge.go` | Preserve context cancellation and plugin error shape. |
| Management/resource routes | `management.go`, `executor_route.go`, `http_bridge.go` | Keep route ownership in pluginhost, not API handlers. |
| Callback lifetimes | `callback_contexts.go`, `host_callbacks*.go` | Avoid stale callback context reuse. |

## CONVENTIONS

- `Host` state is lock-protected; update snapshots after mutating runtime-visible maps.
- Plugin capabilities are normalized in `adapters.go` before touching registries.
- Executor scope defaults to `both` when plugin metadata is absent or invalid.
- Platform loader differences belong in `loader_unix.go`, `loader_windows.go`, `support_*.go`.
- Keep plugin ABI/API compatibility stable; schema changes need `rpc_schema` tests.

## ANTI-PATTERNS

- Do not duplicate model routing or provider selection logic outside `adapters.go`/route bridge.
- Do not keep plugin callbacks after unload without callback-context cleanup.
- Do not hold host locks across plugin RPC/network calls.
- Do not expose raw plugin errors/logs containing secrets to management routes.
- Do not break Windows loader shadow-copy behavior when changing plugin file handling.

## COMMANDS

```bash
go test ./internal/pluginhost/... -count=1
```

## NOTES

- `adapters.go` and `host.go` are hotspot files; prefer small targeted edits with focused tests.
- Plugin model/executor registration affects registry visibility and route fallback behavior.
