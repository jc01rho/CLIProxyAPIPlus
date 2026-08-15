package auth

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestResolveFreebuffAPIKeyConfigPrefersConfigIndex(t *testing.T) {
	cfg := &internalconfig.Config{FreebuffKey: []internalconfig.FreebuffKey{
		{APIKey: "same-token", BaseURL: "https://example.test", Models: []internalconfig.FreebuffModel{{Name: "first"}}},
		{APIKey: "same-token", BaseURL: "https://example.test", Models: []internalconfig.FreebuffModel{{Name: "second"}}},
	}}
	auth := &Auth{Attributes: map[string]string{
		AttributeAPIKey:      "same-token",
		AttributeConfigIndex: "1",
		"base_url":           "https://example.test",
	}}
	entry := resolveFreebuffAPIKeyConfig(cfg, auth)
	if entry == nil || len(entry.Models) != 1 || entry.Models[0].Name != "second" {
		t.Fatalf("resolved entry = %#v, want config index 1", entry)
	}
}
