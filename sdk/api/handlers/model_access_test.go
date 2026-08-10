package handlers

import (
	"testing"

	"github.com/gin-gonic/gin"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

func TestModelAllowedForPatterns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		patterns []string
		model    string
		want     bool
	}{
		{name: "empty allows all", model: "claude-sonnet-4-5", want: true},
		{name: "exact match", patterns: []string{"gpt-5.2"}, model: "gpt-5.2", want: true},
		{name: "exact mismatch", patterns: []string{"gpt-5.2"}, model: "gpt-5.3", want: false},
		{name: "asterisk prefix", patterns: []string{"gpt-*"}, model: "gpt-5.3-codex", want: true},
		{name: "asterisk middle", patterns: []string{"openrouter/*/free"}, model: "openrouter/qwen/free", want: true},
		{name: "asterisk mismatch", patterns: []string{"gpt-*"}, model: "claude-sonnet-4-5", want: false},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := sdkaccess.ModelAllowed(test.model, test.patterns); got != test.want {
				t.Fatalf("ModelAllowed(%q, %v) = %v, want %v", test.model, test.patterns, got, test.want)
			}
		})
	}
}

func TestFilterModelMapsUsesGinContextPatterns(t *testing.T) {
	t.Parallel()

	ctx, _ := gin.CreateTestContext(nil)
	sdkaccess.SetModelAccessPatterns(ctx, []string{"gpt-*"})
	models := []map[string]any{
		{"id": "gpt-5.3"},
		{"id": "claude-sonnet-4-5"},
		{"name": "models/gpt-5.2"},
	}

	got := sdkaccess.FilterModelMaps(ctx, models)
	if len(got) != 2 {
		t.Fatalf("filtered model count = %d, want 2: %#v", len(got), got)
	}
	if got[0]["id"] != "gpt-5.3" || got[1]["name"] != "models/gpt-5.2" {
		t.Fatalf("unexpected filtered models: %#v", got)
	}
}
