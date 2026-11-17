# Claude OAuth wire vectors (Claude Code 2.1.302 persona)

`vectors.json` holds 7 request vectors for an OAuth `POST /v1/messages`
as anthropic-auth's **OpenCode 1** path sends it. Every case is labelled
`"source": "reconstructed"`: it was produced by running this repository's
code, not captured from Claude Code. Shaped for
`crates/cortexkit-anthropic-policy/test-vectors/claude-oauth-wire/`; the
field-by-field explanation is `blueprint.md` (written for 2.1.280; see
"Changes for 2.1.302" below).

## Cases

| id | model | what it covers |
|----|-------|----------------|
| `fable51-first-turn` | `claude-fable-5-1` | first turn: billing block with `cc_prompt_id` only; system sanitising and identity block; forced adaptive thinking; effort pinned; `fallbacks:"default"` + server-side-fallback beta; per-turn-control beta; `diagnostics.previous_message_id: null`; explicit cache (host breakpoints → `ttl:"1h"`) |
| `opus55-continuation-replayed-thinking` | `claude-opus-5-5` | continuation replaying a signed thinking block; `cc_prev_req` + `cc_prompt_id`; per-turn-control beta; `thinking.block_binding` + thinking-binding beta (account setting `drop_block`); hybrid cache anchors |
| `opus55-effort-change` | `claude-opus-5-5` | effort high → low on turn 2: inserted `{"role":"system","content":[],"output_config":{"effort":"low"}}`, top-level effort = baseline `high`, internal marker blocks removed; cache off (5-minute breakpoints) |
| `sonnet55-between-tools` | `claude-sonnet-5-5` | host `thinking: disabled` → `between_tools`, effort `xhigh` → `high`; no fallback; automatic cache (one top-level `cache_control`) |
| `haiku55-tools-continuation` | `claude-haiku-5-5` | tools: `mcp_Read`/`mcp_Bash` in `tools[]` and replayed `tool_use`; trailing whitespace text after `tool_use` removed; `temperature` removed; `tool_result` continuation |
| `sonnet55-full-agent-subagent` | `claude-sonnet-5-5` | main beta list, per-turn-control after prompt-caching-scope; forced `tool_choice` removed; subagent: no 1h cache, no lineage |
| `opus48-structured-output-fast` | `claude-opus-4-8` | main beta list plus structured-output beta for `json_schema`; `speed:"fast"` + fast-mode beta; host budget thinking unchanged |

## Shape of each case

- `input`: host (`opencode-1`), route (`oauth`), `host_headers` (what the
  OpenCode 1 host puts on the request, including the session headers the
  plugin removes), `host_body` (the AI SDK body as a JSON string),
  `options` (plugin settings for the request). Effort-change cases also have
  `effort_markers` and `lowered_host_body`: OpenCode 1 adds internal marker
  text blocks to user messages before lowering, and `lowered_host_body` is the
  body that actually reaches the fetch wrapper.
- `output`: `url`; `request_line`; `headers_wire_order` (name/value pairs in
  the order Bun writes them, including Bun's own `Connection`, `Host`,
  `Accept-Encoding`, `Content-Length`); `body` (exact bytes sent, as a JSON
  string); `body_sha256`; `body_bytes`; `billing_header` (text of
  `system[0]`); `cc_version_suffix` (first user text, the 3 sampled
  characters, the suffix); `cch`; `cch_canonical_body` (the exact bytes fed
  to xxHash64: `model` set to `""`, `max_tokens` removed, `cch=00000`).
- `primitives`: the unit checks `xxHash64("hello world test message") → cc124`
  and `computeCcVersionSuffix("short", "2.1.258") → 587`.

## Held fixed

Each of these replaces a random, account-specific or machine-specific value
(listed again under `fixed` in the file):

| value | fixed as | real source |
|-------|----------|-------------|
| OAuth access token | `sk-ant-oat01-PLACEHOLDER` | account credential |
| account UUID (`metadata.user_id.account_uuid`) | `00000000-0000-4000-8000-000000000000` | verified account |
| device id | 64 × `0` | 32 random bytes per account identity |
| Claude Code session id (`x-claude-code-session-id`, `metadata.user_id.session_id`) | `11111111-1111-4111-8111-111111111111` | random UUID per account identity |
| `x-client-request-id` | `22222222-2222-4222-8222-222222222222` | `crypto.randomUUID()` per request; the generator overwrites the header after it is built |
| `cc_prompt_id` | `33333333-3333-4333-8333-333333333333` | random UUID per user prompt |
| `cc_prev_req` | `req_PLACEHOLDER0000000000000001` | previous response's `request-id` |
| `diagnostics.previous_message_id` | `msg_PLACEHOLDER0000000000000001` or `null` | previous response's message id |
| OpenCode session ids | `ses_PLACEHOLDER_…` | OpenCode session (`x-session-affinity`) |
| tool-use ids, thinking signature | `toolu_PLACEHOLDER_0001`, `SIGNATURE_PLACEHOLDER_0001` | model output |
| `x-stainless-os` / `x-stainless-arch` | `MacOS` / `arm64` | `process.platform` / `process.arch` |
| `Host` | `api.anthropic.com` | the capture targets a local socket, so the generator writes the production host back |

No time-dependent field exists in the request. Nothing else is
substituted: suffixes, `cch` values, beta lists and bodies are exactly what
the code produced from these inputs.

## Changes for 2.1.302

The 2.1.302 set was regenerated from the same `input` of every case, with
the same fixed values and placeholders. Compared with 2.1.280:

- `cc_version` is `2.1.302.<suffix>` and `User-Agent` is
  `claude-cli/2.1.302 (external, cli)`; the suffix and `cch` rules did not
  change.
- `x-stainless-package-version` is `0.128.0`.
- A billing block with `cc_prompt_id` also ends with
  `cc_turn_origin=human; cc_prompt_index=N; cc_turn_index=N;`, where `N` is
  the number of user prompts in `messages` (user messages that carry anything
  besides tool results).
- New headers: `x-claude-code-request-class` (`main`) on every request and
  `x-claude-code-prompt-id` (the billing `cc_prompt_id`) when there is one.
- Beta selection now follows request class, not body-shape heuristics. All
  main turns use the captured main list, including thinking-binding-controls
  and thinking-display-updates even without a binding field. The binding
  field itself still needs replayed thinking and an explicit setting.
  `claude-code-20250219` comes first except on Haiku 5.5, where it follows
  prompt-caching-scope. Per-turn-control follows that scope beta (and
  claude-code on Haiku 5.5) for Sonnet 5.5, Opus 5.5, Fable 5.1 and Haiku
  5.5, but not Opus 4.8. Main JSON-schema output adds structured-output at
  its existing position before cache-diagnosis; fast mode and server-side
  fallback retain their existing feature-beta selection and order.
- Every vector is an uncompressed (identity) send. Claude Code 2.1.302 sends
  bodies of 4,096 characters or more to api.anthropic.com gzipped, with
  `cch=00000` and whitespace padding; that encoding is not part of these
  vectors.
- The obsolete `mid-conversation-system-2026-04-07` and
  `mid-conversation-output-config-2026-07-01` betas are absent. Fable 5.1
  effort changes use `per-turn-control-2026-07-01`; effort-marker bodies
  remain empty system messages with `output_config.effort`.
- Advisor-tool and fallback-credit are absent: these requests enable no
  advisor and carry no fallback credit code. Server-side-fallback remains
  a separate feature beta, not a fallback-credit substitute.
- Auxiliary one-token quota probes use the captured quota list; other
  auxiliary requests use title/summary betas (with structured-output only
  for JSON schema). Those classes are covered by Core tests, not these
  main-request vectors.
- No baseline beta difference remains from the capture. The body still uses
  `thinking.display: "summarized"` where Claude Code sends `"updates"`,
  because `"updates"` streams no thinking text. Advertising the
  thinking-display-updates beta alone does not suppress summarized thinking.

## How the outputs were generated

The outputs come from the real functions, called in the order
`packages/opencode/src/index.ts` `sendWithAccessToken` uses them:
`mergeHeaders` and deletion of the internal headers,
`markOpenCodeEffortTransitions`/`encodeOpenCodeEffortPlan` for effort plans,
`rewriteRequestBody` (which signs the body), `setOAuthHeaders`,
`addFastModeBetaHeader`, `rewriteUrl`, then Bun's `fetch` to a local raw TCP
listener, which records the request line, headers and body bytes as written.
These are the steps anthropic-auth's `claude-oauth-wire-vectors.test.ts`
runs; the 2.1.302 set was produced by running them on each case's `input`
and writing the result to `output`. The harness checks that the captured
body equals the signed body, that recomputing `cch` from
`cch_canonical_body` and re-signing reproduce it, and that the suffix follows
from the first user text.

The beta-corrected outputs were regenerated with Bun 1.3.14 on Linux (the
version CI pins). `vectors.json` has SHA-256
`47072a18eb3640dd03502a4f24e7e9b5325160faffacdf47cb99db6462cb4ba5`.

Not covered: Pi and OpenCode 2 (their differences are described in
`blueprint.md`), lane-start warm requests, and CacheKeep prewarms.
