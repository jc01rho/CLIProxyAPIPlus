package helps

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestSanitizeCodexInputItemIDsFillsMissingToolCallNames(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		itemType string
		itemName string
		want     string
	}{
		{name: "missing function call name", itemType: "function_call", want: "unknown"},
		{name: "empty function call name", itemType: "function_call", itemName: `,"name":""`, want: "unknown"},
		{name: "blank custom tool call name", itemType: "custom_tool_call", itemName: `,"name":"   "`, want: "unknown"},
		{name: "valid custom tool call name", itemType: "custom_tool_call", itemName: `,"name":"lookup"`, want: "lookup"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			// Given
			body := []byte(`{"input":[{"type":"` + testCase.itemType + `","id":"item-1","call_id":"call-1"` + testCase.itemName + `}]}`)

			// When
			got := SanitizeCodexInputItemIDs(body)

			// Then
			if actual := gjson.GetBytes(got, "input.0.name").String(); actual != testCase.want {
				t.Fatalf("input.0.name = %q, want %q; payload=%s", actual, testCase.want, got)
			}
		})
	}
}
