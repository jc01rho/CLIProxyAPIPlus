package management

import (
	"encoding/json"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// commandCodeQuotaFixture mirrors the realistic upstream /alpha/billing/credits
// payload shape (data.credits + data.windowLimits).
const commandCodeQuotaFixture = `{
  "data": {
    "credits": {
      "monthlyCredits": 20,
      "purchasedCredits": 5,
      "freeCredits": 0
    },
    "windowLimits": {
      "fiveHour": {"cap": 100, "used": 25, "resetAt": 1735776000000},
      "weekly":   {"cap": 1000, "used": 700, "resetAt": 1735776000000}
    }
  }
}`

func TestCommandCodeQuotaUnwrapData(t *testing.T) {
	var body map[string]any
	if err := json.Unmarshal([]byte(`{"data":{"credits":{"monthlyCredits":1}}}`), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	payload := commandCodeQuotaUnwrapData(body)
	if _, ok := payload["credits"]; !ok {
		t.Error("unwrap should prefer the nested data shell")
	}
	// A body without a data shell is returned as-is.
	var direct map[string]any
	if err := json.Unmarshal([]byte(`{"credits":{"monthlyCredits":1}}`), &direct); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload := commandCodeQuotaUnwrapData(direct); payload["credits"] == nil {
		t.Error("unwrap should return the body unchanged when no data shell")
	}
}

func TestCommandCodeQuotaOrgID(t *testing.T) {
	var body map[string]any
	if err := json.Unmarshal([]byte(`{"data":{"org":{"id":"org_123"}}}`), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := commandCodeQuotaOrgID(body); got != "org_123" {
		t.Errorf("org id = %q, want org_123", got)
	}
	// Missing org yields empty.
	if err := json.Unmarshal([]byte(`{"data":{}}`), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := commandCodeQuotaOrgID(body); got != "" {
		t.Errorf("org id = %q, want empty", got)
	}
}

func TestCommandCodeQuotaParseWindow(t *testing.T) {
	var body map[string]any
	if err := json.Unmarshal([]byte(commandCodeQuotaFixture), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	limits := commandCodeQuotaWindowLimits(body)

	fiveHour := parseCommandCodeQuotaWindow(limits, "fiveHour")
	if fiveHour == nil {
		t.Fatal("fiveHour window missing")
	}
	if fiveHour.Name != "fiveHour" || fiveHour.UsedPercent != 25 || fiveHour.RemainingPercent != 75 {
		t.Errorf("fiveHour = %+v, want used 25, remaining 75", fiveHour)
	}
	if fiveHour.ResetAt == nil || fiveHour.ResetAt.UTC().UnixMilli() != 1735776000000 {
		t.Errorf("fiveHour reset_at = %v, want 1735776000000ms", fiveHour.ResetAt)
	}

	weekly := parseCommandCodeQuotaWindow(limits, "weekly")
	if weekly == nil || weekly.UsedPercent != 70 || weekly.RemainingPercent != 30 {
		t.Errorf("weekly = %+v, want used 70, remaining 30", weekly)
	}
}

func TestCommandCodeQuotaParseWindowPercentClamp(t *testing.T) {
	body := `{"data":{"windowLimits":{
		"fiveHour": {"cap": 100, "used": -5},
		"weekly":   {"cap": 100, "used": 130}
	}}}`
	var parsed map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	limits := commandCodeQuotaWindowLimits(parsed)
	// A negative used value is malformed (mirrors opencodex: used < 0 → null).
	if w := parseCommandCodeQuotaWindow(limits, "fiveHour"); w != nil {
		t.Errorf("negative used should be nil, got %+v", w)
	}
	// An overflow used value clamps to 100.
	if w := parseCommandCodeQuotaWindow(limits, "weekly"); w == nil || w.UsedPercent != 100 {
		t.Errorf("clamped overflow: weekly = %+v", w)
	}
}

func TestCommandCodeQuotaParseWindowMalformed(t *testing.T) {
	cases := []string{
		`{"data":{"windowLimits":{"fiveHour":{"cap":0,"used":10}}}}`, // zero cap
		`{"data":{"windowLimits":{"fiveHour":{"cap":100}}}}`,         // missing used
		`{"data":{"windowLimits":{"fiveHour":"not-an-object"}}}`,     // wrong type
		`{"data":{}}`, // no windowLimits
	}
	for i, body := range cases {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(body), &parsed); err != nil {
			t.Fatalf("case %d unmarshal: %v", i, err)
		}
		limits := commandCodeQuotaWindowLimits(parsed)
		if w := parseCommandCodeQuotaWindow(limits, "fiveHour"); w != nil {
			t.Errorf("case %d: expected nil window, got %+v", i, w)
		}
	}
}

func TestCommandCodeQuotaNumber(t *testing.T) {
	if v, ok := commandCodeQuotaNumber(float64(3.5)); !ok || v != 3.5 {
		t.Errorf("float64: got %v, %v", v, ok)
	}
	if v, ok := commandCodeQuotaNumber(int(3)); !ok || v != 3 {
		t.Errorf("int: got %v, %v", v, ok)
	}
	if v, ok := commandCodeQuotaNumber(json.Number("7")); !ok || v != 7 {
		t.Errorf("json.Number: got %v, %v", v, ok)
	}
	if _, ok := commandCodeQuotaNumber("nope"); ok {
		t.Error("string should not coerce to number")
	}
}

func TestCommandCodeQuotaTime(t *testing.T) {
	// Epoch seconds.
	if t0, ok := commandCodeQuotaTime(float64(1735776000)); !ok || t0.UTC().Unix() != 1735776000 {
		t.Errorf("epoch seconds: got %v, %v", t0, ok)
	}
	// Epoch milliseconds.
	if t0, ok := commandCodeQuotaTime(float64(1735776000000)); !ok || t0.UTC().UnixMilli() != 1735776000000 {
		t.Errorf("epoch millis: got %v, %v", t0, ok)
	}
	// RFC3339 string.
	if t0, ok := commandCodeQuotaTime("2025-01-02T03:04:05Z"); !ok || t0.UTC().Format(time.RFC3339) != "2025-01-02T03:04:05Z" {
		t.Errorf("rfc3339: got %v, %v", t0, ok)
	}
	if _, ok := commandCodeQuotaTime(true); ok {
		t.Error("bool should not coerce to time")
	}
}

func TestCommandCodeQuotaAPIKey(t *testing.T) {
	auth := &coreauth.Auth{Attributes: map[string]string{"api_key": "sk-123"}}
	if got := commandCodeQuotaAPIKey(auth); got != "sk-123" {
		t.Errorf("api key = %q, want sk-123", got)
	}
	// Missing attributes yields empty.
	if got := commandCodeQuotaAPIKey(&coreauth.Auth{}); got != "" {
		t.Errorf("api key = %q, want empty", got)
	}
}
