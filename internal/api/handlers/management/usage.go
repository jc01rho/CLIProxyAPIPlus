package management

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/redisqueue"
)

const maxUsageQueueDrainCount = 500

type usageQueueRecord []byte

func (r usageQueueRecord) MarshalJSON() ([]byte, error) {
	if json.Valid(r) {
		return append([]byte(nil), r...), nil
	}
	return json.Marshal(string(r))
}

func maskUsageQueueAPIKey(raw string) string {
	if raw == "" {
		return ""
	}
	if len(raw) <= 8 {
		return raw[:2] + "***"
	}
	return raw[:4] + "***" + raw[len(raw)-4:]
}

func maskUsageQueueRecord(item []byte) usageQueueRecord {
	var payload map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(item, &payload); errUnmarshal != nil {
		return usageQueueRecord(append([]byte(nil), item...))
	}
	if rawAPIKey, ok := payload["api_key"]; ok {
		var apiKey string
		if errUnmarshal := json.Unmarshal(rawAPIKey, &apiKey); errUnmarshal == nil {
			if masked, errMarshal := json.Marshal(maskUsageQueueAPIKey(apiKey)); errMarshal == nil {
				payload["api_key"] = masked
			}
		}
	}
	out, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return usageQueueRecord(append([]byte(nil), item...))
	}
	return usageQueueRecord(out)
}

// GetUsageQueue pops queued usage records from the usage queue.
func (h *Handler) GetUsageQueue(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}

	count, errCount := parseUsageQueueCount(c.Query("count"))
	if errCount != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errCount.Error()})
		return
	}
	if count > maxUsageQueueDrainCount {
		count = maxUsageQueueDrainCount
	}

	items := redisqueue.PopOldest(count)
	records := make([]usageQueueRecord, 0, len(items))
	for _, item := range items {
		if !json.Valid(item) {
			continue
		}
		records = append(records, maskUsageQueueRecord(item))
	}

	c.JSON(http.StatusOK, records)
}

func parseUsageQueueCount(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 1, nil
	}
	count, errCount := strconv.Atoi(value)
	if errCount != nil || count <= 0 {
		return 0, errors.New("count must be a positive integer")
	}
	return count, nil
}
