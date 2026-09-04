package executor

import (
	"os"
	"runtime"
	"testing"

	"github.com/tidwall/gjson"
)

// commandCodeWireFixture is the shared OpenAI chat-completions payload that
// exercises every request wire element of the installed command-code@1.12.0
// contract: typed user content (text + image data URL), assistant text plus
// tool_calls with original IDs and JSON arguments, a tool role result that
// references the same ID, an OpenAI tools definition, and reasoning_effort
// (forwarded only when the resolved model documents the requested effort;
// the fixture model is not in the effort table, so it is omitted).
func commandCodeWireFixture() []byte {
	return []byte(`{
		"model": "parrot",
		"reasoning_effort": "high",
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "describe this image"},
				{"type": "image_url", "image_url": {"url": "data:image/png;base64,aGVsbG8="}}
			]},
			{"role": "assistant", "content": "Let me look.", "tool_calls": [
				{"id": "call_abc123", "type": "function", "function": {"name": "vision_lookup", "arguments": "{\"query\":\"sparrow\",\"limit\":3}"}}
			]},
			{"role": "tool", "tool_call_id": "call_abc123", "content": "found 3 results"}
		],
		"tools": [
			{"type": "function", "function": {"name": "vision_lookup", "description": "Look up reference images", "parameters": {"type": "object", "properties": {"query": {"type": "string"}, "limit": {"type": "integer"}}, "required": ["query"]}}}
		]
	}`)
}

// buildCommandCodeWireFixture runs the shared fixture through the payload
// builder with streaming enabled, mirroring the executor call sites.
func buildCommandCodeWireFixture(t *testing.T) []byte {
	t.Helper()
	got, err := buildCommandCodePayload(commandCodeWireFixture(), "nvidia/nemotron-3-ultra-550b-a55b", true)
	if err != nil {
		t.Fatalf("buildCommandCodePayload() error = %v", err)
	}
	return got
}

// commandCodeMessageByRole returns the first params.messages entry with the
// given role, failing the test when it is absent.
func commandCodeMessageByRole(t *testing.T, payload []byte, role string) gjson.Result {
	t.Helper()
	messages := gjson.GetBytes(payload, "params.messages")
	if !messages.IsArray() {
		t.Fatalf("params.messages is not a JSON array; raw=%s", messages.Raw)
	}
	for _, msg := range messages.Array() {
		if msg.Get("role").String() == role {
			return msg
		}
	}
	t.Fatalf("no params.messages entry with role %q; raw=%s", role, messages.Raw)
	return gjson.Result{}
}

// commandCodeBlockByType returns the first content block of the given type
// from a message whose content must be a typed JSON array.
func commandCodeBlockByType(t *testing.T, message gjson.Result, blockType string) gjson.Result {
	t.Helper()
	content := message.Get("content")
	if !content.IsArray() {
		t.Fatalf("message role=%q content is not a typed JSON array; raw=%s", message.Get("role").String(), content.Raw)
	}
	for _, block := range content.Array() {
		if block.Get("type").String() == blockType {
			return block
		}
	}
	t.Fatalf("no content block of type %q in role=%q message; raw=%s", blockType, message.Get("role").String(), content.Raw)
	return gjson.Result{}
}

func Test_CommandCodeRequest_user_message_keeps_typed_text_and_image_blocks(t *testing.T) {
	// Given the shared fixture whose user message mixes text with an image_url data URL.
	// When
	got := buildCommandCodeWireFixture(t)

	// Then the npm wire keeps a typed content array: a text block and an image
	// block carrying the data URL and its media type.
	user := commandCodeMessageByRole(t, got, "user")
	text := commandCodeBlockByType(t, user, "text")
	if got := text.Get("text").String(); got != "describe this image" {
		t.Fatalf("user text block = %q, want %q", got, "describe this image")
	}
	image := commandCodeBlockByType(t, user, "image")
	if got := image.Get("image").String(); got != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("user image block data URL = %q, want %q", got, "data:image/png;base64,aGVsbG8=")
	}
	if got := image.Get("mimeType").String(); got != "image/png" {
		t.Fatalf("user image block mimeType = %q, want %q", got, "image/png")
	}
}

func Test_CommandCodeRequest_assistant_tool_calls_keep_ids_and_json_input(t *testing.T) {
	// Given the shared fixture whose assistant message has text plus one tool call.
	// When
	got := buildCommandCodeWireFixture(t)

	// Then the npm wire emits a text block and a tool-call block that preserves
	// the original tool call ID and the arguments as a parsed JSON object.
	assistant := commandCodeMessageByRole(t, got, "assistant")
	text := commandCodeBlockByType(t, assistant, "text")
	if got := text.Get("text").String(); got != "Let me look." {
		t.Fatalf("assistant text block = %q, want %q", got, "Let me look.")
	}
	toolCall := commandCodeBlockByType(t, assistant, "tool-call")
	if got := toolCall.Get("toolCallId").String(); got != "call_abc123" {
		t.Fatalf("tool-call toolCallId = %q, want %q", got, "call_abc123")
	}
	if got := toolCall.Get("toolName").String(); got != "vision_lookup" {
		t.Fatalf("tool-call toolName = %q, want %q", got, "vision_lookup")
	}
	input := toolCall.Get("input")
	if !input.IsObject() {
		t.Fatalf("tool-call input is not a JSON object; raw=%s", input.Raw)
	}
	if got := input.Get("query").String(); got != "sparrow" {
		t.Fatalf("tool-call input.query = %q, want %q", got, "sparrow")
	}
	if got := input.Get("limit").Int(); got != 3 {
		t.Fatalf("tool-call input.limit = %d, want %d", got, 3)
	}
}

func Test_CommandCodeRequest_tool_result_message_references_original_tool_call_id(t *testing.T) {
	// Given the shared fixture whose tool role message answers call_abc123.
	// When
	got := buildCommandCodeWireFixture(t)

	// Then the npm wire keeps a tool role message with a tool-result block
	// referencing the same ID and the result text as typed output.
	toolMessage := commandCodeMessageByRole(t, got, "tool")
	toolResult := commandCodeBlockByType(t, toolMessage, "tool-result")
	if got := toolResult.Get("toolCallId").String(); got != "call_abc123" {
		t.Fatalf("tool-result toolCallId = %q, want %q", got, "call_abc123")
	}
	if got := toolResult.Get("output.type").String(); got != "text" {
		t.Fatalf("tool-result output.type = %q, want %q", got, "text")
	}
	if got := toolResult.Get("output.value").String(); got != "found 3 results" {
		t.Fatalf("tool-result output.value = %q, want %q", got, "found 3 results")
	}
}

func Test_CommandCodeRequest_tools_use_input_schema_without_type_function(t *testing.T) {
	// Given the shared fixture with one OpenAI function tool definition.
	// When
	got := buildCommandCodeWireFixture(t)

	// Then the npm wire tool is {name, description, input_schema} with no
	// OpenAI type:"function" marker and no nested function wrapper.
	tool := gjson.GetBytes(got, "params.tools.0")
	if !tool.Exists() {
		t.Fatalf("params.tools.0 missing; raw=%s", gjson.GetBytes(got, "params.tools").Raw)
	}
	if got := tool.Get("name").String(); got != "vision_lookup" {
		t.Fatalf("tools[0].name = %q, want %q", got, "vision_lookup")
	}
	if got := tool.Get("description").String(); got != "Look up reference images" {
		t.Fatalf("tools[0].description = %q, want %q", got, "Look up reference images")
	}
	if got := tool.Get("input_schema.properties.query.type").String(); got != "string" {
		t.Fatalf("tools[0].input_schema.properties.query.type = %q, want %q", got, "string")
	}
	if got := tool.Get("input_schema.required.0").String(); got != "query" {
		t.Fatalf("tools[0].input_schema.required[0] = %q, want %q", got, "query")
	}
	if tool.Get("type").Exists() {
		t.Fatalf("tools[0].type = %q, want no type field on the CommandCode wire", tool.Get("type").String())
	}
	if tool.Get("function").Exists() {
		t.Fatalf("tools[0].function present, want the function fields hoisted to the top level")
	}
}

func Test_CommandCodeRequest_envelope_memory_and_taste_are_json_null(t *testing.T) {
	// Given the shared fixture.
	// When
	got := buildCommandCodeWireFixture(t)

	// Then the npm envelope sends memory and taste as JSON null, not strings.
	for _, field := range []string{"memory", "taste"} {
		value := gjson.GetBytes(got, field)
		if !value.Exists() {
			t.Fatalf("envelope field %q missing; want explicit JSON null", field)
		}
		if value.Type != gjson.Null {
			t.Fatalf("envelope field %q = %s (type %v), want JSON null", field, value.Raw, value.Type)
		}
	}
}

func Test_CommandCodeRequest_envelope_carries_cli_run_mode(t *testing.T) {
	// Given the shared fixture.
	// When
	got := buildCommandCodeWireFixture(t)

	// Then the CLI run mode the official client sends is present; its absence
	// is one of the non-CLI fingerprints the upstream proxy detector keys on.
	if got := gjson.GetBytes(got, "mode").String(); got != "agent" {
		t.Fatalf("mode = %q, want %q", got, "agent")
	}
	if got := gjson.GetBytes(got, "permissionMode").String(); got != "standard" {
		t.Fatalf("permissionMode = %q, want %q", got, "standard")
	}
}

func Test_CommandCodeRequest_config_carries_workspace_metadata(t *testing.T) {
	// Given the shared fixture.
	// When
	got := buildCommandCodeWireFixture(t)

	// Then the config block mirrors CLI workspace metadata: a plausible
	// working directory and a platform environment, not /tmp+"terminal".
	if got := gjson.GetBytes(got, "config.workingDir").String(); got == "/tmp" || got == "" {
		t.Fatalf("config.workingDir = %q, want a CLI-plausible workspace path", got)
	}
	if got := gjson.GetBytes(got, "config.environment").String(); got == "terminal" || got == "" {
		t.Fatalf("config.environment = %q, want a platform value like \"linux\"", got)
	}
}

func Test_CommandCodeRequest_config_uses_real_workspace(t *testing.T) {
	// Given the shared fixture.
	// When
	got := buildCommandCodeWireFixture(t)

	// Then the config block carries live process state, not fixed placeholders:
	// os.Getwd() as workingDir, runtime.GOOS as environment.
	wantDir, err := os.Getwd()
	if err != nil {
		t.Skipf("os.Getwd() unavailable: %v", err)
	}
	if got := gjson.GetBytes(got, "config.workingDir").String(); got != wantDir {
		t.Fatalf("config.workingDir = %q, want process cwd %q", got, wantDir)
	}
	if got := gjson.GetBytes(got, "config.environment").String(); got != runtime.GOOS {
		t.Fatalf("config.environment = %q, want runtime.GOOS %q", got, runtime.GOOS)
	}
}

func Test_CommandCodeRequest_default_max_tokens_is_64000(t *testing.T) {
	// Given the shared fixture, which sets no max_tokens.
	// When
	got := buildCommandCodeWireFixture(t)

	// Then the npm default output token limit of 64000 is applied.
	if got := gjson.GetBytes(got, "params.max_tokens").Int(); got != 64000 {
		t.Fatalf("params.max_tokens = %d, want %d", got, 64000)
	}
}

func Test_CommandCodeRequest_stream_true_is_forwarded(t *testing.T) {
	// Given the shared fixture built with streaming enabled.
	// When
	got := buildCommandCodeWireFixture(t)

	// Then params.stream is true; the CommandCode endpoint is always streamed.
	if got := gjson.GetBytes(got, "params.stream").Bool(); !got {
		t.Fatalf("params.stream = %v, want true", got)
	}
}

func Test_CommandCodeRequest_reasoning_effort_forwarded_when_supported(t *testing.T) {
	// Given a payload with reasoning_effort and a model that documents it.
	payload := []byte(`{"model": "parrot", "reasoning_effort": "high", "messages": [{"role": "user", "content": "hello"}]}`)

	// When
	got, err := buildCommandCodePayload(payload, "deepseek/deepseek-v4-flash", true)

	// Then the supported effort is forwarded to the wire params.
	if err != nil {
		t.Fatalf("buildCommandCodePayload() error = %v", err)
	}
	if v := gjson.GetBytes(got, "params.reasoning_effort").String(); v != "high" {
		t.Fatalf("params.reasoning_effort = %q, want high", v)
	}
}

func Test_CommandCodeRequest_reasoning_effort_remapped_ultra_to_max(t *testing.T) {
	// Given a payload requesting ultra on a model whose profile aliases ultra→max.
	payload := []byte(`{"model": "parrot", "reasoning_effort": "ultra", "messages": [{"role": "user", "content": "hello"}]}`)

	// When
	got, err := buildCommandCodePayload(payload, "deepseek/deepseek-v4-flash", true)

	// Then ultra collapses to max on the wire.
	if err != nil {
		t.Fatalf("buildCommandCodePayload() error = %v", err)
	}
	if v := gjson.GetBytes(got, "params.reasoning_effort").String(); v != "max" {
		t.Fatalf("params.reasoning_effort = %q, want max (ultra aliased)", v)
	}
}

func Test_CommandCodeRequest_reasoning_effort_dropped_when_unsupported(t *testing.T) {
	// Given the shared fixture (reasoning_effort high) and a model with no
	// documented effort ladder.
	got := buildCommandCodeWireFixture(t)

	// Then reasoning_effort is never copied into the wire params for a model
	// that does not document the requested effort.
	if gjson.GetBytes(got, "params.reasoning_effort").Exists() {
		t.Fatalf("params.reasoning_effort present, want the key omitted for an unsupported model")
	}
}

func Test_CommandCodeRequest_reasoning_effort_omitted_when_absent(t *testing.T) {
	// Given a payload without reasoning_effort.
	payload := []byte(`{"model": "parrot", "messages": [{"role": "user", "content": "hello"}]}`)

	// When
	got, err := buildCommandCodePayload(payload, "nvidia/nemotron-3-ultra-550b-a55b", true)

	// Then the key is never present in the wire params.
	if err != nil {
		t.Fatalf("buildCommandCodePayload() error = %v", err)
	}
	if gjson.GetBytes(got, "params.reasoning_effort").Exists() {
		t.Fatalf("params.reasoning_effort present, want the key never forwarded to the wire")
	}
}
