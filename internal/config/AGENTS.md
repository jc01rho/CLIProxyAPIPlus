# CONFIG SYSTEM

> Parent: [../AGENTS.md](../AGENTS.md)

## OVERVIEW

`internal/config/` is the runtime YAML contract and compatibility layer. `config.go` is the large central type; SDK config, OAuth alias defaults, and provider compatibility helpers sit nearby.

## STRUCTURE

```text
config/
├── config.go
├── sdk_config.go
├── oauth_model_alias_defaults.go
├── vertex_compat.go
└── *_test.go
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| New YAML key | `config.go` | Kebab-case tag + default/backcompat review. |
| SDK exposure | `sdk_config.go` | External consumer contract. |
| OAuth aliases | `oauth_model_alias_defaults.go` | Alias targets must match auth provider family. |
| Provider compat | `vertex_compat.go`, related helpers | Keep config-backed provider behavior coherent. |

## CONVENTIONS

- YAML tags use kebab-case; CLI/memory naming uses `min-tokens`, not `nin-tokens`.
- Adding config means checking loader defaults, watcher diff hash, synthesizer, management API, Center types/store.
- Claude/Codex/Copilot/Kimi OAuth refresh flows use `refresh-url` when set, falling back to `token-url`.

## ANTI-PATTERNS

- Do not hardcode ports, hosts, endpoints, or credentials in code.
- Do not bypass backward-compatibility paths when adding new fields.
- Do not accept config values into logs without masking/sanitization.
