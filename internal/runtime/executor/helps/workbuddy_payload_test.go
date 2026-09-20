package helps

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func normalizeWorkBuddyForTest(t *testing.T, body string, efforts []string, defaultEffort string) (map[string]any, bool) {
	t.Helper()
	out, changed := NormalizeWorkBuddyPayload([]byte(body), efforts, defaultEffort)
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal normalized payload: %v (payload=%s)", err, out)
	}
	return obj, changed
}

func workBuddyMessages(t *testing.T, obj map[string]any) []map[string]any {
	t.Helper()
	raw, ok := obj["messages"].([]any)
	if !ok {
		t.Fatalf("messages type = %T, want []any", obj["messages"])
	}
	messages := make([]map[string]any, 0, len(raw))
	for i, value := range raw {
		message, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("messages[%d] type = %T, want map[string]any", i, value)
		}
		messages = append(messages, message)
	}
	return messages
}

func TestNormalizeWorkBuddyPayloadStreamAndOptions(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantOptions any
	}{
		{name: "unset stream options", body: `{"stream":false}`, wantOptions: map[string]any{"include_usage": true}},
		{name: "explicit stream options preserved", body: `{"stream":false,"stream_options":{"include_usage":false,"custom":"kept"}}`, wantOptions: map[string]any{"include_usage": false, "custom": "kept"}},
		{name: "explicit null stream options preserved", body: `{"stream_options":null}`, wantOptions: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj, changed := normalizeWorkBuddyForTest(t, tt.body, nil, "")
			if obj["stream"] != true {
				t.Fatalf("stream = %#v, want true", obj["stream"])
			}
			if !reflect.DeepEqual(obj["stream_options"], tt.wantOptions) {
				t.Fatalf("stream_options = %#v, want %#v", obj["stream_options"], tt.wantOptions)
			}
			if !changed {
				t.Fatal("changed = false, want true")
			}
		})
	}

	input := []byte(`{"stream":true,"stream_options":{"include_usage":false}}`)
	out, changed := NormalizeWorkBuddyPayload(input, nil, "")
	if changed {
		t.Fatal("already-normalized payload reported changed")
	}
	if !bytes.Equal(out, input) {
		t.Fatalf("unchanged payload = %s, want exact input %s", out, input)
	}
}

func TestNormalizeWorkBuddyPayloadMaxCompletionTokens(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantMax       any
		wantMaxExists bool
	}{
		{name: "positive integer translated", body: `{"max_completion_tokens":128}`, wantMax: float64(128), wantMaxExists: true},
		{name: "fractional dropped", body: `{"max_completion_tokens":1.5}`},
		{name: "zero dropped", body: `{"max_completion_tokens":0}`},
		{name: "negative dropped", body: `{"max_completion_tokens":-1}`},
		{name: "null dropped", body: `{"max_completion_tokens":null}`},
		{name: "non numeric dropped", body: `{"max_completion_tokens":"128"}`},
		{name: "explicit max tokens wins", body: `{"max_tokens":64,"max_completion_tokens":128}`, wantMax: float64(64), wantMaxExists: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj, _ := normalizeWorkBuddyForTest(t, tt.body, nil, "")
			if _, exists := obj["max_completion_tokens"]; exists {
				t.Fatal("max_completion_tokens was not removed")
			}
			got, exists := obj["max_tokens"]
			if exists != tt.wantMaxExists || (exists && got != tt.wantMax) {
				t.Fatalf("max_tokens = %#v, exists=%v; want %#v, exists=%v", got, exists, tt.wantMax, tt.wantMaxExists)
			}
		})
	}

	for _, value := range []any{int(32), int64(48)} {
		obj := map[string]any{"max_completion_tokens": value}
		if !translateWorkBuddyMaxCompletionTokens(obj) {
			t.Fatalf("hand-built %T alias did not report a change", value)
		}
		if _, exists := obj["max_completion_tokens"]; exists {
			t.Fatalf("hand-built %T alias was not removed", value)
		}
		if got := obj["max_tokens"]; got != int64(reflect.ValueOf(value).Int()) {
			t.Fatalf("hand-built %T max_tokens = %#v", value, got)
		}
	}
}

func TestNormalizeWorkBuddyPayloadToolChoice(t *testing.T) {
	tests := []struct {
		name          string
		toolChoice    string
		wantChoice    any
		wantChoiceSet bool
		wantTools     bool
		wantFunctions bool
	}{
		{name: "string none", toolChoice: `"none"`},
		{name: "object none", toolChoice: `{"type":"none"}`},
		{name: "auto object", toolChoice: `{"type":"auto"}`, wantChoice: "auto", wantChoiceSet: true, wantTools: true, wantFunctions: true},
		{name: "required object", toolChoice: `{"type":"required"}`, wantChoice: "required", wantChoiceSet: true, wantTools: true, wantFunctions: true},
		{name: "nested function name", toolChoice: `{"type":"function","function":{"name":"lookup"}}`, wantChoice: "lookup", wantChoiceSet: true, wantTools: true, wantFunctions: true},
		{name: "top level function name", toolChoice: `{"type":"function","name":"lookup_top"}`, wantChoice: "lookup_top", wantChoiceSet: true, wantTools: true, wantFunctions: true},
		{name: "empty function name", toolChoice: `{"type":"function","function":{"name":" "}}`, wantChoice: "auto", wantChoiceSet: true, wantTools: true, wantFunctions: true},
		{name: "unknown object", toolChoice: `{"type":"parallel"}`, wantTools: true, wantFunctions: true},
		{name: "non scalar array", toolChoice: `["auto"]`, wantTools: true, wantFunctions: true},
		{name: "non scalar null", toolChoice: `null`, wantTools: true, wantFunctions: true},
		{name: "ordinary string preserved", toolChoice: `"required"`, wantChoice: "required", wantChoiceSet: true, wantTools: true, wantFunctions: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"tool_choice":` + tt.toolChoice + `,"tools":[{"type":"function"}],"functions":[{"name":"legacy"}]}`
			obj, _ := normalizeWorkBuddyForTest(t, body, nil, "")
			choice, choiceSet := obj["tool_choice"]
			if choiceSet != tt.wantChoiceSet || (choiceSet && choice != tt.wantChoice) {
				t.Fatalf("tool_choice = %#v, set=%v; want %#v, set=%v", choice, choiceSet, tt.wantChoice, tt.wantChoiceSet)
			}
			_, toolsSet := obj["tools"]
			_, functionsSet := obj["functions"]
			if toolsSet != tt.wantTools || functionsSet != tt.wantFunctions {
				t.Fatalf("tools/functions set = %v/%v, want %v/%v", toolsSet, functionsSet, tt.wantTools, tt.wantFunctions)
			}
		})
	}
}

func TestNormalizeWorkBuddyPayloadDeveloperRole(t *testing.T) {
	obj, _ := normalizeWorkBuddyForTest(t, `{"messages":[
		{"role":"developer","content":"a"},
		{"role":" Developer ","content":"b"},
		{"role":"system","content":"c"},
		{"role":"user","content":"d"},
		{"role":"assistant","content":"e"},
		{"role":"tool","content":"f"},
		{"role":"unknown","content":"g"}]}`, nil, "")
	messages := workBuddyMessages(t, obj)
	want := []string{"system", "system", "system", "user", "assistant", "tool", "unknown"}
	for i, role := range want {
		if messages[i]["role"] != role {
			t.Errorf("messages[%d].role = %#v, want %q", i, messages[i]["role"], role)
		}
	}
}

func TestNormalizeWorkBuddyPayloadRepacksInterleavedToolResults(t *testing.T) {
	obj, _ := normalizeWorkBuddyForTest(t, `{"messages":[
		{"role":"assistant","tool_calls":[{"id":"c1"},{"id":"c2"}]},
		{"role":"tool","tool_call_id":"c1","content":"r1"},
		{"role":"developer","content":"notice"},
		{"role":"tool","tool_call_id":"c2","content":"r2"},
		{"role":"user","content":"next"}]}`, nil, "")
	messages := workBuddyMessages(t, obj)
	wantRoles := []string{"assistant", "tool", "tool", "system", "user"}
	for i, role := range wantRoles {
		if messages[i]["role"] != role {
			t.Fatalf("messages[%d].role = %#v, want %q", i, messages[i]["role"], role)
		}
	}
	if messages[1]["tool_call_id"] != "c1" || messages[2]["tool_call_id"] != "c2" {
		t.Fatalf("tool result order = %#v, %#v", messages[1]["tool_call_id"], messages[2]["tool_call_id"])
	}
	if messages[3]["content"] != "notice" {
		t.Fatalf("interleaved message content = %#v", messages[3]["content"])
	}
}

func TestWorkBuddyToolPairingNoOpReturnsOriginalSlice(t *testing.T) {
	messages := []any{
		map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "c1"}}},
		map[string]any{"role": "tool", "tool_call_id": "c1"},
	}
	repacked, repackedChanged := repackWorkBuddyToolResultBlocks(messages)
	if repackedChanged || &repacked[0] != &messages[0] {
		t.Fatal("contiguous tool results did not return the original slice")
	}
	cleaned, cleanedChanged := cleanupWorkBuddyOrphanToolCalls(messages)
	if cleanedChanged || &cleaned[0] != &messages[0] {
		t.Fatal("complete tool pairs did not return the original slice")
	}
}

func TestNormalizeWorkBuddyPayloadToolPairCleanup(t *testing.T) {
	tests := []struct {
		name             string
		body             string
		wantMessageRoles []string
		wantCallIDs      []string
		wantResultIDs    []string
	}{
		{
			name:             "orphan call removed",
			body:             `{"messages":[{"role":"assistant","tool_calls":[{"id":"c1"}]},{"role":"user","content":"next"}]}`,
			wantMessageRoles: []string{"assistant", "user"},
		},
		{
			name:             "orphan result removed",
			body:             `{"messages":[{"role":"user","content":"u"},{"role":"tool","tool_call_id":"c1","content":"r"}]}`,
			wantMessageRoles: []string{"user"},
		},
		{
			name:             "asymmetric pair trimmed symmetrically",
			body:             `{"messages":[{"role":"assistant","tool_calls":[{"id":"c1"},{"id":"c2"}]},{"role":"tool","tool_call_id":"c1","content":"r1"}]}`,
			wantMessageRoles: []string{"assistant", "tool"}, wantCallIDs: []string{"c1"}, wantResultIDs: []string{"c1"},
		},
		{
			name:             "complete pair kept",
			body:             `{"messages":[{"role":"assistant","tool_calls":[{"id":"c1"},{"id":"c2"}]},{"role":"tool","tool_call_id":"c1"},{"role":"tool","tool_call_id":"c2"}]}`,
			wantMessageRoles: []string{"assistant", "tool", "tool"}, wantCallIDs: []string{"c1", "c2"}, wantResultIDs: []string{"c1", "c2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj, _ := normalizeWorkBuddyForTest(t, tt.body, nil, "")
			messages := workBuddyMessages(t, obj)
			roles := make([]string, 0, len(messages))
			var callIDs, resultIDs []string
			for _, message := range messages {
				role, _ := message["role"].(string)
				roles = append(roles, role)
				if role == "assistant" {
					calls, _ := message["tool_calls"].([]any)
					for _, rawCall := range calls {
						call, _ := rawCall.(map[string]any)
						if id, _ := call["id"].(string); id != "" {
							callIDs = append(callIDs, id)
						}
					}
				}
				if role == "tool" {
					if id, _ := message["tool_call_id"].(string); id != "" {
						resultIDs = append(resultIDs, id)
					}
				}
			}
			if !reflect.DeepEqual(roles, tt.wantMessageRoles) {
				t.Fatalf("roles = %#v, want %#v", roles, tt.wantMessageRoles)
			}
			if !reflect.DeepEqual(callIDs, tt.wantCallIDs) {
				t.Fatalf("call IDs = %#v, want %#v", callIDs, tt.wantCallIDs)
			}
			if !reflect.DeepEqual(resultIDs, tt.wantResultIDs) {
				t.Fatalf("result IDs = %#v, want %#v", resultIDs, tt.wantResultIDs)
			}
		})
	}
}

func TestNormalizeWorkBuddyPayloadDeepSeekThinking(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		defaultEffort string
		wantType      string
		wantEffort    string
		wantEffortSet bool
	}{
		{name: "absent effort absent", body: `{"model":"deepseek-v4"}`, wantType: "enabled", wantEffort: "high", wantEffortSet: true},
		{name: "absent effort present", body: `{"model":"deepseek-v4","reasoning_effort":"low"}`, wantType: "enabled", wantEffort: "low", wantEffortSet: true},
		{name: "enabled effort absent", body: `{"model":"DEEPSEEK-v4","thinking":{"type":"enabled"}}`, wantType: "enabled", wantEffort: "high", wantEffortSet: true},
		{name: "enabled effort present", body: `{"model":"deepseek-v4","thinking":{"type":"enabled"},"reasoning_effort":"medium"}`, wantType: "enabled", wantEffort: "medium", wantEffortSet: true},
		{name: "enabled catalog default", body: `{"model":"deepseek-v4","thinking":{"type":"enabled"}}`, defaultEffort: "medium", wantType: "enabled", wantEffort: "medium", wantEffortSet: true},
		{name: "disabled effort absent", body: `{"model":"deepseek-v4","thinking":{"type":"disabled"}}`, wantType: "disabled"},
		{name: "disabled effort present", body: `{"model":"deepseek-v4","thinking":{"type":"disabled"},"reasoning_effort":"high","reasoningEffort":"low"}`, wantType: "disabled"},
		{name: "empty type injected", body: `{"model":"deepseek-v4","thinking":{"type":""}}`, wantType: "enabled", wantEffort: "high", wantEffortSet: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj, _ := normalizeWorkBuddyForTest(t, tt.body, nil, tt.defaultEffort)
			thinking, ok := obj["thinking"].(map[string]any)
			if !ok || thinking["type"] != tt.wantType {
				t.Fatalf("thinking = %#v, want type %q", obj["thinking"], tt.wantType)
			}
			effort, effortSet := obj["reasoning_effort"]
			if effortSet != tt.wantEffortSet || (effortSet && effort != tt.wantEffort) {
				t.Fatalf("reasoning_effort = %#v, set=%v; want %q, set=%v", effort, effortSet, tt.wantEffort, tt.wantEffortSet)
			}
			if tt.wantType == "disabled" {
				if _, exists := obj["reasoningEffort"]; exists {
					t.Fatal("reasoningEffort was not removed for disabled thinking")
				}
			}
		})
	}
}

func TestNormalizeWorkBuddyPayloadNonDeepSeekThinkingUntouched(t *testing.T) {
	obj, _ := normalizeWorkBuddyForTest(t, `{"model":"glm-5.2","thinking":{"type":"disabled"},"reasoning_effort":"high","messages":[{"role":"assistant","content":"a","reasoning":"trace"}]}`, nil, "medium")
	thinking, _ := obj["thinking"].(map[string]any)
	if thinking["type"] != "disabled" || obj["reasoning_effort"] != "high" {
		t.Fatalf("non-deepseek thinking fields changed: %#v", obj)
	}
	message := workBuddyMessages(t, obj)[0]
	if _, exists := message["reasoning_content"]; exists {
		t.Fatalf("non-deepseek reasoning_content added: %#v", message)
	}
}

func TestNormalizeWorkBuddyPayloadReasoningEffort(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		efforts []string
		wantKey string
		want    any
	}{
		{name: "downgrade", body: `{"model":"gpt-test","reasoning_effort":"xhigh"}`, efforts: []string{"low", "medium", "high"}, wantKey: "reasoning_effort", want: "high"},
		{name: "floor to lowest", body: `{"model":"gpt-test","reasoning_effort":"minimal"}`, efforts: []string{"low", "high"}, wantKey: "reasoning_effort", want: "low"},
		{name: "camel case downgrade", body: `{"model":"gpt-test","reasoningEffort":"max"}`, efforts: []string{"medium", "xhigh"}, wantKey: "reasoningEffort", want: "xhigh"},
		{name: "supported unchanged", body: `{"model":"gpt-test","reasoning_effort":"medium"}`, efforts: []string{"low", "medium", "high"}, wantKey: "reasoning_effort", want: "medium"},
		{name: "unknown level passthrough", body: `{"model":"gpt-test","reasoning_effort":"turbo"}`, efforts: []string{"low", "high"}, wantKey: "reasoning_effort", want: "turbo"},
		{name: "unknown catalog passthrough", body: `{"model":"gpt-test","reasoning_effort":"max"}`, wantKey: "reasoning_effort", want: "max"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj, _ := normalizeWorkBuddyForTest(t, tt.body, tt.efforts, "")
			if got := obj[tt.wantKey]; got != tt.want {
				t.Fatalf("%s = %#v, want %#v", tt.wantKey, got, tt.want)
			}
		})
	}

	obj, _ := normalizeWorkBuddyForTest(t, `{"model":"gpt-test"}`, []string{"low"}, "")
	if _, exists := obj["reasoning_effort"]; exists {
		t.Fatal("absent reasoning_effort was added")
	}
}

func TestNormalizeWorkBuddyPayloadReasoningContentBackfill(t *testing.T) {
	tests := []struct {
		name             string
		message          string
		thinking         string
		want             string
		wantSet          bool
		wantReasoning    string
		wantReasoningSet bool
	}{
		{name: "string kept", message: `{"role":"assistant","reasoning":"other","reasoning_content":"kept"}`, want: "kept", wantSet: true, wantReasoning: "other", wantReasoningSet: true},
		{name: "reasoning content mirrored", message: `{"role":"assistant","reasoning_content":"trace"}`, want: "trace", wantSet: true, wantReasoning: "trace", wantReasoningSet: true},
		{name: "reasoning copied", message: `{"role":"assistant","reasoning":"trace"}`, want: "trace", wantSet: true, wantReasoning: "trace", wantReasoningSet: true},
		{name: "empty added", message: `{"role":"assistant","content":"plain"}`, want: "", wantSet: true, wantReasoning: " ", wantReasoningSet: true},
		{name: "null replaced", message: `{"role":"assistant","reasoning_content":null}`, want: "", wantSet: true, wantReasoning: " ", wantReasoningSet: true},
		{name: "number replaced", message: `{"role":"assistant","reasoning_content":7}`, want: "", wantSet: true, wantReasoning: " ", wantReasoningSet: true},
		{name: "disabled without trace untouched", message: `{"role":"assistant","content":"plain"}`, thinking: `,"thinking":{"type":"disabled"}`},
		{name: "disabled with trace backfilled", message: `{"role":"assistant","reasoning":"trace"}`, thinking: `,"thinking":{"type":"disabled"}`, want: "trace", wantSet: true, wantReasoning: "trace", wantReasoningSet: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"model":"deepseek-v4"` + tt.thinking + `,"messages":[` + tt.message + `]}`
			obj, _ := normalizeWorkBuddyForTest(t, body, nil, "")
			message := workBuddyMessages(t, obj)[0]
			got, set := message["reasoning_content"]
			if set != tt.wantSet || (set && got != tt.want) {
				t.Fatalf("reasoning_content = %#v, set=%v; want %q, set=%v", got, set, tt.want, tt.wantSet)
			}
			reasoning, reasoningSet := message["reasoning"]
			if reasoningSet != tt.wantReasoningSet || (reasoningSet && reasoning != tt.wantReasoning) {
				t.Fatalf("reasoning = %#v, set=%v; want %q, set=%v", reasoning, reasoningSet, tt.wantReasoning, tt.wantReasoningSet)
			}
		})
	}
}

func TestNormalizeWorkBuddyPayloadInvalidJSONUnchanged(t *testing.T) {
	input := []byte(`{"model":`)
	out, changed := NormalizeWorkBuddyPayload(input, []string{"low"}, "high")
	if changed || !bytes.Equal(out, input) {
		t.Fatalf("invalid JSON normalized to %q changed=%v, want exact input and false", out, changed)
	}
}
