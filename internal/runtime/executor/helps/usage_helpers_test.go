package helps

import (
	"context"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestParseOpenAIUsageChatCompletions(t *testing.T) {
	data := []byte(`{"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":5}}}`)
	detail := ParseOpenAIUsage(data)
	if detail.InputTokens != 1 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 1)
	}
	if detail.OutputTokens != 2 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 2)
	}
	if detail.TotalTokens != 3 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 3)
	}
	if detail.CachedTokens != 4 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 4)
	}
	if detail.ReasoningTokens != 5 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 5)
	}
}

func TestParseOpenAIUsageResponses(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30,"input_tokens_details":{"cached_tokens":7},"output_tokens_details":{"reasoning_tokens":9}}}`)
	detail := ParseOpenAIUsage(data)
	if detail.InputTokens != 10 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 10)
	}
	if detail.OutputTokens != 20 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 20)
	}
	if detail.TotalTokens != 30 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 30)
	}
	if detail.CachedTokens != 7 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 7)
	}
	if detail.ReasoningTokens != 9 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 9)
	}
}

func TestUsageReporterBuildRecordIncludesLatency(t *testing.T) {
	reporter := &UsageReporter{
		provider:    "openai",
		model:       "gpt-5.4",
		requestedAt: time.Now().Add(-1500 * time.Millisecond),
	}

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if record.Latency < time.Second {
		t.Fatalf("latency = %v, want >= 1s", record.Latency)
	}
	if record.Latency > 3*time.Second {
		t.Fatalf("latency = %v, want <= 3s", record.Latency)
	}
}

func TestStoreUsageDetailInContext(t *testing.T) {
	c := &gin.Context{}
	ctx := context.WithValue(context.Background(), "gin", c)

	detail := usage.Detail{
		InputTokens:     100,
		OutputTokens:    200,
		TotalTokens:     300,
		CachedTokens:    50,
		ReasoningTokens: 10,
	}

	storeUsageDetailInContext(ctx, detail)

	stored, exists := c.Get("usageDetail")
	if !exists {
		t.Fatal("usageDetail not stored in gin context")
	}

	storedDetail, ok := stored.(usage.Detail)
	if !ok {
		t.Fatalf("stored value is not usage.Detail, got %T", stored)
	}

	if storedDetail.InputTokens != 100 {
		t.Fatalf("InputTokens = %d, want 100", storedDetail.InputTokens)
	}
	if storedDetail.OutputTokens != 200 {
		t.Fatalf("OutputTokens = %d, want 200", storedDetail.OutputTokens)
	}
	if storedDetail.TotalTokens != 300 {
		t.Fatalf("TotalTokens = %d, want 300", storedDetail.TotalTokens)
	}
	if storedDetail.CachedTokens != 50 {
		t.Fatalf("CachedTokens = %d, want 50", storedDetail.CachedTokens)
	}
	if storedDetail.ReasoningTokens != 10 {
		t.Fatalf("ReasoningTokens = %d, want 10", storedDetail.ReasoningTokens)
	}
}
