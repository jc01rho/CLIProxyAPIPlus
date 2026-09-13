package executor

import (
	"testing"
)

// devinTestConfig encodes one ClientModelConfig the way the live catalog does:
// f1 label, f4 disabled, f18 maxTokens, f22 modelUid, f23 modelInfo
// (f4 context window, f13 maxOutputTokens, f22 displayOption, f6 features).
func devinTestConfig(label, uid string, disabled bool, contextWindow, maxOutput, displayOption uint64, thinking bool) []byte {
	var info []byte
	if contextWindow > 0 {
		info = append(info, devinEncodeField(nil, devinModelInfoMaxTokensField, 0, devinEncodeVarint(nil, contextWindow))...)
	}
	if maxOutput > 0 {
		info = append(info, devinEncodeField(nil, devinModelInfoMaxOutputTokensField, 0, devinEncodeVarint(nil, maxOutput))...)
	}
	if displayOption > 0 {
		info = append(info, devinEncodeField(nil, devinModelInfoDisplayOptionField, 0, devinEncodeVarint(nil, displayOption))...)
	}
	if thinking {
		features := devinEncodeField(nil, devinFeaturesSupportsThinkingField, 0, devinEncodeVarint(nil, 1))
		info = append(info, devinEncodeSubMessage(devinModelInfoFeaturesField, features)...)
	}

	var cfg []byte
	cfg = append(cfg, devinEncodeField(nil, devinConfigLabelField, 2, devinEncodeString(label))...)
	if disabled {
		cfg = append(cfg, devinEncodeField(nil, devinConfigDisabledField, 0, devinEncodeVarint(nil, 1))...)
	}
	cfg = append(cfg, devinEncodeField(nil, devinConfigModelUIDField, 2, devinEncodeString(uid))...)
	if len(info) > 0 {
		cfg = append(cfg, devinEncodeSubMessage(devinConfigModelInfoField, info)...)
	}
	return devinEncodeSubMessage(1, cfg)
}

// TestDevinParseModelConfigsKeepsVisibleModels pins the catalog decode against
// the field numbers the live GetCliModelConfigs response uses.
func TestDevinParseModelConfigsKeepsVisibleModels(t *testing.T) {
	var body []byte
	// Live values observed for the SWE-2 family: no display option, no
	// disabled flag, 262000 context, 128000 output.
	body = append(body, devinTestConfig("SWE-2 High", "swe-2-high", false, 262000, 128000, 0, true)...)
	body = append(body, devinTestConfig("SWE-2 Medium", "swe-2-medium", false, 262000, 128000, 0, false)...)

	models := devinParseModelConfigs(body)
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2: %+v", len(models), models)
	}
	first := models[0]
	if first.ID != "swe-2-high" {
		t.Errorf("ID = %q, want swe-2-high", first.ID)
	}
	if first.DisplayName != "SWE-2 High" {
		t.Errorf("DisplayName = %q, want SWE-2 High", first.DisplayName)
	}
	if first.ContextLength != 262000 {
		t.Errorf("ContextLength = %d, want 262000", first.ContextLength)
	}
	if first.MaxCompletionTokens != 128000 {
		t.Errorf("MaxCompletionTokens = %d, want 128000", first.MaxCompletionTokens)
	}
	if first.OwnedBy != "devin" || first.Type != "devin" {
		t.Errorf("OwnedBy/Type = %q/%q, want devin/devin", first.OwnedBy, first.Type)
	}
	if first.Thinking == nil {
		t.Error("Thinking = nil, want thinking support when the config advertises it")
	}
	if models[1].Thinking != nil {
		t.Error("Thinking set for a config that does not advertise it")
	}
}

// TestDevinParseModelConfigsSkipsHiddenModels pins the two filters the native
// client applies: disabled configs and the internal display slots.
func TestDevinParseModelConfigsSkipsHiddenModels(t *testing.T) {
	var body []byte
	body = append(body, devinTestConfig("Disabled", "disabled-model", true, 200000, 64000, 0, false)...)
	// Display option 4 (QUICK_REVIEW) and 6 (INTERNAL_DEFAULT) are requested so
	// the server returns its full catalog, then dropped client-side.
	body = append(body, devinTestConfig("Quick Review", "quick-review-model", false, 200000, 64000, 4, false)...)
	body = append(body, devinTestConfig("Internal", "internal-model", false, 200000, 64000, 6, false)...)
	body = append(body, devinTestConfig("Visible", "visible-model", false, 200000, 64000, 8, false)...)

	models := devinParseModelConfigs(body)
	if len(models) != 1 {
		t.Fatalf("got %d models, want only the visible one: %+v", len(models), models)
	}
	if models[0].ID != "visible-model" {
		t.Errorf("ID = %q, want visible-model", models[0].ID)
	}
}

// TestDevinParseModelConfigsDedupes pins that a repeated uid is kept once.
func TestDevinParseModelConfigsDedupes(t *testing.T) {
	var body []byte
	body = append(body, devinTestConfig("SWE-2 High", "swe-2-high", false, 262000, 128000, 0, false)...)
	body = append(body, devinTestConfig("SWE-2 High dup", "swe-2-high", false, 262000, 128000, 0, false)...)

	models := devinParseModelConfigs(body)
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1 after dedupe", len(models))
	}
	if models[0].DisplayName != "SWE-2 High" {
		t.Errorf("DisplayName = %q, want the first occurrence", models[0].DisplayName)
	}
}

// TestDevinParseModelConfigsFallsBackOnMissingLimits pins the defaults for
// configs that ship no ModelInfo limits.
func TestDevinParseModelConfigsFallsBackOnMissingLimits(t *testing.T) {
	body := devinTestConfig("Bare", "bare-model", false, 0, 0, 0, false)
	models := devinParseModelConfigs(body)
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	if models[0].ContextLength != devinDefaultContextWindow {
		t.Errorf("ContextLength = %d, want %d", models[0].ContextLength, devinDefaultContextWindow)
	}
	if models[0].MaxCompletionTokens != devinDefaultMaxTokens {
		t.Errorf("MaxCompletionTokens = %d, want %d", models[0].MaxCompletionTokens, devinDefaultMaxTokens)
	}
}

// TestDevinBuildModelConfigsRequestCarriesDiscoveryIdentity pins the request
// shape that unlocks the full catalog: the "chisel" dev-channel identity plus
// the requested display slots. With the released "devin-cli" identity the
// backend answers 200 with a single CLI default config instead.
func TestDevinBuildModelConfigsRequestCarriesDiscoveryIdentity(t *testing.T) {
	body := devinBuildModelConfigsRequest("devin-session-token$abc")

	var meta []byte
	devinScanFields(body, func(num int, wire int, _ uint64, data []byte) bool {
		if num == devinReqMetadataField && wire == 2 {
			meta = data
			return false
		}
		return true
	})
	if len(meta) == 0 {
		t.Fatal("request carries no metadata message")
	}

	strs := make(map[int]string)
	var displays []uint64
	devinScanFields(meta, func(num int, wire int, v uint64, data []byte) bool {
		switch wire {
		case 2:
			strs[num] = string(data)
		case 0:
			if num == devinMetaDisplayOptionsField {
				displays = append(displays, v)
			}
		}
		return true
	})

	if strs[devinMetaIDENameField] != devinDiscoveryIDEName {
		t.Errorf("ideName = %q, want %q", strs[devinMetaIDENameField], devinDiscoveryIDEName)
	}
	if strs[devinMetaIDEVersionField] != devinDiscoveryIDEVersion {
		t.Errorf("ideVersion = %q, want %q", strs[devinMetaIDEVersionField], devinDiscoveryIDEVersion)
	}
	if strs[devinMetaExtensionNameField] != devinDiscoveryIDEName {
		t.Errorf("extensionName = %q, want %q", strs[devinMetaExtensionNameField], devinDiscoveryIDEName)
	}
	if strs[devinMetaAPIKeyField] != "devin-session-token$abc" {
		t.Errorf("apiKey = %q, want the session token", strs[devinMetaAPIKeyField])
	}
	if len(displays) != len(devinDiscoveryDisplayOptions) {
		t.Fatalf("display options = %v, want %v", displays, devinDiscoveryDisplayOptions)
	}
	for i, want := range devinDiscoveryDisplayOptions {
		if displays[i] != want {
			t.Errorf("display option %d = %d, want %d", i, displays[i], want)
		}
	}
}

// devinTestConfigWithCostTier encodes a ClientModelConfig that also carries
// field 24 (model_cost_tier), which devinTestConfig does not emit.
func devinTestConfigWithCostTier(label, uid string, contextWindow, maxOutput, costTier uint64) []byte {
	var info []byte
	if contextWindow > 0 {
		info = append(info, devinEncodeField(nil, devinModelInfoMaxTokensField, 0, devinEncodeVarint(nil, contextWindow))...)
	}
	if maxOutput > 0 {
		info = append(info, devinEncodeField(nil, devinModelInfoMaxOutputTokensField, 0, devinEncodeVarint(nil, maxOutput))...)
	}
	var cfg []byte
	cfg = append(cfg, devinEncodeField(nil, devinConfigLabelField, 2, devinEncodeString(label))...)
	cfg = append(cfg, devinEncodeField(nil, devinConfigModelUIDField, 2, devinEncodeString(uid))...)
	if len(info) > 0 {
		cfg = append(cfg, devinEncodeSubMessage(devinConfigModelInfoField, info)...)
	}
	cfg = append(cfg, devinEncodeField(nil, devinConfigCostTierField, 0, devinEncodeVarint(nil, costTier))...)
	return devinEncodeSubMessage(1, cfg)
}

// TestDevinParseModelConfigsMarksFreeTier pins that a config whose
// model_cost_tier is MODEL_COST_TIER_FREE (4) surfaces as a free model, while
// paid tiers do not. The live catalog marks glm-5-2 and swe-2-high as FREE.
func TestDevinParseModelConfigsMarksFreeTier(t *testing.T) {
	var body []byte
	body = append(body, devinTestConfigWithCostTier("GLM-5.2 High", "glm-5-2", 200000, 64000, devinCostTierFree)...)
	body = append(body, devinTestConfigWithCostTier("GLM-5.2 Max", "glm-5-2-max", 200000, 64000, 1)...) // LOW

	models := devinParseModelConfigs(body)
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2", len(models))
	}
	if !models[0].IsFree || models[0].CostTier != "free" {
		t.Errorf("glm-5-2 IsFree=%v CostTier=%q, want free", models[0].IsFree, models[0].CostTier)
	}
	if models[1].IsFree || models[1].CostTier != "" {
		t.Errorf("glm-5-2-max IsFree=%v CostTier=%q, want not-free", models[1].IsFree, models[1].CostTier)
	}
}
