package helps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/requestmeta"
)

func TestRecordAPIRequestStoresUpstreamSummaryWhenRequestLogDisabled(t *testing.T) {
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	RecordAPIRequest(ctx, nil, UpstreamRequestLog{
		URL:      "https://api.commandcode.ai/alpha/generate",
		Method:   http.MethodPost,
		Provider: "commandcode",
		AuthID:   "auth-commandcode",
		Model:    "nvidia/nemotron-3-ultra-550b-a55b",
	})

	got, ok := requestmeta.LatestUpstreamRequest(ctx)
	if !ok {
		t.Fatalf("expected latest upstream request summary")
	}
	if got.URL != "https://api.commandcode.ai/alpha/generate" ||
		got.Method != http.MethodPost ||
		got.Provider != "commandcode" ||
		got.AuthID != "auth-commandcode" ||
		got.Model != "nvidia/nemotron-3-ultra-550b-a55b" {
		t.Fatalf("unexpected upstream summary: %+v", got)
	}
}
