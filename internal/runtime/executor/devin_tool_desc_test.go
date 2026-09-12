package executor

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestDevinTruncateToolDescKeepsValidUTF8 pins the bug that made every omo
// request fail: the description was cut by byte, which split a Korean rune and
// left a dangling 0xED in a protobuf string field. Protobuf strings must be
// valid UTF-8, so the backend rejected the request with invalid_argument and no
// further detail.
func TestDevinTruncateToolDescKeepsValidUTF8(t *testing.T) {
	// The real trigger: a Korean description past the cloud limit.
	desc := strings.Repeat("태스크 통합 관리. action별 사용법 설명입니다. ", 80)
	if len(desc) <= devinMaxToolDescLen {
		t.Fatalf("fixture is %d bytes, needs to exceed the %d limit", len(desc), devinMaxToolDescLen)
	}

	got := devinTruncateToolDesc(desc)

	if !utf8.ValidString(got) {
		t.Fatalf("truncated description is not valid UTF-8: %q", got[max(0, len(got)-40):])
	}
	if len(got) > devinMaxToolDescLen {
		t.Errorf("len = %d, want <= %d", len(got), devinMaxToolDescLen)
	}
	if !strings.HasSuffix(got, devinToolDescSuffix) {
		t.Errorf("truncated description lost its marker: %q", got[max(0, len(got)-40):])
	}
}

// TestDevinTruncateToolDescLeavesShortDescriptions pins that descriptions under
// the limit pass through untouched, including non-ASCII ones.
func TestDevinTruncateToolDescLeavesShortDescriptions(t *testing.T) {
	for _, desc := range []string{"", "plain ascii", "태스크 통합 관리", "emoji 🎯 mixed"} {
		if got := devinTruncateToolDesc(desc); got != desc {
			t.Errorf("devinTruncateToolDesc(%q) = %q, want it unchanged", desc, got)
		}
	}
}

// TestDevinEncodeToolDefEmitsValidUTF8 pins the same guarantee at the wire
// boundary, which is where the backend actually validates it.
func TestDevinEncodeToolDefEmitsValidUTF8(t *testing.T) {
	def := devinToolDef{
		Name:        "sparrow-devcenter_devcenter_task",
		Description: strings.Repeat("태스크 통합 관리. 액션별 사용법. ", 100),
		Parameters:  []byte(`{"type":"object","properties":{}}`),
	}

	encoded := devinEncodeToolDef(def)

	var desc string
	devinScanFields(encoded, func(num int, wire int, _ uint64, data []byte) bool {
		if num == devinToolDefDescField && wire == 2 {
			desc = string(data)
			return false
		}
		return true
	})
	if desc == "" {
		t.Fatal("encoded tool def carries no description")
	}
	if !utf8.ValidString(desc) {
		t.Errorf("encoded description is not valid UTF-8, which the backend rejects as invalid_argument")
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
