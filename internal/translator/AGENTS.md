# TRANSLATOR SYSTEM

> Parent: [../../AGENTS.md](../../AGENTS.md)

## OVERVIEW

`internal/translator/` converts source protocol payloads into target provider formats and converts responses back. It is stateless transformation code, not routing or execution.

## STRUCTURE

```text
translator/
├── openai/
├── claude/
├── gemini/
├── gemini-cli/
├── codex/
├── antigravity/
├── kiro/
└── */common/
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| New source/target transform | `{source}/{target}/` | Include registration. |
| Request conversion | `*_request.go` | JSON field shaping and compatibility. |
| Non-stream response | `*_response.go` | Target-to-source response shape. |
| SSE response | stream response files | Chunk format and terminal events. |
| Shared transform helpers | `common/` | Keep provider-specific temp fields contained. |

## CONVENTIONS

- New transformers must be registered in `init.go` or equivalent package registration.
- Tests should split large JSON behavior into readable cases.
- Translator changes often require executor/registry tests when model names or thinking options are involved.

## ANTI-PATTERNS

- No HTTP/network calls.
- No credential selection, quota, fallback, or provider availability logic.
- Do not duplicate executor payload config logic here.
