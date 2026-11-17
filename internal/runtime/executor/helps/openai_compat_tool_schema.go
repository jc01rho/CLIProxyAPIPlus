package helps

import (
	"fmt"

	"github.com/MoeForge/moego/ojson"
)

// openAIToolSchemaTopLevelKeywords are the JSON Schema keywords strict
// OpenAI-style upstreams refuse next to a function's top-level parameters
// ("schema must have type 'object' and not have 'oneOf'/'anyOf'/'allOf'/
// 'enum'/'const'/'not' at the top level").
var openAIToolSchemaTopLevelKeywords = []string{"oneOf", "anyOf", "allOf", "enum", "const", "not"}

// StripOpenAIToolSchemaTopLevelCombinators removes the top-level combinator
// keywords from every tools[].function.parameters object. Nested schemas and
// the object's own type/properties/required are left untouched.
func StripOpenAIToolSchemaTopLevelCombinators(payload []byte) []byte {
	if !ojson.ValidBytes(payload) {
		return payload
	}
	tools := ojson.GetBytes(payload, "tools")
	if !tools.IsArray() {
		return payload
	}

	out := payload
	for i, tool := range tools.Array() {
		params := tool.Get("function.parameters")
		if !params.IsObject() {
			continue
		}
		for _, keyword := range openAIToolSchemaTopLevelKeywords {
			if !params.Get(keyword).Exists() {
				continue
			}
			next, err := ojson.DeleteBytes(out, fmt.Sprintf("tools.%d.function.parameters.%s", i, keyword))
			if err != nil {
				return payload
			}
			out = next
		}
	}
	return out
}
