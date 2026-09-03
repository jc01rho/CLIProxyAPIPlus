package helps

import "testing"

const metaMuseMeasuredSubscriptionUsageEvent = `{
	"type": "response.subscription_usage",
	"subscription": {
		"window": { "used_percent": 42.5, "resets_at": 1788431188, "window_duration_mins": 300 },
		"weekly": { "used_percent": 63, "resets_at": 1788739200 }
	}
}`

func TestParseMetaMuseSubscriptionUsageHeaders_when_SubscriptionUsageSSEEvent(t *testing.T) {
	// Given
	payload := []byte(metaMuseMeasuredSubscriptionUsageEvent)

	// When
	headers := ParseMetaMuseSubscriptionUsageHeaders("response.subscription_usage", payload)

	// Then
	if got := headers.Get("X-Muse-Fivehour-Used-Percent"); got != "42.5" {
		t.Fatalf("five-hour used percent = %q, want 42.5", got)
	}
	if got := headers.Get("X-Muse-Fivehour-Reset-At"); got != "1788431188" {
		t.Fatalf("five-hour reset = %q, want 1788431188", got)
	}
	if got := headers.Get("X-Muse-Weekly-Used-Percent"); got != "63" {
		t.Fatalf("weekly used percent = %q, want 63", got)
	}
	if got := headers.Get("X-Muse-Weekly-Reset-At"); got != "1788739200" {
		t.Fatalf("weekly reset = %q, want 1788739200", got)
	}
}

func TestParseMetaMuseSubscriptionUsageHeaders_when_EventNameDoesNotMatch(t *testing.T) {
	// Given
	payload := []byte(metaMuseMeasuredSubscriptionUsageEvent)

	// When
	headers := ParseMetaMuseSubscriptionUsageHeaders("response.completed", payload)

	// Then
	if headers != nil {
		t.Fatalf("headers = %#v, want nil", headers)
	}
}

func TestParseMetaMuseSubscriptionUsageHeaders_when_FiveHourDurationChanges(t *testing.T) {
	// Given
	payload := []byte(`{
		"type": "response.subscription_usage",
		"subscription": {
			"window": { "used_percent": 10, "resets_at": 1, "window_duration_mins": 60 },
			"weekly": { "used_percent": 20, "resets_at": 2 }
		}
	}`)

	// When
	headers := ParseMetaMuseSubscriptionUsageHeaders("response.subscription_usage", payload)

	// Then
	if headers.Get("X-Muse-Fivehour-Used-Percent") != "" {
		t.Fatalf("five-hour value retained for a mismatched duration: %#v", headers)
	}
	if got := headers.Get("X-Muse-Weekly-Used-Percent"); got != "20" {
		t.Fatalf("weekly used percent = %q, want 20", got)
	}
}
