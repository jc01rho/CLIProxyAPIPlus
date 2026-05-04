# SDK KNOWLEDGE BASE

> Parent: [../AGENTS.md](../AGENTS.md)

## OVERVIEW

`sdk/` is the public embedding/extensibility layer: service builder, auth/access interfaces, translator registry, config bridge, logging API, and handler adapters.

## STRUCTURE

```text
sdk/
├── cliproxy/      # Builder + service runtime
├── auth/          # public auth provider/client interfaces
├── access/        # request credential gating
├── api/           # server/management wrappers
├── translator/    # public transform registry
├── config/        # SDK config bridge
└── logging/       # request logger API
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Embedding service | `cliproxy/` | Start with `NewBuilder()`. |
| External auth | `auth/` | Public interfaces only. |
| Request access | `access/` | Credential gating and auth errors. |
| Translator API | `translator/` | Public wrapper around internal transforms. |
| SDK config | `config/` | External contract; change carefully. |

## CONVENTIONS

- Public examples should start from `sdk/cliproxy.NewBuilder()`.
- Keep public SDK interfaces separate from `internal/*` implementation details.
- SDK contract changes need broader compatibility review than private handler changes.

## ANTI-PATTERNS

- Do not add examples that import `internal/*`.
- Do not document direct `Service` struct construction.
- Do not let public translator registry behavior diverge from backend registration.

## SUB-DOCUMENTS

```text
cliproxy/AGENTS.md
```
