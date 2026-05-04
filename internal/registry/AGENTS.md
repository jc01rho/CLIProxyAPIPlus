# REGISTRY SYSTEM

> Parent: [../AGENTS.md](../AGENTS.md)

## OVERVIEW

`internal/registry/` centralizes model definitions, provider scoping, dynamic model discovery, aliases, quota status, and static fallback catalogs.

## STRUCTURE

```text
registry/
├── model_registry.go
├── model_definitions.go
├── model_updater.go
├── cline_models.go / kilo_models.go
├── *_model_converter.go
└── models/models.json
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Static model list | `model_definitions.go`, `models/models.json` | Provider-specific fallback. |
| Runtime registration | `model_registry.go` | Client/provider/model availability. |
| Remote refresh | `model_updater.go` | Interacts with `--local-model`. |
| Provider normalization | `*_model_converter.go` | Keep conversion near provider. |

## CONVENTIONS

- Prefer registry APIs over direct model string comparisons.
- Static fallback lookup must be provider-scoped; do not borrow another provider's model definition.
- GitHub Copilot exposure is a five-model allowlist only: `claude-haiku-4.5`, `gemini-2.5-pro`, `gemini-3-pro-preview`, `gemini-3.1-pro-preview`, `gemini-3-flash-preview`.

## ANTI-PATTERNS

- Do not hardcode model catalogs in handlers or UI.
- Do not let `oauth-model-alias` entries leak into upstream `/models` requests.
- Do not expose unsupported upstream Copilot models from static fallback, dynamic discovery, or auth-file model endpoints.
