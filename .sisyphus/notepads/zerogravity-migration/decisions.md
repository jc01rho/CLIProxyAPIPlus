# Decisions

## 2026-02-20 Static Model Registration
- Decision: Replace dynamic FetchAntigravityModels (requires running LS) with static GetAntigravityModels()
- Rationale: ZeroGravity uses static model defs; other providers use static lists; LS may not be ready during registration
- Model IDs: Keep existing upstream model names (gemini-3-flash, claude-opus-4-6-thinking, etc.) for translator compatibility
- Also add ZeroGravity aliases (opus-4.6, sonnet-4.6, gemini-3.1-pro-high) as additional models
- Protobuf enum values stored in ModelInfo.Metadata for future LS communication
