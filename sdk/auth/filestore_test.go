package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestExtractAccessToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		metadata map[string]any
		expected string
	}{
		{
			"antigravity top-level access_token",
			map[string]any{"access_token": "tok-abc"},
			"tok-abc",
		},
		{
			"gemini nested token.access_token",
			map[string]any{
				"token": map[string]any{"access_token": "tok-nested"},
			},
			"tok-nested",
		},
		{
			"top-level takes precedence over nested",
			map[string]any{
				"access_token": "tok-top",
				"token":        map[string]any{"access_token": "tok-nested"},
			},
			"tok-top",
		},
		{
			"empty metadata",
			map[string]any{},
			"",
		},
		{
			"whitespace-only access_token",
			map[string]any{"access_token": "   "},
			"",
		},
		{
			"wrong type access_token",
			map[string]any{"access_token": 12345},
			"",
		},
		{
			"token is not a map",
			map[string]any{"token": "not-a-map"},
			"",
		},
		{
			"nested whitespace-only",
			map[string]any{
				"token": map[string]any{"access_token": "  "},
			},
			"",
		},
		{
			"fallback to nested when top-level empty",
			map[string]any{
				"access_token": "",
				"token":        map[string]any{"access_token": "tok-fallback"},
			},
			"tok-fallback",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractAccessToken(tt.metadata)
			if got != tt.expected {
				t.Errorf("extractAccessToken() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestFileTokenStore_PersistsAntigravityPrimaryInfo(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	auth := &cliproxyauth.Auth{
		ID:       "antigravity-primary.json",
		FileName: "antigravity-primary.json",
		Provider: "antigravity",
		Disabled: false,
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "token",
		},
		PrimaryInfo: &cliproxyauth.PrimaryInfo{IsPrimary: true, Order: 1},
	}

	path, err := store.Save(context.Background(), auth)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected saved file at %s: %v", path, err)
	}
	loaded, err := store.readAuthFile(filepath.Join(baseDir, "antigravity-primary.json"), baseDir)
	if err != nil {
		t.Fatalf("readAuthFile() error = %v", err)
	}
	if loaded.PrimaryInfo == nil {
		t.Fatal("expected primary info to round-trip through filestore")
	}
	if !loaded.PrimaryInfo.IsPrimary {
		t.Fatal("expected loaded auth to remain primary")
	}
	if loaded.PrimaryInfo.Order != 1 {
		t.Fatalf("expected order 1, got %d", loaded.PrimaryInfo.Order)
	}
}
