package executor

import (
	"encoding/json"
	"testing"
)

// commandCodeGenericEnvelope mirrors the wire envelope but keeps message
// content as raw JSON so we can inspect converted blocks without the
// commandCodeWireContentBlock interface (which has no JSON decoder).
type commandCodeGenericEnvelope struct {
	Params struct {
		Messages []struct {
			Role    string            `json:"role"`
			Content []json.RawMessage `json:"content"`
		} `json:"messages"`
	} `json:"params"`
}

// collectCommandCodeWireBlocks walks the converted envelope and returns every
// tool-call/tool-result ToolCallID present in the wire messages.
func collectCommandCodeWireBlocks(t *testing.T, out []byte) (toolCalls []string, toolResults []string) {
	t.Helper()
	var env commandCodeGenericEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("failed to unmarshal wire envelope: %v", err)
	}
	for _, m := range env.Params.Messages {
		for _, raw := range m.Content {
			var hdr struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(raw, &hdr); err != nil {
				continue
			}
			switch hdr.Type {
			case "tool-call":
				var b commandCodeWireToolCallBlock
				if err := json.Unmarshal(raw, &b); err == nil {
					toolCalls = append(toolCalls, b.ToolCallID)
				}
			case "tool-result":
				var b commandCodeWireToolResultBlock
				if err := json.Unmarshal(raw, &b); err == nil {
					toolResults = append(toolResults, b.ToolCallID)
				}
			}
		}
	}
	return toolCalls, toolResults
}

// TestCommandCodeSkipsFullyEmptyAssistantMessage verifies that an assistant
// message whose only tool calls all have empty names (and no text content) is
// dropped entirely instead of being sent upstream as an empty-content message.
func TestCommandCodeSkipsFullyEmptyAssistantMessage(t *testing.T) {
	emptyID := "tool_773451ce-636e-4397-9c2d-a3a297c5300"

	payload := []byte(`{
		"model": "higher-coding",
		"messages": [
			{"role":"user","content":"hi"},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"` + emptyID + `","type":"function","function":{"name":"","arguments":"{}"}}
			]},
			{"role":"tool","tool_call_id":"` + emptyID + `","content":"orphan result"},
			{"role":"assistant","content":"final answer"}
		]
	}`)

	out, err := buildCommandCodePayload(payload, "higher-coding", false)
	if err != nil {
		t.Fatalf("buildCommandCodePayload returned error: %v", err)
	}

	var env commandCodeGenericEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("failed to unmarshal wire envelope: %v", err)
	}

	var roles []string
	for _, m := range env.Params.Messages {
		roles = append(roles, m.Role)
		if len(m.Content) == 0 && m.Role == "assistant" {
			t.Errorf("assistant message with empty content reached the wire payload")
		}
	}

	want := []string{"user", "assistant"}
	if len(roles) != len(want) {
		t.Fatalf("wire message roles = %v, want %v", roles, want)
	}
	for i := range want {
		if roles[i] != want[i] {
			t.Errorf("wire message roles[%d] = %q, want %q", i, roles[i], want[i])
		}
	}
}

// TestCommandCodeSkipsEmptyNameToolCallAndOrphanResult verifies that an
// assistant tool call with an empty function name is dropped, and its matching
// tool-result message is dropped too so the upstream never sees an orphaned
// result ("No function call found for function call output with call_id ...").
func TestCommandCodeSkipsEmptyNameToolCallAndOrphanResult(t *testing.T) {
	emptyID := "tool_773451ce-636e-4397-9c2d-a3a297c5300"
	goodID := "tool_good"

	payload := []byte(`{
		"model": "higher-coding",
		"messages": [
			{"role":"system","content":"sys"},
			{"role":"user","content":"hi"},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"` + goodID + `","type":"function","function":{"name":"good_tool","arguments":"{}"}},
				{"id":"` + emptyID + `","type":"function","function":{"name":"","arguments":"{}"}}
			]},
			{"role":"tool","tool_call_id":"` + goodID + `","content":"result good"},
			{"role":"tool","tool_call_id":"` + emptyID + `","content":"orphan result"},
			{"role":"assistant","content":"final answer"}
		],
		"tools":[{"type":"function","function":{"name":"good_tool","description":"d","parameters":{}}}]
	}`)

	out, err := buildCommandCodePayload(payload, "higher-coding", false)
	if err != nil {
		t.Fatalf("buildCommandCodePayload returned error: %v", err)
	}

	env := commandCodeGenericEnvelope{}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("failed to unmarshal wire envelope: %v", err)
	}

	// The system message is mapped to top-level params.system, so the message array
	// holds: user, assistant(with tool_good call), tool(good result), assistant(final) = 4.
	if len(env.Params.Messages) != 4 {
		t.Fatalf("expected 4 wire messages (orphan tool message dropped), got %d", len(env.Params.Messages))
	}

	toolCalls, toolResults := collectCommandCodeWireBlocks(t, out)

	// The good tool call is preserved.
	if !contains(toolCalls, goodID) {
		t.Fatalf("expected tool call %q to be preserved, got %v", goodID, toolCalls)
	}
	// The empty-name tool call is dropped.
	if contains(toolCalls, emptyID) {
		t.Fatalf("empty-name tool call %q must be dropped, got %v", emptyID, toolCalls)
	}
	// The good tool result is preserved.
	if !contains(toolResults, goodID) {
		t.Fatalf("expected tool result %q to be preserved, got %v", goodID, toolResults)
	}
	// The orphan tool result (referencing the skipped empty-name call) is dropped.
	if contains(toolResults, emptyID) {
		t.Fatalf("orphan tool result %q must be dropped, got %v", emptyID, toolResults)
	}
}

// TestCommandCodeSkipsEmptyNameToolCallNoOrphan verifies a single assistant
// tool call with an empty name is silently dropped without an error.
func TestCommandCodeSkipsEmptyNameToolCallNoOrphan(t *testing.T) {
	emptyID := "tool_empty"
	payload := []byte(`{
		"model": "higher-coding",
		"messages": [
			{"role":"user","content":"hi"},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"` + emptyID + `","type":"function","function":{"name":"","arguments":"{}"}}
			]}
		]
	}`)

	out, err := buildCommandCodePayload(payload, "higher-coding", false)
	if err != nil {
		t.Fatalf("buildCommandCodePayload returned error: %v", err)
	}

	toolCalls, _ := collectCommandCodeWireBlocks(t, out)
	if contains(toolCalls, emptyID) {
		t.Fatalf("empty-name tool call %q must be dropped, got %v", emptyID, toolCalls)
	}
}

func contains(xs []string, target string) bool {
	for _, x := range xs {
		if x == target {
			return true
		}
	}
	return false
}
