# EXECUTOR SYSTEM

> Parent: [../../AGENTS.md](../../AGENTS.md)

## OVERVIEW

`runtime/executor/` performs actual upstream calls after request translation and credential selection. It contains provider executors, WebSocket paths, detailed upstream error logging, payload helpers, and many focused tests.

## STRUCTURE

```text
executor/
├── {provider}_executor.go
├── mistral_executor.go        # standalone: OpenAI-compat /v1/chat/completions, reasoning_effort, base_url /v1 stripping
├── commandcode_executor.go    # standalone: custom /alpha/generate, tools/message conversion, SSE streaming
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
| Mistral | `mistral_executor.go` | Standalone: OpenAI-compat `/v1/chat/completions`, reasoning_effort support. `base_url`에서 중복 /v1 제거 주의. |
| CommandCode | `commandcode_executor.go` | Standalone: custom `/alpha/generate` endpoint, tools/message conversion, SSE streaming, null content handling. |
| Copilot logging/routing | `github_copilot_executor.go` | Log requested/resolved/upstream model on errors. |
| TTFT tracking | executor implementations | Time-to-first-token tracking and reporting for performance metrics. |
| Ollama Cloud | `ollama_executor.go` | `/v1/tags` for Ollama Cloud, `/tags` for self-hosted; `/api/chat`; non-OpenAI shape. |
| Shared behavior | `helps/` | Do not duplicate helper logic in provider files. |

## CONVENTIONS

- Apply thinking before root payload config in Gemini paths; filters may remove thinking config afterward.
- 4xx/5xx debug logging should include masked raw request and raw response, provider/auth, resolved model, request id.
- Stream channel sends should respect context cancellation.
- Usage reporting failures should go through the existing usage reporter paths.
- XAI `normalizeXAITools` enforces a hard 200 tools cap regardless of namespace normalization; do not bypass.
- Ollama Cloud API uses `/v1/tags`; self-hosted Ollama uses `/tags`. `FetchOllamaModels` calls `/v1/tags`.
- All Ollama requests to `ollama.com` must be logged on failure.
- CommandCode `content`에 null을 허용하지 않음: null content는 빈 텍스트 배열로 대체.
- Mistral `resolveBaseURL`에서 base_url에 이미 `/v1` suffix가 있으면 제거 (중복 방지).
- CommandCode `max_tokens`는 16384로 설정.

## ANTI-PATTERNS

- Do not perform translator work inside executors except provider-native response wrapping required by that executor.
- Do not add arbitrary upstream timeouts unless an existing liveness/streaming/management path already defines them.
- Do not copy shared proxy/logging/payload code from `helps/` into provider files.
