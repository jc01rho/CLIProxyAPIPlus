// Package constant defines provider name constants used throughout the CLI Proxy API.
// These constants identify different AI service providers and their variants,
// ensuring consistent naming across the application.
package constant

const (
	// Gemini represents the Google Gemini provider identifier.
	Gemini = "gemini"

	// GeminiInteractions represents the native Google Interactions API provider identifier.
	GeminiInteractions = "gemini-interactions"

	// Codex represents the OpenAI Codex provider identifier.
	Codex = "codex"

	// Claude represents the Anthropic Claude provider identifier.
	Claude = "claude"

	// OpenAI represents the OpenAI provider identifier.
	OpenAI = "openai"

	// OpenaiResponse represents the OpenAI response format identifier.
	OpenaiResponse = "openai-response"

	// Antigravity represents the Antigravity response format identifier.
	Antigravity = "antigravity"

	// Kiro represents the AWS CodeWhisperer (Kiro) provider identifier.
	Kiro = "kiro"

	// Kilo represents the Kilo AI provider identifier.
	Kilo = "kilo"

	// Cline represents the Cline AI provider identifier.
	Cline = "cline"

	// CommandCode represents the Command Code provider identifier.
	CommandCode = "commandcode"

	// Mistral represents the Mistral AI provider identifier.
	Mistral = "mistral"

	// Interactions represents the Google Interactions API format identifier.
	Interactions = "interactions"
)
