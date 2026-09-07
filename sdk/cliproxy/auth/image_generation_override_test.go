package auth

import "testing"

func TestImageGenerationModeOverride(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata map[string]any
		want     string
		wantOK   bool
	}{
		{name: "absent", metadata: map[string]any{}},
		{name: "nil metadata"},
		{name: "canonical chat", metadata: map[string]any{"disable_image_generation": "chat"}, want: "chat", wantOK: true},
		{name: "canonical passthrough", metadata: map[string]any{"disable_image_generation": "passthrough"}, want: "passthrough", wantOK: true},
		{name: "legacy key", metadata: map[string]any{"disable-image-generation": "chat"}, want: "chat", wantOK: true},
		{name: "canonical wins over legacy", metadata: map[string]any{"disable_image_generation": "true", "disable-image-generation": "chat"}, want: "true", wantOK: true},
		{name: "bool true", metadata: map[string]any{"disable_image_generation": true}, want: "true", wantOK: true},
		{name: "explicit bool false", metadata: map[string]any{"disable_image_generation": false}, want: "false", wantOK: true},
		{name: "string false is an override", metadata: map[string]any{"disable_image_generation": "false"}, want: "false", wantOK: true},
		{name: "blank string is not an override", metadata: map[string]any{"disable_image_generation": "   "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Auth{Metadata: tc.metadata}
			got, ok := a.ImageGenerationModeOverride()
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("ImageGenerationModeOverride() = (%q, %t), want (%q, %t)", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestImageGenerationModeOverrideNilAuth(t *testing.T) {
	var a *Auth
	if got, ok := a.ImageGenerationModeOverride(); ok || got != "" {
		t.Fatalf("nil auth override = (%q, %t), want (\"\", false)", got, ok)
	}
}

func TestCanonicalCredentialMetadataKeyMapsDisableImageGeneration(t *testing.T) {
	if got := CanonicalCredentialMetadataKey("disable-image-generation"); got != "disable_image_generation" {
		t.Fatalf("CanonicalCredentialMetadataKey() = %q, want disable_image_generation", got)
	}
	metadata := map[string]any{"disable-image-generation": "chat"}
	NormalizeCredentialMetadata(metadata)
	if got, ok := metadata["disable_image_generation"]; !ok || got != "chat" {
		t.Fatalf("normalized metadata = %v, want canonical chat", metadata)
	}
	if _, ok := metadata["disable-image-generation"]; ok {
		t.Fatal("legacy disable-image-generation key should be removed after normalization")
	}
}
