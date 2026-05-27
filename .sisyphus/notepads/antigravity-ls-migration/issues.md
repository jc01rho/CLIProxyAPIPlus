## LS Crash Issue Analysis - 2026-02-20

### Issue Summary
LS (Language Server) process crashes with "signal: killed" immediately after SendUserCascadeMessage completes successfully (HTTP 200 response received after 33.928s).

---

### Root Cause Identified

**MISSING: StreamCascadeReactiveUpdates subscription**

The ZeroGravity reference implementation (`backend.rs` lines 418-527) maintains an active subscription to `StreamCascadeReactiveUpdates` after sending a cascade message. This is absent in the current implementation.

#### Key Differences from ZeroGravity:

| Aspect | ZeroGravity (Working) | CLIProxy (Crashing) |
|--------|----------------------|---------------------|
| Post-Message | Subscribes to reactive updates | No reactive subscription |
| Stream Reading | Background goroutine consumes chunks | No stream consumer |
| Protocol | Connect protocol envelope handling | Missing |
| Connection Lifecycle | Maintains stream connection | Connection may be orphaned |

#### ZeroGravity Reactive Stream Implementation:

```rust
// backend.rs lines 418-527
async fn stream_reactive_rpc(
    &self,
    rpc_method: &str,
    cascade_id: &str,
) -> Result<tokio::sync::mpsc::Receiver<serde_json::Value>, String> {
    // Uses application/connect+json content-type
    // Sends envelope: [flags:1][length:4][payload]
    // Spawns background task to continuously read chunks
    // Parses Connect protocol frames
}
```

---

### Why This Causes Crashes

1. **LS expects active reader**: After SendUserCascadeMessage, the LS likely expects the client to subscribe to reactive updates to receive streaming responses
2. **Buffer backpressure**: Without a consumer reading the reactive stream, the LS write buffers fill up, potentially causing it to block and eventually be killed
3. **Missing lifecycle signal**: The reactive stream subscription signals to LS that the client is ready to receive streaming updates
4. **USS topic insufficient**: Current USS topic subscription in stub server (`uss-oauth`) handles authentication only, not cascade-specific reactive updates

---

### Current State Analysis

**Files examined:**

1. **antigravity_executor.go** (lines 677-850)
   - `ExecuteStream()` calls `SendUserCascadeMessage` and then reads response body
   - No reactive update subscription before/during message send
   - Response body is read as SSE stream, but this is different from reactive updates

2. **ls_client.go**
   - `SendProto()` sends protobuf requests
   - No stream subscription capability
   - HTTP client has 300s response header timeout

3. **ls_process.go**
   - Handles LS lifecycle (start/stop/healthcheck)
   - Crash recovery mechanism present but triggered after "signal: killed"
   - No reactive stream handling

4. **ls_stub_server.go** (lines 164-169)
   - Handles `SubscribeToUnifiedStateSyncTopic` for USS (oauth token)
   - Only handles initial state + keepalive
   - Does NOT handle cascade-specific reactive updates

5. **ls_warmup.go**
   - Warmup sequence runs initialization RPCs
   - No reactive stream setup in warmup

---

### Required Fix

**Implement StreamCascadeReactiveUpdates subscription** similar to ZeroGravity:

1. **Add subscription before SendUserCascadeMessage:**
   - Open streaming connection to `/exa.language_server_pb.LanguageServerService/StreamCascadeReactiveUpdates`
   - Use `application/connect+json` content-type
   - Send proper Connect protocol envelope

2. **Background stream reader:**
   - Spawn goroutine to continuously read chunks
   - Parse `[flags:1][length:4][payload]` envelope format
   - Handle both data frames (flags=0x00) and end frames (flags=0x02)

3. **Integration with executor:**
   - Subscribe when cascade is created
   - Keep subscription alive during message exchange
   - Close subscription when cascade completes

---

### Technical Details

**Connect Protocol Frame Format:**
```
[1 byte flags] [4 bytes big-endian length] [payload data]

Flags:
- 0x00 = Data frame (JSON payload)
- 0x02 = End frame (stream complete)
```

**Request Format:**
```json
{
  "protocolVersion": 1,
  "id": "<cascade_id>"
}
```

**Headers Required:**
```
Content-Type: application/connect+json
Connect-Protocol-Version: 1
```

---

### Evidence from ZeroGravity

ZeroGravity's `stream_reactive_rpc()` function:
- Creates mpsc channel for streaming updates
- Spawns tokio task to continuously consume chunks
- Parses Connect protocol frames
- Sends parsed JSON to channel
- This keeps the connection alive and LS responsive

---

### Additional Observations

1. **UpdateConversationAnnotations**: ZeroGravity also calls `update_annotations()` after SendUserCascadeMessage (backend.rs lines 207-226) - this might be another fingerprinting issue

2. **Heartbeat**: Current heartbeat runs every ~1s (ls_warmup.go line 77-106) - this is good but not sufficient without reactive subscription

3. **USS Keepalive**: Stub server sends keepalive every 5s for USS topics (ls_stub_server.go line 241-259) - this is correct for USS but unrelated to cascade reactive updates

---

### Conclusion

The LS crash is caused by the **missing reactive updates subscription**. The LS expects a consumer for `StreamCascadeReactiveUpdates` to be active during cascade operations. Without this, the LS likely encounters buffer issues or timeout conditions that result in the process being killed.

**Priority**: HIGH - This is a critical missing feature that causes LS instability
**Fix Complexity**: MEDIUM - Requires implementing Connect protocol streaming support

### Related Code Locations

- ZeroGravity reference: `/home/jc01rho/git/zerogravity-src/src/backend.rs` lines 418-527
- Current stub server: `CLIProxyAPIPlus/internal/runtime/executor/ls_stub_server.go`
- Executor: `CLIProxyAPIPlus/internal/runtime/executor/antigravity_executor.go`
