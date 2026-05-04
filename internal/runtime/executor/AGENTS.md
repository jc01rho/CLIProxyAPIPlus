# EXECUTOR SYSTEM

> Parent: [../../AGENTS.md](../../AGENTS.md)

## OVERVIEW

`runtime/executor/` performs actual upstream calls after request translation and credential selection. It contains provider executors, WebSocket paths, detailed upstream error logging, payload helpers, and many focused tests.

## STRUCTURE

```text
executor/
├── {provider}_executor.go
├── openai_compat_executor.go
├── codex_websockets_executor.go
├── ollama_executor.go
├── helps/                 # shared payload, logging, proxy, cache helpers
└── *_test.go
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Provider execution | `{provider}_executor.go` | Auth injection + upstream request/response. |
| OpenAI-compatible providers | `openai_compat_executor.go` | Payload config and thinking order matters. |
| Copilot logging/routing | `github_copilot_executor.go` | Log requested/resolved/upstream model on errors. |
| Ollama Cloud | `ollama_executor.go` | `/api/tags` and `/api/chat`; non-OpenAI shape. |
| Shared behavior | `helps/` | Do not duplicate helper logic in provider files. |

## CONVENTIONS

- Apply thinking before root payload config in Gemini paths; filters may remove thinking config afterward.
- 4xx/5xx debug logging should include masked raw request and raw response, provider/auth, resolved model, request id.
- Stream channel sends should respect context cancellation.
- Usage reporting failures should go through the existing usage reporter paths.

## ANTI-PATTERNS

- Do not perform translator work inside executors except provider-native response wrapping required by that executor.
- Do not add arbitrary upstream timeouts unless an existing liveness/streaming/management path already defines them.
- Do not copy shared proxy/logging/payload code from `helps/` into provider files.
