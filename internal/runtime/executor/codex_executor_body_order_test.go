package executor

import (
	"testing"
)

// TestOrderCodexBodyMatchesReferenceKeyOrder asserts the exact serialized byte
// output matches cortexkit/openai-auth orderCodexBody (CODEX_BODY_KEY_ORDER):
// type, model, instructions, previous_response_id, input, tools, tool_choice,
// parallel_tool_calls, reasoning, store, stream, include, prompt_cache_key,
// text, generate, client_metadata — unknown keys appended in original order.
func TestOrderCodexBodyMatchesReferenceKeyOrder(t *testing.T) {
	// Keys deliberately scrambled relative to the canonical order.
	input := []byte(`{"client_metadata":{"x-codex-installation-id":"iid"},"stream":true,"prompt_cache_key":"cache-1","model":"gpt-5.4","reasoning":{"effort":"high"},"input":[{"type":"message","role":"user","content":"hi"}],"instructions":"be helpful","tools":[{"type":"function","name":"f"}],"parallel_tool_calls":true,"store":false,"tool_choice":"auto","unknown_extra":1}`)

	want := `{"model":"gpt-5.4","instructions":"be helpful","input":[{"type":"message","role":"user","content":"hi"}],"tools":[{"type":"function","name":"f"}],"tool_choice":"auto","parallel_tool_calls":true,"reasoning":{"effort":"high"},"store":false,"stream":true,"prompt_cache_key":"cache-1","client_metadata":{"x-codex-installation-id":"iid"},"unknown_extra":1}`

	got := orderCodexBody(input)
	if string(got) != want {
		t.Fatalf("orderCodexBody mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestOrderCodexBodyWebsocketTypeFirst(t *testing.T) {
	input := []byte(`{"model":"gpt-5.4","input":[],"type":"response.create"}`)
	want := `{"type":"response.create","model":"gpt-5.4","input":[]}`
	got := orderCodexBody(input)
	if string(got) != want {
		t.Fatalf("orderCodexBody mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestOrderCodexBodyPassthroughInvalid(t *testing.T) {
	for _, in := range []string{"", "[1,2]", "not json"} {
		got := orderCodexBody([]byte(in))
		if string(got) != in {
			t.Fatalf("orderCodexBody(%q) = %q, want passthrough", in, got)
		}
	}
}

func TestBuildCodexWebsocketRequestBodyOrdersKeys(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":[],"stream":true}`)
	got := buildCodexWebsocketRequestBody(body)
	want := `{"type":"response.create","model":"gpt-5.4","input":[],"stream":true}`
	if string(got) != want {
		t.Fatalf("buildCodexWebsocketRequestBody mismatch:\n got: %s\nwant: %s", got, want)
	}
}
