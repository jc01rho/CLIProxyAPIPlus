# Headroom Integration Guide

**Purpose**: Compress request bodies (conversation history, tool results, system prompts) before they reach CLIProxyAPIPlus, reducing upstream token usage by 60-95%.

[Headroom](https://github.com/chopratejas/headroom) is a context compression layer for AI agents. It sits in front of CLIProxyAPIPlus as a reverse proxy, compressing everything the client sends before forwarding it to the proxy — fewer tokens, same answers.

## Architecture

```
Client / Agent
    |
    |  requests (messages, tool outputs, conversation history)
    v
 Headroom (proxy mode)          ← compresses request body
    |
    v
 CLIProxyAPIPlus                ← routes to provider, handles auth/selection
    |
    v
 LLM Provider (OpenAI, Anthropic, Google, etc.)
```

Headroom sits **in front of** CLIProxyAPIPlus. This is the optimal placement because:

1. **CacheAligner** sees the client's original format (OpenAI-compat messages) and stabilizes prefixes for KV cache hits
2. **SmartCrusher** compresses tool results and large JSON arrays in the request body
3. **CCR** (Compress-Cache-Retrieve) stores originals locally — the client can retrieve full data via `headroom_retrieve`
4. CLIProxyAPIPlus receives a smaller request and processes it normally — **zero code changes required**

## Quick Start

### 1. Install Headroom

```bash
pip install "headroom-ai[proxy]"
# or
docker pull ghcr.io/chopratejas/headroom:latest
```

### 2. Run Headroom in proxy mode

```bash
# Point headroom at CLIProxyAPIPlus
headroom proxy --port 8787 --target http://localhost:8317
```

### 3. Point your client at Headroom

```bash
# Before: client → CLIProxyAPIPlus:8317
# After:  client → headroom:8787 → CLIProxyAPIPlus:8317

export OPENAI_BASE_URL=http://localhost:8787/v1
```

CLIProxyAPIPlus sees the compressed request and routes it as usual. The LLM provider receives fewer tokens. The response passes through unchanged.

## Docker Compose

See [`docker-compose.headroom.yml`](../../docker-compose.headroom.yml) for a ready-to-use setup:

```bash
docker compose -f docker-compose.headroom.yml up
```

This starts:
- `headroom` on port **8787** (client-facing)
- `cli-proxy-api-plus` on port **8317** (internal, headroom → cliproxy)

## What gets compressed

| Content type               | What happens                                              | Typical savings |
| -------------------------- | --------------------------------------------------------- | --------------- |
| JSON arrays (tool outputs) | Statistical analysis keeps errors, anomalies, boundaries  | 70-90%          |
| Build/test logs            | Keeps failures and errors, drops passing noise            | 80-95%          |
| Search results             | Ranks by relevance, keeps top matches                     | 60-80%          |
| Conversation history       | Rolling window or intelligent context (score-based)       | 30-50%          |

**Headroom does NOT compress:**
- User messages (intent preserved exactly)
- System prompts (content preserved; dynamic parts relocated for caching)
- Model responses (returned unchanged)
- Short content under 200 tokens (overhead exceeds savings)

## Key features for CLIProxyAPIPlus users

### CacheAligner

Extracts dynamic content (dates, UUIDs, session tokens) from the system prompt and moves it to the end. This stabilizes the prefix so provider caches hit on repeated calls — especially valuable with CLIProxyAPIPlus's multi-provider routing where the same conversation may be routed to different providers.

### CCR (Compress-Cache-Retrieve)

When SmartCrusher compresses a tool output or IntelligentContext drops messages, the original is stored locally. The LLM gets a `headroom_retrieve` tool and can fetch full originals when it needs more detail. Compression is aggressive but reversible.

### Token savings impact on CLIProxyAPIPlus

- **Lower upstream costs**: fewer tokens sent to LLM providers
- **Faster responses**: smaller request bodies, faster processing
- **Better cache utilization**: CacheAligner increases KV cache hit rates across providers
- **No auth/routing impact**: CLIProxyAPIPlus handles all auth, model selection, and failover normally

## Configuration

Headroom proxy mode supports environment variables:

```bash
# Headroom proxy settings
HEADROOM_PORT=8787                    # Port headroom listens on
HEADROOM_TARGET=http://cliproxy:8317  # CLIProxyAPIPlus address
HEADROOM_LOG_LEVEL=info               # Logging verbosity

# Compression settings
HEADROOM_MIN_TOKENS=200               # Skip compression below this threshold
HEADROOM_COMPRESSION_STRATEGY=auto    # auto, smart-crusher, code-compressor, kompress
```

See [Headroom docs](https://headroom-docs.vercel.app/docs/configuration) for full configuration options.

## Limitations

- Headroom is Python/Rust — requires Python 3.10+ or Docker
- Adds ~10-50ms latency per request (compression overhead)
- Streaming responses pass through unchanged (compression applies to requests only)
- CLIProxyAPIPlus management endpoints (`/v0/management/*`) should bypass headroom — configure headroom's route exclusions accordingly

## References

- Headroom GitHub: https://github.com/chopratejas/headroom
- Headroom docs: https://headroom-docs.vercel.app/docs
- Headroom architecture: https://headroom-docs.vercel.app/docs/architecture
- Headroom proxy guide: https://headroom-docs.vercel.app/docs/proxy
