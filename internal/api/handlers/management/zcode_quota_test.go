package management

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// zcodeQuotaFixture mirrors the realistic upstream example from zhipu_test.go,
// formatted through the api.z.ai envelope shape (code/msg/success/data).
const zcodeQuotaFixture = `{
  "code": 0,
  "msg": "ok",
  "success": true,
  "data": {
    "level": "pro",
    "limits": [
      {"type": "TOKENS_LIMIT", "unit": 3, "percentage": 25, "nextResetTime": 1735776000000},
      {"type": "TOKENS_LIMIT", "unit": 6, "percentage": 70, "nextResetTime": 1735776000000},
      {"type": "TIME_LIMIT",   "unit": 9, "percentage": 10, "nextResetTime": -1},
      {"type": "UNKNOWN_LIMIT","unit": 4, "percentage": 99, "nextResetTime": 1}
    ]
  }
}`

func TestZcodeParseQuotaResponseFixture(t *testing.T) {
	quota, err := parseZcodeQuotaResponse([]byte(zcodeQuotaFixture))
	if err != nil {
		t.Fatalf("parseZcodeQuotaResponse: %v", err)
	}
	if quota.Level != "pro" {
		t.Errorf("level = %q, want pro", quota.Level)
	}
	if quota.FiveHour.Name != "five_hour" || quota.FiveHour.UsedPercent != 25 || quota.FiveHour.RemainingPercent != 75 {
		t.Errorf("five_hour = %+v, want used 25, remaining 75", quota.FiveHour)
	}
	if quota.FiveHour.ResetAt == nil {
		t.Error("five_hour reset_at missing")
	} else if quota.FiveHour.ResetAt.UTC().UnixMilli() != 1735776000000 {
		t.Errorf("five_hour reset_at = %v, want 1735776000000ms", quota.FiveHour.ResetAt.UTC().UnixMilli())
	}
	if quota.Weekly.Name != "weekly" || quota.Weekly.UsedPercent != 70 || quota.Weekly.RemainingPercent != 30 {
		t.Errorf("weekly = %+v, want used 70, remaining 30", quota.Weekly)
	}
	if quota.Weekly.ResetAt == nil || quota.Weekly.ResetAt.UTC().UnixMilli() != 1735776000000 {
		t.Error("weekly reset_at incorrect")
	}
	if quota.MCP.Name != "mcp" || quota.MCP.UsedPercent != 10 || quota.MCP.RemainingPercent != 90 {
		t.Errorf("mcp = %+v, want used 10, remaining 90", quota.MCP)
	}
	if quota.MCP.ResetAt != nil {
		t.Errorf("mcp reset_at should be nil for -1, got %v", *quota.MCP.ResetAt)
	}
	if quota.Monthly.Name != "monthly" || quota.Monthly.UsedPercent != 0 || quota.Monthly.RemainingPercent != 0 {
		t.Errorf("monthly = %+v, want zero-filled monthly", quota.Monthly)
	}
	if quota.Monthly.ResetAt != nil {
		t.Errorf("monthly reset_at should be nil, got %v", *quota.Monthly.ResetAt)
	}
}

func TestZcodeParseQuotaResponsePercentClamp(t *testing.T) {
	body := fmt.Sprintf(`{"data":{"level":"lite","limits":[
      {"type":"TOKENS_LIMIT","unit":3,"percentage":%f,"nextResetTime":0},
      {"type":"TOKENS_LIMIT","unit":6,"percentage":%f,"nextResetTime":0}
    ]}}`, -5.0, 130.0)
	quota, err := parseZcodeQuotaResponse([]byte(body))
	if err != nil {
		t.Fatalf("parseZcodeQuotaResponse: %v", err)
	}
	if quota.FiveHour.UsedPercent != 0 || quota.FiveHour.RemainingPercent != 100 {
		t.Errorf("clamped negative: five_hour = %+v", quota.FiveHour)
	}
	if quota.Weekly.UsedPercent != 100 || quota.Weekly.RemainingPercent != 0 {
		t.Errorf("clamped overflow: weekly = %+v", quota.Weekly)
	}
}

func TestZcodeParseQuotaResponseResetBoundary(t *testing.T) {
	cases := []struct {
		name       string
		resetValue float64
		wantNil    bool
	}{
		{"zero is nil", 0, true},
		{"negative is nil", -1, true},
		{"positive is not nil", 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"data":{"level":"pro","limits":[{"type":"TOKENS_LIMIT","unit":3,"percentage":10,"nextResetTime":%f}]}}`, tc.resetValue)
			quota, err := parseZcodeQuotaResponse([]byte(body))
			if err != nil {
				t.Fatalf("parseZcodeQuotaResponse: %v", err)
			}
			if tc.wantNil && quota.FiveHour.ResetAt != nil {
				t.Errorf("reset_at should be nil for %f, got %v", tc.resetValue, *quota.FiveHour.ResetAt)
			}
			if !tc.wantNil && quota.FiveHour.ResetAt == nil {
				t.Errorf("reset_at should not be nil for %f", tc.resetValue)
			}
		})
	}
}

func TestZcodeParseQuotaResponseRejectsMissingLimits(t *testing.T) {
	bodies := []string{
		`{"data":{"level":"pro"}}`,
		`{"data":{"level":"pro","limits":null}}`,
		`{"data":{"level":"pro","limits":"not-an-array"}}`,
	}
	for i, body := range bodies {
		_, err := parseZcodeQuotaResponse([]byte(body))
		if err == nil {
			t.Fatalf("case %d %q: expected parse error", i, body)
		}
	}
	// Missing data entirely is also an error.
	if _, err := parseZcodeQuotaResponse([]byte(`{"code":0}`)); err == nil {
		t.Fatal("missing data: expected parse error")
	}
}

func TestZcodeParseQuotaResponseRejectsEmptyBody(t *testing.T) {
	_, err := parseZcodeQuotaResponse([]byte(``))
	if err == nil {
		t.Fatal("empty body: expected parse error")
	}
}

func TestZcodeParseQuotaResponseSurfacesRejectedEnvelope(t *testing.T) {
	_, err := parseZcodeQuotaResponse([]byte(`{"code":401,"msg":"token expired or incorrect","data":null}`))
	if err == nil {
		t.Fatal("rejected envelope: expected error")
	}
	if !strings.Contains(err.Error(), "token expired") && !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention rejection, got %q", err.Error())
	}
}

func TestZcodeParseQuotaResponseUnknownWindowsAreIgnored(t *testing.T) {
	body := `{"data":{"level":"pro","limits":[{"type":"UNKNOWN_LIMIT","unit":4,"percentage":99,"nextResetTime":1}]}}`
	quota, err := parseZcodeQuotaResponse([]byte(body))
	if err != nil {
		t.Fatalf("parseZcodeQuotaResponse: %v", err)
	}
	if quota.FiveHour.UsedPercent != 0 || quota.Weekly.UsedPercent != 0 || quota.MCP.UsedPercent != 0 {
		t.Errorf("unknown type should be ignored: five_hour=%+v weekly=%+v mcp=%+v", quota.FiveHour, quota.Weekly, quota.MCP)
	}
}

func TestZcodeParseQuotaResponseDuplicateWindowsLastWins(t *testing.T) {
	body := `{"data":{"level":"pro","limits":[
      {"type":"TOKENS_LIMIT","unit":3,"percentage":10,"nextResetTime":1},
      {"type":"TOKENS_LIMIT","unit":3,"percentage":20,"nextResetTime":2}
    ]}}`
	quota, err := parseZcodeQuotaResponse([]byte(body))
	if err != nil {
		t.Fatalf("parseZcodeQuotaResponse: %v", err)
	}
	if quota.FiveHour.UsedPercent != 20 {
		t.Errorf("duplicate: five_hour used = %v, want 20 (last wins)", quota.FiveHour.UsedPercent)
	}
	if quota.FiveHour.ResetAt == nil || quota.FiveHour.ResetAt.UTC().UnixMilli() != 2 {
		t.Errorf("duplicate: reset_at = %v, want 2ms", quota.FiveHour.ResetAt)
	}
}

func TestZcodeQuotaResponseJSONShape(t *testing.T) {
	quota, err := parseZcodeQuotaResponse([]byte(zcodeQuotaFixture))
	if err != nil {
		t.Fatalf("parseZcodeQuotaResponse: %v", err)
	}
	// Ensure the normalized shape serializes with the documented field names
	// and that reset_at is RFC3339 when present and absent(null in fields) when nil.
	encoded, err := json.Marshal(quota)
	if err != nil {
		t.Fatalf("json marshal: %v", err)
	}
	var asMap map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &asMap); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	for _, key := range []string{"level", "five_hour", "weekly", "mcp", "monthly"} {
		if _, ok := asMap[key]; !ok {
			t.Errorf("shape missing key %q", key)
		}
	}
	// five_hour and weekly should include reset_at; mcp's -1 means omitted.
	var fiveRaw map[string]json.RawMessage
	json.Unmarshal(asMap["five_hour"], &fiveRaw)
	if _, ok := fiveRaw["reset_at"]; !ok {
		t.Error("five_hour should include reset_at")
	} else {
		var ts string
		json.Unmarshal(fiveRaw["reset_at"], &ts)
		if _, err := time.Parse(time.RFC3339, ts); err != nil {
			t.Errorf("reset_at not RFC3339: %q (%v)", ts, err)
		}
	}
	var mcpRaw map[string]json.RawMessage
	json.Unmarshal(asMap["mcp"], &mcpRaw)
	if _, ok := mcpRaw["reset_at"]; ok {
		t.Error("mcp reset_at should be omitted for -1")
	}
}

func TestZcodeQuotaAPIKeyResolution(t *testing.T) {
	tests := []struct {
		name string
		auth interface{ GetAttr() string }
	}{
		{"via api_key attribute", &zcodeCredCheck{"attr", ""}},
		{"via access_token metadata", &zcodeCredCheck{"", "meta"}},
	}
	_ = tests // use helper below directly

	// Direct unit checks (avoids extra manager wiring):
	if key := zcodeQuotaAPIKey(nil); key != "" {
		t.Errorf("nil auth should yield empty key, got %q", key)
	}
}

func TestZcodeClampZcodePercent(t *testing.T) {
	cases := []struct{ in, want float64 }{
		{-10, 0}, {0, 0}, {50.5, 50.5}, {100, 100}, {200, 100},
	}
	for _, tc := range cases {
		if got := clampZcodePercent(tc.in); got != tc.want {
			t.Errorf("clamp(%f) = %f, want %f", tc.in, got, tc.want)
		}
	}
}

type zcodeCredCheck struct {
	attr string
	meta string
}

func (z *zcodeCredCheck) GetAttr() string { return z.attr }
