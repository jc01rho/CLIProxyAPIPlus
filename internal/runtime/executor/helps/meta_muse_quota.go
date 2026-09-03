package helps

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

const (
	metaMuseSubscriptionUsageEventType = "response.subscription_usage"
	metaMuseFiveHourWindowMinutes      = 300

	metaMuseFiveHourUsedPercentHeader = "X-Muse-Fivehour-Used-Percent"
	metaMuseFiveHourResetAtHeader     = "X-Muse-Fivehour-Reset-At"
	metaMuseWeeklyUsedPercentHeader   = "X-Muse-Weekly-Used-Percent"
	metaMuseWeeklyResetAtHeader       = "X-Muse-Weekly-Reset-At"
)

type metaMuseSubscriptionUsageEvent struct {
	Type         string                    `json:"type"`
	Subscription metaMuseSubscriptionQuota `json:"subscription"`
}

type metaMuseSubscriptionQuota struct {
	Window *metaMuseQuotaWindow `json:"window"`
	Weekly *metaMuseQuotaWindow `json:"weekly"`
}

type metaMuseQuotaWindow struct {
	UsedPercent        *float64 `json:"used_percent"`
	ResetsAt           *int64   `json:"resets_at"`
	WindowDurationMins *int64   `json:"window_duration_mins"`
}

// ParseMetaMuseSubscriptionUsageHeaders parses one Meta subscription SSE
// frame into normalized internal result headers. They do not represent
// upstream HTTP headers; ExecuteStream adds them to StreamResult.Headers so
// the existing conductor quota observer can persist the passive snapshot.
func ParseMetaMuseSubscriptionUsageHeaders(eventName string, payload []byte) http.Header {
	if strings.TrimSpace(eventName) != metaMuseSubscriptionUsageEventType || len(payload) == 0 {
		return nil
	}

	var event metaMuseSubscriptionUsageEvent
	if err := json.Unmarshal(payload, &event); err != nil || event.Type != metaMuseSubscriptionUsageEventType {
		return nil
	}

	headers := make(http.Header)
	if window := event.Subscription.Window; window != nil &&
		window.WindowDurationMins != nil && *window.WindowDurationMins == metaMuseFiveHourWindowMinutes {
		setMetaMuseQuotaWindowHeaders(headers, metaMuseFiveHourUsedPercentHeader, metaMuseFiveHourResetAtHeader, window)
	}
	if weekly := event.Subscription.Weekly; weekly != nil {
		setMetaMuseQuotaWindowHeaders(headers, metaMuseWeeklyUsedPercentHeader, metaMuseWeeklyResetAtHeader, weekly)
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}

func setMetaMuseQuotaWindowHeaders(headers http.Header, usedPercentHeader, resetAtHeader string, window *metaMuseQuotaWindow) {
	if headers == nil || window == nil {
		return
	}
	if window.UsedPercent != nil && *window.UsedPercent >= 0 && *window.UsedPercent <= 100 {
		headers.Set(usedPercentHeader, strconv.FormatFloat(*window.UsedPercent, 'f', -1, 64))
	}
	if window.ResetsAt != nil && *window.ResetsAt > 0 {
		headers.Set(resetAtHeader, strconv.FormatInt(*window.ResetsAt, 10))
	}
}
