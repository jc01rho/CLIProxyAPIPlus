package helps

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestStripOpenAIToolSchemaTopLevelCombinators(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "top-level anyOf is removed and nested anyOf is kept",
			in:   `{"tools":[{"type":"function","function":{"name":"eval","parameters":{"type":"object","properties":{"action":{"anyOf":[{"type":"string"}]}},"anyOf":[{"required":["code"]},{"required":["cell_id"]}]}}}]}`,
			want: `{"tools":[{"type":"function","function":{"name":"eval","parameters":{"type":"object","properties":{"action":{"anyOf":[{"type":"string"}]}}}}}]}`,
		},
		{
			name: "every forbidden keyword is removed from every tool",
			in:   `{"tools":[{"function":{"parameters":{"type":"object","oneOf":[{}],"allOf":[{}],"enum":[1],"const":1,"not":{}}}},{"function":{"parameters":{"type":"object","anyOf":[{}]}}}]}`,
			want: `{"tools":[{"function":{"parameters":{"type":"object"}}},{"function":{"parameters":{"type":"object"}}}]}`,
		},
		{
			name: "plain schemas and tools without parameters are untouched",
			in:   `{"tools":[{"function":{"name":"a","parameters":{"type":"object","properties":{"x":{"type":"string"}}}}},{"function":{"name":"b"}}]}`,
			want: `{"tools":[{"function":{"name":"a","parameters":{"type":"object","properties":{"x":{"type":"string"}}}}},{"function":{"name":"b"}}]}`,
		},
		{
			name: "payload without tools is untouched",
			in:   `{"messages":[]}`,
			want: `{"messages":[]}`,
		},
		{
			name: "invalid json is returned as is",
			in:   `not json`,
			want: `not json`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := StripOpenAIToolSchemaTopLevelCombinators([]byte(tc.in))
			var gotValue, wantValue any
			if err := json.Unmarshal([]byte(tc.want), &wantValue); err != nil {
				if string(got) != tc.want {
					t.Fatalf("got %s, want %s", got, tc.want)
				}
				return
			}
			if err := json.Unmarshal(got, &gotValue); err != nil {
				t.Fatalf("got invalid JSON %s: %v", got, err)
			}
			if !reflect.DeepEqual(gotValue, wantValue) {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}
