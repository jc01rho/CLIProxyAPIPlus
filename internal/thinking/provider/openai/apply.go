// Package openai implements thinking configuration for OpenAI/Codex models.
package openai

import (
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
)

// Applier implements thinking.ProviderApplier for OpenAI models.
type Applier struct{}

var _ thinking.ProviderDocApplier = (*Applier)(nil)

// NewApplier creates a new OpenAI thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("openai", NewApplier())
}

// Apply applies thinking configuration to OpenAI request body.
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	thinkingType, effort, ok := resolveOpenAIThinking(config, modelInfo)
	if !ok {
		return body, nil
	}
	if len(body) == 0 || !ojson.ValidBytes(body) {
		body = []byte("{}")
	}
	if thinkingType != "" {
		return setOpenAIThinkingType(body, thinkingType), nil
	}
	result, _ := ojson.SetBytes(body, "reasoning_effort", effort)
	return result, nil
}

// ApplyDoc applies thinking configuration onto doc in place.
func (a *Applier) ApplyDoc(doc *ojson.Doc, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) error {
	if doc == nil {
		return nil
	}
	thinkingType, effort, ok := resolveOpenAIThinking(config, modelInfo)
	if !ok {
		return nil
	}
	if thinkingType != "" {
		_ = doc.Delete("reasoning_effort")
		_ = doc.Set("thinking.type", thinkingType)
		return nil
	}
	_ = doc.Set("reasoning_effort", effort)
	return nil
}

// resolveOpenAIThinking returns either a thinking.type value (for models that
// expose enable/disable/adaptive switches) or a reasoning_effort value.
func resolveOpenAIThinking(config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) (thinkingType string, effort string, ok bool) {
	if thinking.IsUserDefinedModel(modelInfo) {
		if t, okType := openAIThinkingType(config, modelInfo); okType {
			return t, "", true
		}
		effort, ok = resolveOpenAIEffort(config, modelInfo)
		return "", effort, ok
	}
	if modelInfo.Thinking == nil {
		return "", "", false
	}
	// Only handle discrete levels and explicit on/off/automatic modes.
	if config.Mode != thinking.ModeLevel && config.Mode != thinking.ModeNone && config.Mode != thinking.ModeAuto {
		return "", "", false
	}
	if t, okType := openAIThinkingType(config, modelInfo); okType {
		return t, "", true
	}
	if config.Mode == thinking.ModeAuto {
		return "", "", false
	}
	effort, ok = resolveOpenAIEffort(config, modelInfo)
	return "", effort, ok
}

func resolveOpenAIEffort(config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) (string, bool) {
	if thinking.IsUserDefinedModel(modelInfo) {
		switch config.Mode {
		case thinking.ModeLevel:
			if config.Level == "" {
				return "", false
			}
			return string(config.Level), true
		case thinking.ModeNone:
			effort := string(thinking.LevelNone)
			if config.Level != "" {
				effort = string(config.Level)
			}
			return effort, true
		case thinking.ModeAuto:
			// Auto mode for user-defined models: pass through as "auto"
			return string(thinking.LevelAuto), true
		case thinking.ModeBudget:
			// Budget mode: convert budget to level using threshold mapping
			level, ok := thinking.ConvertBudgetToLevel(config.Budget)
			if !ok {
				return "", false
			}
			return level, true
		default:
			return "", false
		}
	}
	if modelInfo.Thinking == nil {
		return "", false
	}
	if config.Mode != thinking.ModeLevel && config.Mode != thinking.ModeNone {
		return "", false
	}
	if config.Mode == thinking.ModeLevel {
		return string(config.Level), true
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
		return "", false
	}
	return effort, true
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
	if withoutEffort, err := ojson.DeleteBytes(body, "reasoning_effort"); err == nil {
		body = withoutEffort
	}
	result, _ := ojson.SetBytes(body, "thinking.type", thinkingType)
	return result
}
