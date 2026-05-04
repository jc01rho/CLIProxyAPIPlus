# SDK AUTH CONDUCTOR

> Parent: [../AGENTS.md](../AGENTS.md)

## OVERVIEW

`sdk/cliproxy/auth/` coordinates runtime credential selection: auth manager state, selectors, refresh scheduling, quota/cooldown, OAuth model aliases, fallback chains, and session affinity.

## STRUCTURE

```text
auth/
├── conductor.go
├── manager.go
├── selector.go
├── scheduler.go
├── auth.go / runtime.go / attributes.go
├── quota*.go / status*.go
├── *_test.go
└── conductor_overrides_test.go
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Credential selection | `conductor.go`, `selector.go` | Round-robin/fill-first, retries, fallback. |
| Refresh scheduling | `scheduler.go`, refresh helpers | Provider refresh policy and cooldown. |
| Runtime auth state | `manager.go`, `auth.go`, `runtime.go` | File/config-backed auth lifecycle. |
| Quota/status | `quota*.go`, `status*.go` | Provider/auth availability. |
| Override regression tests | `conductor_overrides_test.go` | Large safety net for routing/fallback changes. |

## CONVENTIONS

- `conductor.go` is high-risk concurrency code; change minimally and run targeted tests first.
- Never `Store(nil)` into `atomic.Value`; use empty slices/maps or explicit typed zero values.
- Keep lock ordering stable: conductor state before scheduler internals; avoid nested reverse acquisition.
- OAuth model alias targets must stay in the current auth provider family.
- Fallback logs should include requested model, selected/fallback model, fallback source/reason, upstream status, and request id.
- Antigravity OAuth primary handoff triggers on auth/quota failures; do not create temporary fallback semantics there.

## TESTS

```bash
go test ./sdk/cliproxy/auth -count=1
go test ./sdk/cliproxy/auth -run 'Test.*Fallback|Test.*Alias|Test.*Conductor' -count=1
```

## ANTI-PATTERNS

- Do not add credential selection logic inside executors or API handlers.
- Do not bypass selectors for a one-off provider special case.
- Do not hold locks while making upstream network calls.
- Do not suppress route/fallback failures with silent fallback; log structured context.
