package requestmeta

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
)

const latestUpstreamRequestKey = "API_LATEST_UPSTREAM_REQUEST"

// UpstreamRequestSummary captures non-sensitive upstream route metadata.
type UpstreamRequestSummary struct {
	URL      string
	Method   string
	Provider string
	AuthID   string
}

func RecordLatestUpstreamRequest(ctx context.Context, summary UpstreamRequestSummary) {
	ginCtx := ginContextFrom(ctx)
	if ginCtx == nil {
		return
	}
	ginCtx.Set(latestUpstreamRequestKey, UpstreamRequestSummary{
		URL:      strings.TrimSpace(summary.URL),
		Method:   strings.TrimSpace(summary.Method),
		Provider: strings.TrimSpace(summary.Provider),
		AuthID:   strings.TrimSpace(summary.AuthID),
	})
}

func LatestUpstreamRequest(ctx context.Context) (UpstreamRequestSummary, bool) {
	ginCtx := ginContextFrom(ctx)
	if ginCtx == nil {
		return UpstreamRequestSummary{}, false
	}
	value, exists := ginCtx.Get(latestUpstreamRequestKey)
	if !exists {
		return UpstreamRequestSummary{}, false
	}
	summary, ok := value.(UpstreamRequestSummary)
	return summary, ok
}

func ginContextFrom(ctx context.Context) *gin.Context {
	if ctx == nil {
		return nil
	}
	ginCtx, _ := ctx.Value("gin").(*gin.Context)
	return ginCtx
}
