package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestDropOrphanOpenAIToolResultsRemovesUnmatchedToolMessages(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role":"user","content":"hi"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_function_1000_1","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_function_1000_1","content":"24C"},
			{"role":"tool","tool_call_id":"call-orphan-1","content":"stale"},
			{"role":"assistant","content":"done"}
		]
	}`)

	got := dropOrphanOpenAIToolResults(body)
	toolCount := 0
	msgs := gjson.GetBytes(got, "messages").Array()
	if len(msgs) != 4 {
		t.Fatalf("messages len = %d, want 4:\n%s", len(msgs), got)
	}
	for _, msg := range msgs {
		if msg.Get("role").String() == "tool" {
			toolCount++
			if id := msg.Get("tool_call_id").String(); id != "call_function_1000_1" {
				t.Fatalf("unexpected surviving tool result id %q", id)
			}
		}
	}
	if toolCount != 1 {
		t.Fatalf("tool messages = %d, want 1", toolCount)
	}
}

func TestDropOrphanOpenAIToolResultsKeepsPairedResults(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role":"user","content":"hi"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call-a","type":"function","function":{"name":"f","arguments":"{}"}},{"id":"call-b","type":"function","function":{"name":"g","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call-b","content":"b"},
			{"role":"tool","tool_call_id":"call-a","content":"a"}
		]
	}`)

	got := dropOrphanOpenAIToolResults(body)
	if string(got) != string(body) {
		t.Fatalf("paired results must be preserved:\ngot:  %s\nwant: %s", got, body)
	}
}

func TestDropOrphanOpenAIToolResultsDropsResultsOfRenamedEmptyCalls(t *testing.T) {
	// sanitizeEmptyToolCallMessages drops tool calls whose function.name is
	// empty; the tool result left behind would become an orphan for strict
	// upstreams such as MiniMax and trigger error 2013.
	body := []byte(`{
		"messages": [
			{"role":"assistant","content":null,"tool_calls":[{"id":"call-empty","type":"function","function":{"name":"","arguments":"{}"}},{"id":"call-ok","type":"function","function":{"name":"f","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call-empty","content":"x"},
			{"role":"tool","tool_call_id":"call-ok","content":"y"}
		]
	}`)

	got := dropOrphanOpenAIToolResults(sanitizeEmptyToolFunctionNames(body))
	msgs := gjson.GetBytes(got, "messages").Array()
	if len(msgs) != 2 {
		t.Fatalf("messages len = %d, want 2:\n%s", len(msgs), got)
	}
	if msgs[1].Get("tool_call_id").String() != "call-ok" {
		t.Fatalf("surviving tool result = %s", msgs[1].Raw)
	}
}

func TestDropOrphanOpenAIToolResultsToleratesMissingToolCallsField(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":"no calls"},{"role":"tool","tool_call_id":"x","content":"orphan"}]}`)
	got := dropOrphanOpenAIToolResults(body)
	msgs := gjson.GetBytes(got, "messages").Array()
	if len(msgs) != 1 || msgs[0].Get("role").String() != "assistant" {
		t.Fatalf("unexpected messages:\n%s", got)
	}
}

func TestDropOrphanOpenAIToolResultsKeepsEmptyToolCallIDResults(t *testing.T) {
	// Some clients emit tool results without an id; dropping them silently
	// could hide client bugs, and upstreams tolerate the empty id.
	body := []byte(`{"messages":[{"role":"assistant","content":"no calls"},{"role":"tool","tool_call_id":"","content":"keep"}]}`)
	got := dropOrphanOpenAIToolResults(body)
	if string(got) != string(body) {
		t.Fatalf("empty tool_call_id must be preserved:\n%s", got)
	}
}
