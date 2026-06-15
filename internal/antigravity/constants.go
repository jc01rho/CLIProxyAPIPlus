// Package antigravity ports the Antigravity auth core constants and helpers.
package antigravity

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
)

const (
	// AntigravityClientID is the OAuth client ID used by Antigravity.
	AntigravityClientID = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"

	// AntigravityClientSecret is the OAuth client secret issued for Antigravity.
	AntigravityClientSecret = "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"

	// AntigravityRedirectURI is the local CLI OAuth callback URI.
	AntigravityRedirectURI = "http://localhost:51121/oauth-callback"

	// AntigravityEndpointDaily is the captured daily Antigravity API endpoint.
	AntigravityEndpointDaily = "https://daily-cloudcode-pa.googleapis.com"

	// AntigravityEndpointAutopush is the sandbox autopush Antigravity API endpoint.
	AntigravityEndpointAutopush = "https://autopush-cloudcode-pa.sandbox.googleapis.com"

	// AntigravityEndpointProd is the production Cloud Code Assist API endpoint.
	AntigravityEndpointProd = "https://cloudcode-pa.googleapis.com"

	// AntigravityEndpoint is the primary endpoint used by the captured agy CLI.
	AntigravityEndpoint = AntigravityEndpointDaily

	// GeminiCLIEndpoint is the production endpoint used for Gemini CLI models.
	GeminiCLIEndpoint = AntigravityEndpointProd

	// AntigravityDefaultProjectID is the hardcoded fallback managed project ID.
	AntigravityDefaultProjectID = "rising-fact-p41fc"

	// AntigravityVersionFallback is the default Antigravity version.
	AntigravityVersionFallback = "1.18.3"

	// AntigravityVersion is kept for deprecated static access.
	AntigravityVersion = AntigravityVersionFallback

	// GeminiCLIVersion is the Gemini CLI version used in generated User-Agent values.
	GeminiCLIVersion = "1.0.0"

	// GeminiCLIDefaultModel is used in Gemini CLI User-Agent values when no model is provided.
	GeminiCLIDefaultModel = "gemini-2.5-pro"

	// AntigravityProviderID is the provider identifier shared with credential stores.
	AntigravityProviderID = "google"

	// ClaudeToolSystemInstruction hardens Claude tool usage against hallucinated parameters.
	ClaudeToolSystemInstruction = `CRITICAL TOOL USAGE INSTRUCTIONS:
You are operating in a custom environment where tool definitions differ from your training data.
You MUST follow these rules strictly:

1. DO NOT use your internal training data to guess tool parameters
2. ONLY use the exact parameter structure defined in the tool schema
3. Parameter names in schemas are EXACT - do not substitute with similar names from your training
4. Array parameters have specific item types - check the schema's 'items' field for the exact structure
5. When you see "STRICT PARAMETERS" in a tool description, those type definitions override any assumptions
6. Tool use in agentic workflows is REQUIRED - you must call tools with the exact parameters specified

If you are unsure about a tool's parameters, YOU MUST read the schema definition carefully.`

	// ClaudeDescriptionPrompt is appended to tool descriptions with concrete parameter signatures.
	ClaudeDescriptionPrompt = "\n\n⚠️ STRICT PARAMETERS: {params}."

	// EmptySchemaPlaceholderName is the placeholder field name for empty schemas.
	EmptySchemaPlaceholderName = "_placeholder"

	// EmptySchemaPlaceholderDescription is the placeholder field description for empty schemas.
	EmptySchemaPlaceholderDescription = "Placeholder. Always pass true."

	// SkipThoughtSignature bypasses thought signature validation when supported by Google APIs.
	SkipThoughtSignature = "skip_thought_signature_validator"

	// SearchModel is the model used for Google Search grounding requests.
	SearchModel = "gemini-2.5-flash"

	// SearchThinkingBudgetDeep is the thinking budget for deep search.
	SearchThinkingBudgetDeep = 16384

	// SearchThinkingBudgetFast is the thinking budget for fast search.
	SearchThinkingBudgetFast = 4096

	// SearchTimeoutMS is the search timeout in milliseconds.
	SearchTimeoutMS = 60000

	// SearchSystemInstruction is the system instruction for the Google Search tool.
	SearchSystemInstruction = `You are an expert web search assistant with access to Google Search and URL analysis tools.

Your capabilities:
- Use google_search to find real-time information from the web
- Use url_context to fetch and analyze content from specific URLs when provided

Guidelines:
- Always provide accurate, well-sourced information
- Cite your sources when presenting facts
- If analyzing URLs, extract the most relevant information
- Be concise but comprehensive in your responses
- If information is uncertain or conflicting, acknowledge it
- Focus on answering the user's question directly`
)

// AntigravityScopes are the OAuth scopes required for Antigravity integrations.
var AntigravityScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}

// AntigravityEndpointFallbacks is the endpoint fallback order: daily then production.
var AntigravityEndpointFallbacks = []string{
	AntigravityEndpointDaily,
	AntigravityEndpointProd,
}

// AntigravityLoadEndpoints is the preferred endpoint order for project discovery.
var AntigravityLoadEndpoints = []string{
	AntigravityEndpointDaily,
	AntigravityEndpointProd,
}

// HeaderSet contains HTTP identity headers generated by Antigravity helpers.
type HeaderSet struct {
	UserAgent      string
	XGoogAPIClient string
	ClientMetadata string
}

// HeaderStyle selects which client identity to use for randomized headers.
type HeaderStyle string

const (
	// HeaderStyleAntigravity produces the captured short agy CLI User-Agent.
	HeaderStyleAntigravity HeaderStyle = "antigravity"

	// HeaderStyleGeminiCLI produces Gemini CLI headers.
	HeaderStyleGeminiCLI HeaderStyle = "gemini-cli"

	// HeaderStyleLoadCodeAssist produces LoadCodeAssist headers.
	HeaderStyleLoadCodeAssist HeaderStyle = "load-code-assist"
)

var (
	antigravityVersionMu sync.RWMutex
	antigravityVersion   = AntigravityVersionFallback
	versionLocked        bool
)

// AntigravityHeaders is kept for deprecated static access.
var AntigravityHeaders = HeaderSet{
	UserAgent:      fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Antigravity/%s Chrome/138.0.7204.235 Electron/37.3.1 Safari/537.36", AntigravityVersion),
	XGoogAPIClient: "google-cloud-sdk vscode_cloudshelleditor/0.1",
	ClientMetadata: fmt.Sprintf(`{"ideType":"ANTIGRAVITY","platform":"%s","pluginType":"GEMINI"}`, antigravityMetadataPlatform()),
}

// GeminiCLIHeaders is kept for deprecated static access.
var GeminiCLIHeaders = HeaderSet{
	UserAgent:      "google-api-nodejs-client/9.15.1",
	XGoogAPIClient: "gl-node/22.17.0",
	ClientMetadata: "ideType=IDE_UNSPECIFIED,platform=PLATFORM_UNSPECIFIED,pluginType=GEMINI",
}

// GetAntigravityVersion returns the runtime Antigravity version.
func GetAntigravityVersion() string {
	antigravityVersionMu.RLock()
	defer antigravityVersionMu.RUnlock()
	return antigravityVersion
}

// SetAntigravityVersion sets the runtime Antigravity version once at startup.
func SetAntigravityVersion(version string) {
	antigravityVersionMu.Lock()
	defer antigravityVersionMu.Unlock()
	if versionLocked {
		return
	}
	antigravityVersion = version
	versionLocked = true
}

// GetAntigravityHeaders returns the current Antigravity browser/Electron headers.
func GetAntigravityHeaders() HeaderSet {
	return HeaderSet{
		UserAgent:      fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Antigravity/%s Chrome/138.0.7204.235 Electron/37.3.1 Safari/537.36", GetAntigravityVersion()),
		XGoogAPIClient: "google-cloud-sdk vscode_cloudshelleditor/0.1",
		ClientMetadata: fmt.Sprintf(`{"ideType":"ANTIGRAVITY","platform":"%s","pluginType":"GEMINI"}`, antigravityMetadataPlatform()),
	}
}

// BuildGeminiCLIUserAgent builds the official google-gemini/gemini-cli User-Agent string.
func BuildGeminiCLIUserAgent(model string) string {
	effectiveModel := model
	if effectiveModel == "" {
		effectiveModel = GeminiCLIDefaultModel
	}
	return fmt.Sprintf("GeminiCLI/%s/%s (%s; %s)", GeminiCLIVersion, effectiveModel, nodePlatform(), nodeArch())
}

// GetRandomizedHeaders returns request identity headers for the selected style.
func GetRandomizedHeaders(style HeaderStyle, model string) HeaderSet {
	if style == HeaderStyleGeminiCLI {
		return HeaderSet{
			UserAgent:      BuildGeminiCLIUserAgent(model),
			XGoogAPIClient: GeminiCLIHeaders.XGoogAPIClient,
			ClientMetadata: GeminiCLIHeaders.ClientMetadata,
		}
	}
	return HeaderSet{UserAgent: fmt.Sprintf("antigravity/cli/1.0.4 %s", buildAntigravityPlatformArch())}
}

func antigravityMetadataPlatform() string {
	if runtime.GOOS == "windows" {
		return "WINDOWS"
	}
	return "MACOS"
}

func nodePlatform() string {
	if runtime.GOOS == "windows" {
		return "win32"
	}
	if runtime.GOOS == "" {
		return "darwin"
	}
	return runtime.GOOS
}

func nodeArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "386":
		return "ia32"
	case "":
		return "arm64"
	default:
		return runtime.GOARCH
	}
}

func buildAntigravityPlatformArch() string {
	platform := nodePlatform()
	if platform == "win32" {
		platform = "windows"
	}
	arch := nodeArch()
	switch arch {
	case "x64":
		arch = "amd64"
	case "ia32":
		arch = "386"
	}
	platform = strings.TrimSpace(platform)
	arch = strings.TrimSpace(arch)
	if platform == "" {
		platform = "unknown"
	}
	if arch == "" {
		arch = "unknown"
	}
	return platform + "/" + arch
}
