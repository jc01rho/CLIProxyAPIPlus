package util

import (
	"strings"
	"testing"
)

func TestTruncateCallID(t *testing.T) {
	tests := []struct {
		name   string
		id     string
		maxLen int
		want   string
	}{
		{"short string", "abc", 10, "abc"},
		{"exact length", "abcdefgh", 8, "abcdefgh"},
		{"long string truncated", "abcdefghij", 5, "abcde"},
		{"maxLen zero defaults to 64", strings.Repeat("x", 70), 0, strings.Repeat("x", 64)},
		{"maxLen negative defaults to 64", strings.Repeat("x", 70), -1, strings.Repeat("x", 64)},
		{"empty string", "", 10, ""},
		{"empty string maxLen zero", "", 0, ""},
		{"exact 64-char id maxLen=64 no truncation", strings.Repeat("a", 64), 64, strings.Repeat("a", 64)},
		{"68-char id maxLen=64 truncated to 64", strings.Repeat("b", 68), 64, strings.Repeat("b", 64)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateCallID(tt.id, tt.maxLen)
			if got != tt.want {
				t.Errorf("TruncateCallID(%q, %d) = %q; want %q", tt.id, tt.maxLen, got, tt.want)
			}
		})
	}
}
