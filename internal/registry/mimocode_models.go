package registry

// GetMimocodeModels returns the supported Xiaomi MiMo Code catalog.
func GetMimocodeModels() []*ModelInfo {
	return []*ModelInfo{
		mimocodeStaticModel("mimo-v2.6-pro", "MiMo V2.6 Pro"),
		mimocodeStaticModel("mimo-v2.6-flash", "MiMo V2.6 Flash"),
		mimocodeStaticModel("mimo-v2.6-pro-ultraspeed", "MiMo V2.6 Pro Ultraspeed"),
		mimocodeStaticModel("mimo-v2.5-pro", "MiMo V2.5 Pro"),
		mimocodeStaticModel("mimo-v2.5", "MiMo V2.5"),
		mimocodeStaticModel("mimo-v2.5-pro-ultraspeed", "MiMo V2.5 Pro Ultraspeed"),
	}
}

func mimocodeStaticModel(id, displayName string) *ModelInfo {
	return &ModelInfo{
		ID:                  id,
		Name:                id,
		Object:              "model",
		Created:             1790035200,
		OwnedBy:             "xiaomi",
		Type:                "mimocode",
		DisplayName:         displayName,
		ContextLength:       1_000_000,
		MaxCompletionTokens: 131072,
		SupportedEndpoints:  []string{"/chat/completions"},
		Thinking:            &ThinkingSupport{DynamicAllowed: true},
	}
}
