package helps

import "testing"

func TestCursorProxyOwnedBareToolNameSet(t *testing.T) {
	wants := []string{
		"exec", "wait", "exec_command", "shell_command",
		"apply_patch", "edit_file", "multi_edit", "tool_search",
	}
	if len(CursorProxyOwnedBareToolNameSet) != len(wants) {
		t.Fatalf("owned set size mismatch: want %d got %d", len(wants), len(CursorProxyOwnedBareToolNameSet))
	}
	for _, n := range wants {
		if _, ok := CursorProxyOwnedBareToolNameSet[n]; !ok {
			t.Fatalf("expected %q in proxy-owned set", n)
		}
	}
}

func TestIsCursorProxyOwnedBareToolName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"exec_command", "exec_command", true},
		{"apply_patch", "apply_patch", true},
		{"random_bare", "list_files", false},
		{"mcp_aliased", "mcp__foo__bar", false}, // namespaced -> false
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsCursorProxyOwnedBareToolName(tc.in); got != tc.want {
				t.Fatalf("name=%q want=%v got=%v", tc.in, tc.want, got)
			}
		})
	}
}

func TestWireNameForCursorClientTool(t *testing.T) {
	cases := []struct {
		name      string
		namespace string
		tool      string
		want      string
	}{
		{"namespaced passthrough", "mcp__node_repl", "exec", "exec"},
		{"proxy-owned passthrough", "", "exec_command", "exec_command"},
		{"client bare gets prefix", "", "list_files", "ocx_client_list_files"},
		{"empty tool", "", "", ""},
		{"empty tool namespaced", "ns", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WireNameForCursorClientTool(tc.namespace, tc.tool); got != tc.want {
				t.Fatalf("ns=%q tool=%q want=%q got=%q", tc.namespace, tc.tool, tc.want, got)
			}
		})
	}
}

func TestSplitPipeList(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single", "exec", []string{"exec"}},
		{"trailing pipe", "exec|", []string{"exec"}},
		{"leading pipe", "|exec", []string{"exec"}},
		{"double pipe", "exec||wait", []string{"exec", "wait"}},
		{"with whitespace", " exec | wait |apply_patch ", []string{"exec", "wait", "apply_patch"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitPipeList(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("length mismatch: want %v got %v", tc.want, got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("idx %d: want %q got %q", i, tc.want[i], got[i])
				}
			}
		})
	}
}
