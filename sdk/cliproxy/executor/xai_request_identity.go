package executor

import (
	"context"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type xaiRequestIdentityKey struct{}

type xaiRequestIdentity struct {
	mu  sync.Mutex
	ids map[string]string
}

const xaiHTTPRequestIdentityKey = "cliproxy.xai.http-request-identity"

// Serializes Gin's separate Get/Set operations during lazy initialization only.
// It retains no requests or credentials; each identity map belongs to its request.
var xaiHTTPIdentityInitMu sync.Mutex

// WithXAIRequestIdentity provides request-owned storage for xAI HTTP retries.
// It never reads or mutates caller Options/Metadata. SDK callers may explicitly
// reuse this context for a logical replay; ordinary manager invocations get
// independent storage even when the caller reuses its context and Options.
func WithXAIRequestIdentity(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(xaiRequestIdentityKey{}).(*xaiRequestIdentity); ok {
		return ctx
	}
	return context.WithValue(ctx, xaiRequestIdentityKey{}, &xaiRequestIdentity{})
}

// XAIRequestID pins a random UUIDv4 to each credential target for this request.
// HTTP handler bootstrap re-entry shares the Gin request owner. WebSocket turns
// must not share that connection-long owner, so they use the execution context.
// No UUID or target map is allocated until an xAI HTTP request asks for one.
func XAIRequestID(ctx context.Context, credentialTarget string) string {
	var state *xaiRequestIdentity
	if ctx != nil {
		state, _ = ctx.Value(xaiRequestIdentityKey{}).(*xaiRequestIdentity)
		if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil && !DownstreamWebsocket(ctx) {
			xaiHTTPIdentityInitMu.Lock()
			stored, _ := ginCtx.Get(xaiHTTPRequestIdentityKey)
			state, _ = stored.(*xaiRequestIdentity)
			if state == nil {
				state = &xaiRequestIdentity{}
				ginCtx.Set(xaiHTTPRequestIdentityKey, state)
			}
			xaiHTTPIdentityInitMu.Unlock()
		}
	}
	if state == nil {
		return uuid.NewString()
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if id := state.ids[credentialTarget]; id != "" {
		return id
	}
	id := uuid.NewString()
	if state.ids == nil {
		state.ids = make(map[string]string)
	}
	state.ids[credentialTarget] = id
	return id
}
