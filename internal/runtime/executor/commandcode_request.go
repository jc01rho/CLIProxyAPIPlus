// commandcode_request.go converts a normalized OpenAI chat-completions payload
// into the CommandCode /alpha/generate wire envelope of command-code@1.12.0.
// The payload is parsed once into typed values at this boundary; conversion
// never passes map[string]any across it, and unsupported shapes fail instead
// of being silently flattened.

package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	// commandCodeDefaultMaxTokens mirrors the npm default output token limit (64e3).
	commandCodeDefaultMaxTokens = 64000
	commandCodePermissionMode   = "standard"
	// commandCodeMaxCallIDLen is the upstream CommandCode limit on tool-call/
	// tool-result "toolCallId" wire field length. IDs longer than this are
	// rejected by CommandCode with "input[N].call_id ... must be <= 64".
	commandCodeMaxCallIDLen = 64
)

// commandCodeNormalizeCallID returns id unchanged when it already fits the
// upstream CommandCode call-id length limit. Longer IDs are replaced with a
// deterministic 64-char lowercase hex sha256 digest of the original value, so
// the same raw ID always normalizes to the same wire value independently at
// every call site — no shared remap table is needed to keep an assistant
// tool-call and its paired tool-result message consistent on the wire.
// Empty IDs are returned unchanged; callers validate emptiness separately.
func commandCodeNormalizeCallID(id string) string {
	if len(id) <= commandCodeMaxCallIDLen {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

// --- OpenAI input types ---

type commandCodeOpenAIRequest struct {
	Model           string                     `json:"model"`
	Messages        []commandCodeOpenAIMessage `json:"messages"`
	Tools           []commandCodeOpenAITool    `json:"tools"`
	MaxTokens       int64                      `json:"max_tokens"`
	Temperature     *float64                   `json:"temperature"`
	ReasoningEffort string                     `json:"reasoning_effort"`
}

type commandCodeOpenAIMessage struct {
	Role       string                      `json:"role"`
	Content    json.RawMessage             `json:"content"`
	ToolCalls  []commandCodeOpenAIToolCall `json:"tool_calls"`
	ToolCallID string                      `json:"tool_call_id"`
}

type commandCodeOpenAIContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url"`
}

type commandCodeOpenAIToolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type commandCodeOpenAITool struct {
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

// --- CommandCode wire types ---

// commandCodeWireContentBlock is the sealed union of typed content blocks the
// CommandCode wire accepts: text, reasoning, image, tool-call, tool-result.
type commandCodeWireContentBlock interface{ commandCodeWireBlock() }

// commandCodeWireTextBlock carries both "text" and "reasoning" blocks.
type commandCodeWireTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type commandCodeWireImageBlock struct {
	Type     string `json:"type"`
	Image    string `json:"image"`
	MimeType string `json:"mimeType"`
}

type commandCodeWireToolCallBlock struct {
	Type       string          `json:"type"`
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Input      json.RawMessage `json:"input"`
}

type commandCodeWireToolResultBlock struct {
	Type       string                    `json:"type"`
	ToolCallID string                    `json:"toolCallId"`
	ToolName   string                    `json:"toolName"`
	Output     commandCodeWireToolOutput `json:"output"`
}

type commandCodeWireToolOutput struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

func (commandCodeWireTextBlock) commandCodeWireBlock()       {}
func (commandCodeWireImageBlock) commandCodeWireBlock()      {}
func (commandCodeWireToolCallBlock) commandCodeWireBlock()   {}
func (commandCodeWireToolResultBlock) commandCodeWireBlock() {}

type commandCodeWireMessage struct {
	Role    string                        `json:"role"`
	Content []commandCodeWireContentBlock `json:"content"`
}

type commandCodeWireTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type commandCodeWireParams struct {
	Model           string                   `json:"model"`
	Messages        []commandCodeWireMessage `json:"messages"`
	Tools           []commandCodeWireTool    `json:"tools,omitempty"`
	System          string                   `json:"system,omitempty"`
	MaxTokens       int64                    `json:"max_tokens"`
	Stream          bool                     `json:"stream"`
	Temperature     *float64                 `json:"temperature,omitempty"`
	ReasoningEffort string                   `json:"reasoning_effort,omitempty"`
}

type commandCodeWireConfig struct {
	WorkingDir    string   `json:"workingDir"`
	Date          string   `json:"date"`
	Environment   string   `json:"environment"`
	Structure     []string `json:"structure"`
	IsGitRepo     bool     `json:"isGitRepo"`
	CurrentBranch string   `json:"currentBranch"`
	MainBranch    string   `json:"mainBranch"`
	GitStatus     string   `json:"gitStatus"`
	RecentCommits []string `json:"recentCommits"`
}

type commandCodeWireEnvelope struct {
	Config         commandCodeWireConfig `json:"config"`
	Memory         json.RawMessage       `json:"memory"`
	Taste          json.RawMessage       `json:"taste"`
	Skills         json.RawMessage       `json:"skills"`
	PermissionMode string                `json:"permissionMode"`
	Params         commandCodeWireParams `json:"params"`
}

// buildCommandCodePayload constructs the CommandCode envelope from an OpenAI-format payload.
// System/developer messages are extracted into params.system; tools and tool-related
// messages are converted to the typed CommandCode wire shapes.
func buildCommandCodePayload(openAIPayload []byte, model string, stream bool) ([]byte, error) {
	var request commandCodeOpenAIRequest
	if err := json.Unmarshal(openAIPayload, &request); err != nil {
		return nil, fmt.Errorf("commandcode: parse openai payload: %w", err)
	}
	if model == "" {
		model = request.Model
	}

	// Build the tool call ID -> tool name map before converting messages so
	// tool-result blocks can resolve their tool name ("" when unknown, matching npm).
	// Tool calls with an empty function name are dropped from the assistant wire
	// message; their IDs are tracked so the matching tool-result messages are
	// dropped too — otherwise upstream rejects the orphaned result with
	// "No function call found for function call output with call_id ...".
	toolNames := make(map[string]string)
	skippedToolCallIDs := make(map[string]bool)
	for _, msg := range request.Messages {
		for _, call := range msg.ToolCalls {
			if call.ID == "" {
				continue
			}
			if call.Function.Name == "" {
				skippedToolCallIDs[call.ID] = true
				continue
			}
			toolNames[call.ID] = call.Function.Name
		}
	}

	messages := make([]commandCodeWireMessage, 0, len(request.Messages))
	var systemParts []string
	for _, msg := range request.Messages {
		switch msg.Role {
		case "system", "developer":
			text, err := commandCodeTextContent(msg)
			if err != nil {
				return nil, err
			}
			if text != "" {
				systemParts = append(systemParts, text)
			}
		case "user":
			wire, err := commandCodeUserMessage(msg)
			if err != nil {
				return nil, err
			}
			messages = append(messages, wire)
		case "assistant":
			wire, err := commandCodeAssistantMessage(msg)
			if err != nil {
				return nil, err
			}
			// A malformed assistant message can end up with no content blocks
			// (e.g. every tool call had an empty name and no text) — skip it
			// rather than sending an empty-content message upstream.
			if len(wire.Content) == 0 {
				continue
			}
			messages = append(messages, wire)
		case "tool":
			if skippedToolCallIDs[msg.ToolCallID] {
				continue
			}
			wire, err := commandCodeToolMessage(msg, toolNames)
			if err != nil {
				return nil, err
			}
			messages = append(messages, wire)
		default:
			return nil, fmt.Errorf("commandcode: unsupported message role %q", msg.Role)
		}
	}

	maxTokens := request.MaxTokens
	if maxTokens == 0 {
		maxTokens = commandCodeDefaultMaxTokens
	}
	envelope := commandCodeWireEnvelope{
		Config: commandCodeWireConfig{
			WorkingDir:    "/tmp",
			Date:          time.Now().Format("2006-01-02"),
			Environment:   "terminal",
			Structure:     []string{},
			IsGitRepo:     false,
			CurrentBranch: "",
			MainBranch:    "",
			GitStatus:     "",
			RecentCommits: []string{},
		},
		PermissionMode: commandCodePermissionMode,
		Params: commandCodeWireParams{
			Model:       model,
			Messages:    messages,
			Tools:       commandCodeConvertTools(request.Tools),
			System:      strings.Join(systemParts, "\n"),
			MaxTokens:   maxTokens,
			Stream:      stream,
			Temperature: request.Temperature,
			// Forward reasoning_effort only when the resolved model documents the
			// requested effort; unsupported values are omitted so the upstream never
			// sees a field it rejects (mirrors opencodex's supportedCommandCodeEffort).
			ReasoningEffort: commandCodeSupportedEffort(model, request.ReasoningEffort),
		},
	}
	return json.Marshal(envelope)
}
