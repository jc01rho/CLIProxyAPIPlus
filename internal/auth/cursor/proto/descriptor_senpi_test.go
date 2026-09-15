package proto

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestSenpiDescriptorHasToolCallAndMcpHistoryMessages(t *testing.T) {
	file := AgentFileDescriptor()

	toolCall := file.Messages().ByName("ToolCall")
	if toolCall == nil {
		t.Fatal("ToolCall message missing from senpi agent descriptor")
	}
	idField := toolCall.Fields().ByNumber(57)
	if idField == nil || idField.Name() != "tool_call_id" {
		t.Fatalf("ToolCall field 57 = %v, want tool_call_id (senpi modern exec envelope)", idField)
	}

	step := file.Messages().ByName("ConversationStep")
	if step == nil {
		t.Fatal("ConversationStep message missing")
	}
	if step.Fields().ByName("tool_call") == nil {
		t.Fatal("ConversationStep.tool_call missing; typed MCP history cannot encode")
	}

	mcpCall := file.Messages().ByName("McpToolCall")
	if mcpCall == nil {
		t.Fatal("McpToolCall message missing")
	}
	for _, name := range []protoreflect.Name{"args", "result"} {
		if mcpCall.Fields().ByName(name) == nil {
			t.Fatalf("McpToolCall.%s missing", name)
		}
	}

	item := file.Messages().ByName("McpToolResultContentItem")
	if item == nil {
		t.Fatal("McpToolResultContentItem missing")
	}
	if item.Fields().ByName("image") == nil {
		t.Fatal("McpToolResultContentItem.image missing; MCP image parts cannot encode")
	}
}
