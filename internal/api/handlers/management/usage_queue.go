package management

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/redisqueue"
)

const (
	defaultUsageQueueDrainCount = 50
	maxUsageQueueDrainCount     = 500
)

type usageQueueResponse struct {
	Count int               `json:"count"`
	Items []json.RawMessage `json:"items"`
}

// GetUsageQueue drains request-level usage event payloads from the in-memory usage queue.
func (h *Handler) GetUsageQueue(c *gin.Context) {
	count := defaultUsageQueueDrainCount
	if raw := strings.TrimSpace(c.Query("count")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid count"})
			return
		}
		count = parsed
	}
	if count > maxUsageQueueDrainCount {
		count = maxUsageQueueDrainCount
	}

	items := redisqueue.PopOldest(count)
	out := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		if !json.Valid(item) {
			continue
		}
		out = append(out, json.RawMessage(append([]byte(nil), item...)))
	}
	c.JSON(http.StatusOK, usageQueueResponse{Count: len(out), Items: out})
}
