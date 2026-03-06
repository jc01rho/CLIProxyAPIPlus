# LS Startup Initialization - Implementation Learnings

## Changes Made

### 1. AntigravityExecutor (antigravity_executor.go)

Added `Initialize()` method to support immediate LS startup at app initialization:

```go
func (e *AntigravityExecutor) Initialize(ctx context.Context) error
func (e *AntigravityExecutor) initializeLSAtStartup(ctx context.Context) error
```

- `Initialize()` is called asynchronously when executor is registered
- `initializeLSAtStartup()` performs the actual LS startup and warmup sequence
- LS process starts immediately, warmup runs synchronously (blocking)
- Heartbeat goroutine starts after successful initialization

### 2. Modified ensureLSRunning (antigravity_executor.go)

Changed behavior from "always lazy init" to "check health first, fallback to lazy init":

- **Primary path**: If `e.initialized == true`, just check health via `EnsureRunning()`
- **Fallback path**: If startup failed earlier, use lazy initialization (backward compatible)

### 3. InitializableExecutor Interface (auth/conductor.go)

Added new optional interface following existing `ExecutionSessionCloser` pattern:

```go
type InitializableExecutor interface {
    Initialize(ctx context.Context) error
}
```

### 4. RegisterExecutor Enhancement (auth/conductor.go)

Modified `RegisterExecutor()` to detect and call `Initialize()` asynchronously:

```go
if initable, ok := executor.(InitializableExecutor); ok && initable != nil {
    go func() {
        ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
        defer cancel()
        if err := initable.Initialize(ctx); err != nil {
            log.Warnf("executor %s: initialization failed: %v", provider, err)
        }
    }()
}
```

### 5. Bug Fix (auth/conductor.go)

Fixed pre-existing bug: `return new(*retryAfter)` → `return retryAfter`

## Architecture Pattern

This follows the **ZeroGravity pattern**:

```rust
// ZeroGravity main.rs
let mut ls = StandaloneLS::spawn(&main_config, ...)?;
ls.wait_ready(10).await?;  // Blocking wait at startup
```

Our Go implementation:

```go
// service.go ensureExecutorsForAuthWithMode
case "antigravity":
    s.coreManager.RegisterExecutor(executor.NewAntigravityExecutor(s.cfg))
    // Initialize() called asynchronously by RegisterExecutor
```

## Benefits

1. **Faster first request**: LS already running when first request arrives
2. **Predictable startup**: Errors discovered at app startup, not first request
3. **Health check ready**: LS is ready for health checks immediately
4. **Backward compatible**: Lazy init remains as fallback for edge cases

## Testing

- Build passes: ✓
- executor package tests: ✓
- auth/conductor tests: ✓
- go vet: ✓

## Future Considerations

- Could add configuration option to disable immediate startup (for resource-constrained environments)
- Could add health check endpoint for LS specifically
- Could expose initialization status via metrics
