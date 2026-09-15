package helps

import "testing"

func TestCursorNormalizeWireName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"ocx_client_list_files", "list_files"},
		{"list_files", "list_files"},
		{"exec_command", "exec_command"},
		{"", ""},
		{"ocx_client_", ""},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := CursorNormalizeWireName(c.in); got != c.want {
				t.Fatalf("in=%q want=%q got=%q", c.in, c.want, got)
			}
		})
	}
}

func TestCursorResponsesToolNameFromWire(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"ocx_client_list_files", "list_files"},
		{"list_files", "list_files"},
		{"exec_command", "exec_command"},
		{"", ""},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := CursorResponsesToolNameFromWire(c.in); got != c.want {
				t.Fatalf("in=%q want=%q got=%q", c.in, c.want, got)
			}
		})
	}
}

func TestCursorIsCursorTextToolMarker(t *testing.T) {
	// Today this always returns false — the seam is reserved for future port of
	// opencodex normalizeCursorTextToolMarkers. Pin the behavior so a future change is
	// caught by the diff.
	for _, in := range []string{"", "any", "ocx_client_list_files"} {
		if got := CursorIsCursorTextToolMarker(in); got {
			t.Fatalf("expected false for %q, got true", in)
		}
	}
}

func TestBuildCursorToolNotFound(t *testing.T) {
	payload := BuildCursorToolNotFound("missing_tool", []string{"exec_command", "list_files", "", "list_files", "apply_patch"})
	if payload.Name != "missing_tool" {
		t.Fatalf("name mismatch: %q", payload.Name)
	}
	if len(payload.AvailableTools) != 3 {
		t.Fatalf("dedup+empty-strip failed: %v", payload.AvailableTools)
	}
	// Sorted for stable diffs.
	want := []string{"apply_patch", "exec_command", "list_files"}
	for i, n := range want {
		if payload.AvailableTools[i] != n {
			t.Fatalf("idx %d: want %q got %q", i, n, payload.AvailableTools[i])
		}
	}
}

func TestMarshalCursorToolNotFound(t *testing.T) {
	out, err := MarshalCursorToolNotFound(CursorToolNotFound{Name: "x", AvailableTools: []string{"a"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if out["name"] != "x" {
		t.Fatalf("name: %v", out["name"])
	}
	tools, ok := out["availableTools"].([]string)
	if !ok || len(tools) != 1 || tools[0] != "a" {
		t.Fatalf("availableTools: %v", out["availableTools"])
	}
}

func TestMarshalCursorToolNotFound_EmptyAvailableKeepsKey(t *testing.T) {
	out, err := MarshalCursorToolNotFound(CursorToolNotFound{Name: "x"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	tools, ok := out["availableTools"].([]string)
	if !ok {
		t.Fatalf("availableTools key must be present even when empty, got %v", out["availableTools"])
	}
	if len(tools) != 0 {
		t.Fatalf("expected empty slice, got %v", tools)
	}
}

func TestCursorToolNotFoundJSON_RoundTrip(t *testing.T) {
	data, err := CursorToolNotFoundJSON("missing", []string{"exec", "wait"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("expected non-empty JSON bytes")
	}
	// Just sanity-check the shape via substring: name first, then availableTools array.
	s := string(data)
	if !containsAll(s, `"name"`, `"missing"`, `"availableTools"`, `"exec"`, `"wait"`) {
		t.Fatalf("unexpected JSON shape: %s", s)
	}
}

// containsAll is a tiny helper to avoid pulling in encoding/json twice for the round-trip
// sanity check. The helpers test the same wire contract; we don't need full decoding here.
func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !contains(s, p) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
