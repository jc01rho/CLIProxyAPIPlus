package helps

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeOpenAISystemContentAsString(t *testing.T) {
	for _, tc := range []struct {
		name, role, content, want string
	}{
		{"text blocks", "system", `[{"type":"text","text":"first"},{"type":"text","text":"second"}]`, `"first\nsecond"`},
		{"whitespace", "system", `[{"type":"text","text":" first "},{"type":"text","text":"\nsecond"}]`, `" first \n\nsecond"`},
		{"empty array", "system", `[]`, `""`},
		{"string", "system", `"unchanged"`, `"unchanged"`},
		{"null", "system", `null`, `null`},
		{"image", "system", `[{"type":"text","text":"first"},{"type":"image_url","image_url":{"url":"data:image/png;base64,eA=="}}]`, ""},
		{"invalid text", "system", `[{"type":"text","text":7}]`, ""},
		{"user", "user", `[{"type":"text","text":"first"}]`, ""},
		{"assistant", "assistant", `[{"type":"text","text":"first"}]`, ""},
		{"tool", "tool", `[{"type":"text","text":"first"}]`, ""},
		{"developer", "developer", `[{"type":"text","text":"first"}]`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(`{"messages":[{"role":"` + tc.role + `","content":` + tc.content + `}]}`)
			result, err := NormalizeOpenAISystemContentAsString(payload)
			if err != nil {
				t.Fatal(err)
			}
			want := tc.want
			if want == "" {
				want = tc.content
			}
			if content := gjson.GetBytes(result, "messages.0.content").Raw; content != want {
				t.Fatalf("content = %s, want %s", content, want)
			}
		})
	}
}
