// Package openai implements thinking configuration for OpenAI/Codex models.
//
// OpenAI models use the reasoning_effort format with discrete levels
// (low/medium/high). Some models support xhigh and none levels.
// See: _bmad-output/planning-artifacts/architecture.md#Epic-8
package openai

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Applier implements thinking.ProviderApplier for OpenAI models.
//
// OpenAI-specific behavior:
//   - Output format: reasoning_effort (string: low/medium/high/xhigh)
//   - Level-only mode: no numeric budget support
//   - Some models support ZeroAllowed (gpt-5.1, gpt-5.2)
type Applier struct{}

var _ thinking.ProviderApplier = (*Applier)(nil)

// NewApplier creates a new OpenAI thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("openai", NewApplier())
}

// Apply applies thinking configuration to OpenAI request body.
//
// Expected output format:
//
//	{
//	  "reasoning_effort": "high"
//	}
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if thinking.IsUserDefinedModel(modelInfo) {
		return applyCompatibleOpenAI(body, config, modelInfo)
	}
	if modelInfo.Thinking == nil {
		return body, nil
	}

	// Only handle discrete levels and explicit on/off/automatic modes.
	if config.Mode != thinking.ModeLevel && config.Mode != thinking.ModeNone && config.Mode != thinking.ModeAuto {
		return body, nil
	}

	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	if thinkingType, ok := openAIThinkingType(config, modelInfo); ok {
		return setOpenAIThinkingType(body, thinkingType), nil
	}
	if config.Mode == thinking.ModeAuto {
		return body, nil
	}

	if config.Mode == thinking.ModeLevel {
		result, _ := sjson.SetBytes(body, "reasoning_effort", string(config.Level))
		return result, nil
	}

	effort := ""
	support := modelInfo.Thinking
	if config.Budget == 0 {
		if support.ZeroAllowed || thinking.HasLevel(support.Levels, string(thinking.LevelNone)) {
			effort = string(thinking.LevelNone)
		}
	}
	if effort == "" && config.Level != "" {
		effort = string(config.Level)
	}
	if effort == "" && len(support.Levels) > 0 {
		effort = support.Levels[0]
	}
	if effort == "" {
		return body, nil
	}

	result, _ := sjson.SetBytes(body, "reasoning_effort", effort)
	return result, nil
}

func applyCompatibleOpenAI(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}
	if thinkingType, ok := openAIThinkingType(config, modelInfo); ok {
		return setOpenAIThinkingType(body, thinkingType), nil
	}

	var effort string
	switch config.Mode {
	case thinking.ModeLevel:
		if config.Level == "" {
			return body, nil
		}
		effort = string(config.Level)
	case thinking.ModeNone:
		effort = string(thinking.LevelNone)
		if config.Level != "" {
			effort = string(config.Level)
		}
	case thinking.ModeAuto:
		// Auto mode for user-defined models: pass through as "auto"
		effort = string(thinking.LevelAuto)
	case thinking.ModeBudget:
		// Budget mode: convert budget to level using threshold mapping
		level, ok := thinking.ConvertBudgetToLevel(config.Budget)
		if !ok {
			return body, nil
		}
		effort = level
	default:
		return body, nil
	}

	result, _ := sjson.SetBytes(body, "reasoning_effort", effort)
	return result, nil
}

func openAIThinkingType(config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) (string, bool) {
	switch config.Mode {
	case thinking.ModeLevel:
		switch config.Level {
		case thinking.LevelEnable:
			return "enabled", true
		case thinking.LevelDisable:
			return "disabled", true
		case thinking.LevelAdaptive:
			return "adaptive", true
		}
	case thinking.ModeNone:
		if modelInfo != nil && modelInfo.Thinking != nil && thinking.HasLevel(modelInfo.Thinking.Levels, string(thinking.LevelDisable)) {
			return "disabled", true
		}
	case thinking.ModeAuto:
		if modelInfo == nil || modelInfo.Thinking == nil {
			return "", false
		}
		if thinking.HasLevel(modelInfo.Thinking.Levels, string(thinking.LevelAdaptive)) {
			return "adaptive", true
		}
		if thinking.HasLevel(modelInfo.Thinking.Levels, string(thinking.LevelEnable)) {
			return "enabled", true
		}
	}
	return "", false
}

func setOpenAIThinkingType(body []byte, thinkingType string) []byte {
	if withoutEffort, err := sjson.DeleteBytes(body, "reasoning_effort"); err == nil {
		body = withoutEffort
	}
	result, _ := sjson.SetBytes(body, "thinking.type", thinkingType)
	return result
}
