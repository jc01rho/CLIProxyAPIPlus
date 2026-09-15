package auth

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSetFallbackInfoInContextPreservesOriginalRequestedModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	ctx = SetFallbackInfoInContext(ctx, "command-deepseek-v4.1-flash", "lower-coding")
	ctx = SetFallbackInfoInContext(ctx, "lower-coding", "glm-5.3")

	requested, actual := GetFallbackInfoFromContext(ctx)
	if requested != "command-deepseek-v4.1-flash" || actual != "glm-5.3" {
		t.Fatalf("context fallback info = (%q, %q), want (command-deepseek-v4.1-flash, glm-5.3)", requested, actual)
	}

	value, exists := ginCtx.Get(GinFallbackInfoKey)
	if !exists {
		t.Fatal("gin fallback info was not set")
	}
	info, ok := value.(map[string]string)
	if !ok {
		t.Fatalf("gin fallback info type = %T, want map[string]string", value)
	}
	if info["requested_model"] != "command-deepseek-v4.1-flash" || info["actual_model"] != "glm-5.3" {
		t.Fatalf("gin fallback info = %v, want original requested model preserved through alias mapping", info)
	}
}
