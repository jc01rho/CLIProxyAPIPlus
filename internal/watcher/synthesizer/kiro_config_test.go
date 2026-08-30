package synthesizer

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestSynthesizeKiroKeysKeepsAuthAndAPIRegionsSeparate(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{KiroKey: []config.KiroKey{{
			AccessToken: "token",
			Region:      "us-east-1",
			APIRegion:   "eu-west-1",
		}}},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(auths) != 1 {
		t.Fatalf("auth count = %d, want 1", len(auths))
	}
	if got := auths[0].Attributes["region"]; got != "us-east-1" {
		t.Fatalf("auth region = %q, want us-east-1", got)
	}
	if got := auths[0].Attributes["api_region"]; got != "eu-west-1" {
		t.Fatalf("API region = %q, want eu-west-1", got)
	}
}
