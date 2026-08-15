package cliproxy

import (
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestBuildFreebuffConfigModelsCarriesMaxContextLength(t *testing.T) {
	models := buildFreebuffConfigModels(&config.FreebuffKey{Models: []config.FreebuffModel{{
		Name:             "deepseek/deepseek-v4-flash",
		Alias:            "fb-flash",
		MaxContextLength: 131072,
	}}})
	if len(models) != 1 {
		t.Fatalf("models len = %d, want 1", len(models))
	}
	if models[0].ContextLength != 131072 || models[0].MaxContextLength != 131072 {
		t.Fatalf("context lengths = (%d, %d), want (131072, 131072)", models[0].ContextLength, models[0].MaxContextLength)
	}
}

func TestResolveConfigFreebuffKeyPrefersConfigIndex(t *testing.T) {
	cfg := &config.Config{FreebuffKey: []config.FreebuffKey{
		{APIKey: "same-token", BaseURL: "https://example.test", Models: []config.FreebuffModel{{Name: "first"}}},
		{APIKey: "same-token", BaseURL: "https://example.test", Models: []config.FreebuffModel{{Name: "second"}}},
	}}
	service := &Service{cfg: cfg}
	auth := &coreauth.Auth{Attributes: map[string]string{
		coreauth.AttributeAPIKey:      "same-token",
		coreauth.AttributeConfigIndex: "1",
		coreauth.AttributeSource:      "config:freebuff[test]",
		"base_url":                    "https://example.test",
	}}
	entry := service.resolveConfigFreebuffKey(auth)
	if entry == nil || len(entry.Models) != 1 || entry.Models[0].Name != "second" {
		t.Fatalf("resolved entry = %#v, want config index 1", entry)
	}
}
