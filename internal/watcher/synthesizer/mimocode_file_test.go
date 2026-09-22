package synthesizer

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestSynthesizeMimocodeAPIAuthFile(t *testing.T) {
	authDir := t.TempDir()
	path := filepath.Join(authDir, "mimocode-user.json")
	payload := []byte(`{"type":"api","provider":"mimocode","api_key":"secret","uid":"user","base_url":"https://region.example/v1","key_name":"stable"}`)
	auths, errSynthesize := SynthesizeAuthFile(&SynthesisContext{
		Config: &config.Config{}, AuthDir: authDir, Now: time.Now(), IDGenerator: NewStableIDGenerator(),
	}, path, payload)
	if errSynthesize != nil {
		t.Fatalf("SynthesizeAuthFile() error = %v", errSynthesize)
	}
	if len(auths) != 1 {
		t.Fatalf("auth count = %d, want 1", len(auths))
	}
	auth := auths[0]
	if auth.Provider != "mimocode" || auth.AuthKind() != coreauth.AuthKindAPIKey {
		t.Fatalf("provider/auth kind = %q/%q", auth.Provider, auth.AuthKind())
	}
	if auth.Attributes["api_key"] != "secret" || auth.Attributes["uid"] != "user" || auth.Attributes["base_url"] != "https://region.example/v1" {
		t.Fatalf("attributes = %+v", auth.Attributes)
	}
}
