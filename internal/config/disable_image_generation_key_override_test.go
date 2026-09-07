package config

import "testing"

func TestParseConfigBytesReadsCodexKeyDisableImageGeneration(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
disable-image-generation: false
codex-api-key:
  - api-key: chat-key
    base-url: https://codex.example.com
    disable-image-generation: chat
  - api-key: all-key
    base-url: https://codex.example.com
    disable-image-generation: true
  - api-key: passthrough-key
    base-url: https://codex.example.com
    disable-image-generation: passthrough
  - api-key: explicit-off-key
    base-url: https://codex.example.com
    disable-image-generation: false
  - api-key: inherit-key
    base-url: https://codex.example.com
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if cfg.DisableImageGeneration != DisableImageGenerationOff {
		t.Fatalf("global disable-image-generation = %v, want off", cfg.DisableImageGeneration)
	}
	if len(cfg.CodexKey) != 5 {
		t.Fatalf("codex-api-key entries = %d, want 5", len(cfg.CodexKey))
	}

	want := []struct {
		name string
		mode DisableImageGenerationMode
	}{
		{"chat-key", DisableImageGenerationChat},
		{"all-key", DisableImageGenerationAll},
		{"passthrough-key", DisableImageGenerationPassthrough},
		{"explicit-off-key", DisableImageGenerationOff},
	}
	for i, tc := range want {
		got := cfg.CodexKey[i].DisableImageGeneration
		if got == nil {
			t.Fatalf("%s disable-image-generation = nil, want %v", tc.name, tc.mode)
		}
		if *got != tc.mode {
			t.Errorf("%s disable-image-generation = %v, want %v", tc.name, *got, tc.mode)
		}
	}

	// An omitted key must stay nil so the global value keeps applying.
	if cfg.CodexKey[4].DisableImageGeneration != nil {
		t.Errorf("inherit-key disable-image-generation = %v, want nil", *cfg.CodexKey[4].DisableImageGeneration)
	}
}

func TestParseConfigBytesRejectsInvalidCodexKeyDisableImageGeneration(t *testing.T) {
	_, errParse := ParseConfigBytes([]byte(`
codex-api-key:
  - api-key: bad-key
    base-url: https://codex.example.com
    disable-image-generation: sometimes
`))
	if errParse == nil {
		t.Fatal("ParseConfigBytes() error = nil, want invalid disable-image-generation error")
	}
}

func TestParseDisableImageGenerationModeExported(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want DisableImageGenerationMode
	}{
		{"false", DisableImageGenerationOff},
		{"true", DisableImageGenerationAll},
		{"chat", DisableImageGenerationChat},
		{"passthrough", DisableImageGenerationPassthrough},
	} {
		got, err := ParseDisableImageGenerationMode(tc.in)
		if err != nil {
			t.Fatalf("ParseDisableImageGenerationMode(%q) error = %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("ParseDisableImageGenerationMode(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
	if _, err := ParseDisableImageGenerationMode("nope"); err == nil {
		t.Error("ParseDisableImageGenerationMode(\"nope\") error = nil, want error")
	}
}
