package antigravity

// ModelThinkingLevel is the named thinking tier used by Antigravity model variants.
type ModelThinkingLevel string

const (
	ModelThinkingLevelMinimal ModelThinkingLevel = "minimal"
	ModelThinkingLevelLow     ModelThinkingLevel = "low"
	ModelThinkingLevelMedium  ModelThinkingLevel = "medium"
	ModelThinkingLevelHigh    ModelThinkingLevel = "high"
)

// ModelThinkingConfig contains numeric thinking configuration for a model variant.
type ModelThinkingConfig struct {
	ThinkingBudget int `json:"thinkingBudget"`
}

// ModelVariant describes an alternate variant exposed for a model definition.
type ModelVariant struct {
	ThinkingLevel  ModelThinkingLevel   `json:"thinkingLevel,omitempty"`
	ThinkingConfig *ModelThinkingConfig `json:"thinkingConfig,omitempty"`
	Disabled       bool                 `json:"disabled,omitempty"`
}

// ModelLimit defines the context and output token limits for a model.
type ModelLimit struct {
	Context int `json:"context"`
	Output  int `json:"output"`
}

// ModelModality is an input or output modality supported by a model.
type ModelModality string

const (
	ModelModalityText  ModelModality = "text"
	ModelModalityImage ModelModality = "image"
	ModelModalityPDF   ModelModality = "pdf"
)

// ModelQuotaGroup is the quota bucket a backend model consumes.
type ModelQuotaGroup string

const (
	ModelQuotaGroupClaude      ModelQuotaGroup = "claude"
	ModelQuotaGroupGeminiPro   ModelQuotaGroup = "gemini-pro"
	ModelQuotaGroupGeminiFlash ModelQuotaGroup = "gemini-flash"
	ModelQuotaGroupGPTOSS      ModelQuotaGroup = "gpt-oss"
)

// ModelModalities lists supported input and output modalities for a model.
type ModelModalities struct {
	Input  []ModelModality `json:"input"`
	Output []ModelModality `json:"output"`
}

// ModelCost contains per-token input and output cost metadata.
type ModelCost struct {
	Input  int `json:"input"`
	Output int `json:"output"`
}

// OpencodeModelDefinition is the OpenCode provider model shape used by Antigravity.
type OpencodeModelDefinition struct {
	ID          string                  `json:"id"`
	Name        string                  `json:"name"`
	ReleaseDate string                  `json:"release_date"`
	Attachment  bool                    `json:"attachment"`
	Reasoning   bool                    `json:"reasoning"`
	Temperature bool                    `json:"temperature"`
	ToolCall    bool                    `json:"tool_call"`
	Limit       ModelLimit              `json:"limit"`
	Modalities  ModelModalities         `json:"modalities"`
	Cost        ModelCost               `json:"cost"`
	Options     map[string]any          `json:"options"`
	Variants    map[string]ModelVariant `json:"variants,omitempty"`
}

// OpencodeModelDefinitions maps model IDs to OpenCode model definitions.
type OpencodeModelDefinitions map[string]OpencodeModelDefinition

type opencodeModelDefinitionInput struct {
	Name       string
	Reasoning  bool
	Limit      ModelLimit
	Modalities ModelModalities
	Variants   map[string]ModelVariant
}

var defaultModalities = ModelModalities{
	Input:  []ModelModality{ModelModalityText, ModelModalityImage, ModelModalityPDF},
	Output: []ModelModality{ModelModalityText},
}

const modelReleaseDate = ""

var (
	defaultCost    = ModelCost{Input: 0, Output: 0}
	defaultOptions = map[string]any{}
)

func defineModel(id string, model opencodeModelDefinitionInput) OpencodeModelDefinition {
	return OpencodeModelDefinition{
		ID:          id,
		Name:        model.Name,
		ReleaseDate: modelReleaseDate,
		Attachment:  hasNonTextInputModality(model.Modalities),
		Reasoning:   model.Reasoning,
		Temperature: true,
		ToolCall:    true,
		Limit:       model.Limit,
		Modalities:  copyModalities(model.Modalities),
		Cost:        defaultCost,
		Options:     copyOptions(defaultOptions),
		Variants:    copyVariants(model.Variants),
	}
}

func hasNonTextInputModality(modalities ModelModalities) bool {
	for _, modality := range modalities.Input {
		if modality != ModelModalityText {
			return true
		}
	}
	return false
}

var allModelDefinitions = OpencodeModelDefinitions{
	"antigravity-gemini-3.1-pro": defineModel("antigravity-gemini-3.1-pro", opencodeModelDefinitionInput{
		Name:       "Gemini 3.1 Pro (Antigravity)",
		Reasoning:  true,
		Limit:      ModelLimit{Context: 1048576, Output: 65535},
		Modalities: defaultModalities,
		Variants: map[string]ModelVariant{
			"low":  {ThinkingLevel: ModelThinkingLevelLow},
			"high": {ThinkingLevel: ModelThinkingLevelHigh},
		},
	}),
	"antigravity-gemini-3.5-flash": defineModel("antigravity-gemini-3.5-flash", opencodeModelDefinitionInput{
		Name:       "Gemini 3.5 Flash (Antigravity)",
		Reasoning:  true,
		Limit:      ModelLimit{Context: 1048576, Output: 65536},
		Modalities: defaultModalities,
		Variants: map[string]ModelVariant{
			"low":  {ThinkingLevel: ModelThinkingLevelLow},
			"high": {ThinkingLevel: ModelThinkingLevelHigh},
		},
	}),
	"antigravity-claude-sonnet-4-6-thinking": defineModel("antigravity-claude-sonnet-4-6-thinking", opencodeModelDefinitionInput{
		Name:       "Claude Sonnet 4.6 Thinking (Antigravity)",
		Reasoning:  true,
		Limit:      ModelLimit{Context: 250000, Output: 64000},
		Modalities: defaultModalities,
		Variants: map[string]ModelVariant{
			"low":  {Disabled: true},
			"high": {Disabled: true},
		},
	}),
	"antigravity-claude-opus-4-6-thinking": defineModel("antigravity-claude-opus-4-6-thinking", opencodeModelDefinitionInput{
		Name:       "Claude Opus 4.6 Thinking (Antigravity)",
		Reasoning:  true,
		Limit:      ModelLimit{Context: 250000, Output: 64000},
		Modalities: defaultModalities,
		Variants: map[string]ModelVariant{
			"low":  {Disabled: true},
			"high": {Disabled: true},
		},
	}),
	"antigravity-gemini-3.1-flash-image": defineModel("antigravity-gemini-3.1-flash-image", opencodeModelDefinitionInput{
		Name:      "Gemini 3.1 Flash Image (Antigravity)",
		Reasoning: false,
		Limit:     ModelLimit{Context: 66000, Output: 33000},
		Modalities: ModelModalities{
			Input:  []ModelModality{ModelModalityText, ModelModalityImage},
			Output: []ModelModality{ModelModalityText, ModelModalityImage},
		},
	}),
	"antigravity-gpt-oss-120b": defineModel("antigravity-gpt-oss-120b", opencodeModelDefinitionInput{
		Name:       "GPT-OSS 120B (Antigravity)",
		Reasoning:  false,
		Limit:      ModelLimit{Context: 128000, Output: 16384},
		Modalities: defaultModalities,
		Variants: map[string]ModelVariant{
			"medium": {},
		},
	}),
	"gemini-2.5-flash": defineModel("gemini-2.5-flash", opencodeModelDefinitionInput{
		Name:       "Gemini 2.5 Flash (Gemini CLI)",
		Reasoning:  true,
		Limit:      ModelLimit{Context: 1048576, Output: 65536},
		Modalities: defaultModalities,
	}),
	"gemini-2.5-pro": defineModel("gemini-2.5-pro", opencodeModelDefinitionInput{
		Name:       "Gemini 2.5 Pro (Gemini CLI)",
		Reasoning:  true,
		Limit:      ModelLimit{Context: 1048576, Output: 65536},
		Modalities: defaultModalities,
	}),
	"gemini-3-flash-preview": defineModel("gemini-3-flash-preview", opencodeModelDefinitionInput{
		Name:       "Gemini 3 Flash Preview (Gemini CLI)",
		Reasoning:  true,
		Limit:      ModelLimit{Context: 1048576, Output: 65536},
		Modalities: defaultModalities,
	}),
	"gemini-3.1-pro-preview": defineModel("gemini-3.1-pro-preview", opencodeModelDefinitionInput{
		Name:       "Gemini 3.1 Pro Preview (Gemini CLI)",
		Reasoning:  true,
		Limit:      ModelLimit{Context: 1048576, Output: 65535},
		Modalities: defaultModalities,
	}),
	"gemini-3.5-flash-preview": defineModel("gemini-3.5-flash-preview", opencodeModelDefinitionInput{
		Name:       "Gemini 3.5 Flash Preview (Gemini CLI)",
		Reasoning:  true,
		Limit:      ModelLimit{Context: 1048576, Output: 65536},
		Modalities: defaultModalities,
	}),
	"gemini-3.1-flash-image": defineModel("gemini-3.1-flash-image", opencodeModelDefinitionInput{
		Name:      "Gemini 3.1 Flash Image (Gemini CLI)",
		Reasoning: false,
		Limit:     ModelLimit{Context: 66000, Output: 33000},
		Modalities: ModelModalities{
			Input:  []ModelModality{ModelModalityText, ModelModalityImage},
			Output: []ModelModality{ModelModalityText, ModelModalityImage},
		},
	}),
	"gemini-3.1-flash-image-preview": defineModel("gemini-3.1-flash-image-preview", opencodeModelDefinitionInput{
		Name:      "Gemini 3.1 Flash Image Preview (Gemini CLI)",
		Reasoning: false,
		Limit:     ModelLimit{Context: 66000, Output: 33000},
		Modalities: ModelModalities{
			Input:  []ModelModality{ModelModalityText, ModelModalityImage},
			Output: []ModelModality{ModelModalityText, ModelModalityImage},
		},
	}),
	"gemini-3.1-pro-preview-customtools": defineModel("gemini-3.1-pro-preview-customtools", opencodeModelDefinitionInput{
		Name:       "Gemini 3.1 Pro Preview Custom Tools (Gemini CLI)",
		Reasoning:  true,
		Limit:      ModelLimit{Context: 1048576, Output: 65535},
		Modalities: defaultModalities,
	}),
}

var resolverAliases = map[string]string{
	"gemini-3.1-pro-low":                         "gemini-3.1-pro",
	"gemini-3.1-pro-high":                        "gemini-3.1-pro",
	"gemini-3-flash-low":                         "gemini-3-flash",
	"gemini-3-flash-medium":                      "gemini-3-flash",
	"gemini-3-flash-high":                        "gemini-3-flash",
	"gemini-3.5-flash-low":                       "gemini-3.5-flash",
	"gemini-3.5-flash-medium":                    "gemini-3.5-flash",
	"gemini-3.5-flash-high":                      "gemini-3.5-flash",
	"gemini-claude-opus-4-6-thinking-low":        "claude-opus-4-6-thinking",
	"gemini-claude-opus-4-6-thinking-medium":     "claude-opus-4-6-thinking",
	"gemini-claude-opus-4-6-thinking-high":       "claude-opus-4-6-thinking",
	"gemini-claude-sonnet-4-6-thinking-low":      "claude-sonnet-4-6",
	"gemini-claude-sonnet-4-6-thinking-medium":   "claude-sonnet-4-6",
	"gemini-claude-sonnet-4-6-thinking-high":     "claude-sonnet-4-6",
	"gemini-claude-sonnet-4-6":                   "claude-sonnet-4-6",
	"claude-sonnet-4-6-thinking":                 "claude-sonnet-4-6",
	"claude-sonnet-4-6-thinking-low":             "claude-sonnet-4-6",
	"claude-sonnet-4-6-thinking-medium":          "claude-sonnet-4-6",
	"claude-sonnet-4-6-thinking-high":            "claude-sonnet-4-6",
}

// OPENCODE_MODEL_DEFINITIONS contains the public Antigravity OpenCode model definitions.
var OPENCODE_MODEL_DEFINITIONS = pickModelDefinitions(antigravityOpenCodeModelIDs)

func pickModelDefinitions(ids []string) OpencodeModelDefinitions {
	definitions := make(OpencodeModelDefinitions, len(ids))
	for _, id := range ids {
		definitions[id] = copyModelDefinition(allModelDefinitions[id])
	}
	return definitions
}

// GetPublicModelDefinitions returns the public Antigravity OpenCode model definitions.
func GetPublicModelDefinitions() OpencodeModelDefinitions {
	return copyModelDefinitions(OPENCODE_MODEL_DEFINITIONS)
}

// GetAntigravityOpencodeModelIds returns the public Antigravity OpenCode model IDs.
func GetAntigravityOpencodeModelIds() []string {
	ids := make([]string, len(antigravityOpenCodeModelIDs))
	copy(ids, antigravityOpenCodeModelIDs)
	return ids
}

// GetResolverAliasMap returns the Antigravity resolver alias map.
func GetResolverAliasMap() map[string]string {
	aliases := make(map[string]string, len(resolverAliases))
	for alias, model := range resolverAliases {
		aliases[alias] = model
	}
	return aliases
}

// GetQuotaGroupForModel is implemented in transform.go to reuse the existing quota registry.
// GetGemini35FlashAntigravityModel is implemented in transform.go to reuse existing Gemini 3.5 Flash routing.
// GetGemini35FlashGeminiCliFallbackModel is implemented in transform.go to reuse existing Gemini CLI fallback routing.

func copyModelDefinitions(definitions OpencodeModelDefinitions) OpencodeModelDefinitions {
	copied := make(OpencodeModelDefinitions, len(definitions))
	for id, definition := range definitions {
		copied[id] = copyModelDefinition(definition)
	}
	return copied
}

func copyModelDefinition(definition OpencodeModelDefinition) OpencodeModelDefinition {
	definition.Modalities = copyModalities(definition.Modalities)
	definition.Options = copyOptions(definition.Options)
	definition.Variants = copyVariants(definition.Variants)
	return definition
}

func copyModalities(modalities ModelModalities) ModelModalities {
	input := make([]ModelModality, len(modalities.Input))
	copy(input, modalities.Input)
	output := make([]ModelModality, len(modalities.Output))
	copy(output, modalities.Output)
	return ModelModalities{Input: input, Output: output}
}

func copyOptions(options map[string]any) map[string]any {
	copied := make(map[string]any, len(options))
	for key, value := range options {
		copied[key] = value
	}
	return copied
}

func copyVariants(variants map[string]ModelVariant) map[string]ModelVariant {
	if variants == nil {
		return nil
	}
	copied := make(map[string]ModelVariant, len(variants))
	for key, value := range variants {
		if value.ThinkingConfig != nil {
			thinkingConfig := *value.ThinkingConfig
			value.ThinkingConfig = &thinkingConfig
		}
		copied[key] = value
	}
	return copied
}
