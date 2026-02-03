package common

import (
	"strings"
	"testing"
)

func TestSanitizeToolUseID(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantLen int
	}{
		{
			name:    "valid alphanumeric with hyphen",
			input:   "toolu_abc123-def456",
			wantLen: 19,
		},
		{
			name:    "UUID with hyphens (hyphens are valid)",
			input:   "e9577a7d-809c-4e3f",
			wantLen: 18,
		},
		{
			name:    "invalid characters removed",
			input:   "tool@use#id$123",
			wantLen: 12,
		},
		{
			name:    "empty string",
			input:   "",
			wantLen: 0,
		},
		{
			name:    "too short after sanitization generates new ID",
			input:   "abc",
			wantLen: 18,
		},
		{
			name:    "special characters only generates new ID",
			input:   "@#$%^&*()",
			wantLen: 18,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeToolUseID(tt.input)

			if len(got) != tt.wantLen {
				t.Errorf("SanitizeToolUseID() length = %v, want %v (got: %s)", len(got), tt.wantLen, got)
			}

			for _, r := range got {
				if !((r >= 'a' && r <= 'z') ||
					(r >= 'A' && r <= 'Z') ||
					(r >= '0' && r <= '9') ||
					r == '_' || r == '-') {
					t.Errorf("SanitizeToolUseID() contains invalid character: %c in %s", r, got)
				}
			}
		})
	}
}

func TestGenerateToolUseID(t *testing.T) {
	ids := make(map[string]bool)

	for i := 0; i < 100; i++ {
		id := GenerateToolUseID()

		if !strings.HasPrefix(id, "toolu_") {
			t.Errorf("GenerateToolUseID() doesn't start with 'toolu_': %s", id)
		}

		if len(id) != 18 {
			t.Errorf("GenerateToolUseID() length = %v, want 18 (got: %s)", len(id), id)
		}

		if strings.Contains(id, "-") {
			t.Errorf("GenerateToolUseID() contains hyphen: %s", id)
		}

		if ids[id] {
			t.Errorf("GenerateToolUseID() generated duplicate: %s", id)
		}
		ids[id] = true

		for _, r := range id {
			if !((r >= 'a' && r <= 'z') ||
				(r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9') ||
				r == '_' || r == '-') {
				t.Errorf("GenerateToolUseID() contains invalid character: %c in %s", r, id)
			}
		}
	}
}

func TestSanitizeToolUseID_ClaudeAPICompliance(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "UUID slice with hyphen (hyphens are valid in pattern)",
			input: "e9577a7d-809",
		},
		{
			name:  "Full UUID (hyphens are valid in pattern)",
			input: "550e8400-e29b-41d4-a716-446655440000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeToolUseID(tt.input)

			if len(got) < 8 {
				t.Errorf("SanitizeToolUseID() too short: %s (len=%d)", got, len(got))
			}

			for _, r := range got {
				if !((r >= 'a' && r <= 'z') ||
					(r >= 'A' && r <= 'Z') ||
					(r >= '0' && r <= '9') ||
					r == '_' || r == '-') {
					t.Errorf("SanitizeToolUseID() contains invalid character: %c in %s", r, got)
				}
			}
		})
	}
}
