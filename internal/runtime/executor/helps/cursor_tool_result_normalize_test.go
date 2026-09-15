package helps

import (
	"strings"
	"testing"
)

func TestIsCursorComputerUseTool(t *testing.T) {
	cases := []struct {
		name      string
		toolName  string
		namespace string
		want      bool
	}{
		{"empty", "", "", false},
		{"bare node_repl", "node_repl", "", true},
		{"bare computer_use", "computer_use", "", true},
		{"case insensitive", "Node_Repl", "", true},
		{"mcp namespace", "", "mcp__node_repl", true},
		{"mcp tool name", "mcp__node_repl__js", "", true},
		{"unrelated tool", "list_files", "", false},
		{"unrelated namespace", "", "mcp__filesystem", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsCursorComputerUseTool(tc.toolName, tc.namespace); got != tc.want {
				t.Fatalf("tool=%q ns=%q want=%v got=%v", tc.toolName, tc.namespace, tc.want, got)
			}
		})
	}
}

func TestIsCursorEmptyOrFailedExecOutput(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", true},
		{"   ", true},
		{"\n\n", true},
		{"hello", false},
		{"Script failed", true},
		{"  script failed: nope  ", true},
		{"Script completed successfully", false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := IsCursorEmptyOrFailedExecOutput(c.in); got != c.want {
				t.Fatalf("in=%q want=%v got=%v", c.in, c.want, got)
			}
		})
	}
}

func TestNormalizeCursorToolResultText_EmptyComputerUse(t *testing.T) {
	got := NormalizeCursorToolResultText("", CursorToolResultOptions{
		ToolName: "node_repl",
		IsError:  false,
	})
	if !got.Changed {
		t.Fatalf("empty Computer Use output must change result, got %+v", got)
	}
	if !got.IsError {
		t.Fatalf("empty Computer Use output must be marked IsError=true, got %+v", got)
	}
	if got.Text != cursorEmptyExecOutputMessage {
		t.Fatalf("text mismatch: %q", got.Text)
	}
}

func TestNormalizeCursorToolResultText_RuntimeFailureGuidance(t *testing.T) {
	got := NormalizeCursorToolResultText("Error: SkyComputerUseError happened", CursorToolResultOptions{
		ToolName: "computer_use",
		IsError:  false,
	})
	if !got.Changed {
		t.Fatalf("marker match must change result")
	}
	if !got.IsError {
		t.Fatalf("marker match must mark IsError=true")
	}
	if !strings.Contains(got.Text, "[recovery: The Computer Use runtime rejected this action.") {
		t.Fatalf("expected recovery hint, got %q", got.Text)
	}
}

func TestNormalizeCursorToolResultText_SuccessWrapperPassThrough(t *testing.T) {
	got := NormalizeCursorToolResultText("Script completed, output=42", CursorToolResultOptions{
		ToolName: "node_repl",
		IsError:  false,
	})
	if got.Changed {
		t.Fatalf("success wrapper must pass through, got %+v", got)
	}
	if got.IsError {
		t.Fatalf("success wrapper must not be marked as error")
	}
}

func TestNormalizeCursorToolResultText_NonComputerUsePassThrough(t *testing.T) {
	got := NormalizeCursorToolResultText("SkyComputerUseError in result", CursorToolResultOptions{
		ToolName: "list_files",
		IsError:  false,
	})
	if got.Changed {
		t.Fatalf("non-Computer Use tool must pass through unchanged, got %+v", got)
	}
}
