package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	neturl "net/url"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// The helpers below reshape a ChatGPT-backend Responses body the way
// cortexkit/openai-auth prepareCodexRequest (codex-request.ts) does.

const (
	codexInstallationIDMetadataKey = "x-codex-installation-id"
	codexWindowIDMetadataKey       = "x-codex-window-id"
)

// codexInstallationID derives a stable per-credential installation UUID; native
// Codex persists one per install and CPA has no install to persist it for.
func codexInstallationID(authID string) string {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return ""
	}
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:installation:"+authID)).String()
}

// normalizeCodexFunctionToolRaw mirrors normalizeCodexTool: keys come out as
// type, name, description, strict, parameters, then third-party extras; strict
// defaults to false and parameters loses a top-level $schema.
func normalizeCodexFunctionToolRaw(tool ojson.Result) string {
	var out bytes.Buffer
	out.WriteByte('{')
	first := true
	write := func(key, raw string) {
		if !first {
			out.WriteByte(',')
		}
		first = false
		encodedKey, _ := json.Marshal(key)
		out.Write(encodedKey)
		out.WriteByte(':')
		out.WriteString(raw)
	}
	for _, key := range []string{"type", "name", "description"} {
		if value := tool.Get(key); value.Exists() {
			write(key, value.Raw)
		}
	}
	if strict := tool.Get("strict"); strict.Exists() && strict.Type != ojson.Null {
		write("strict", strict.Raw)
	} else {
		write("strict", "false")
	}
	if parameters := tool.Get("parameters"); parameters.Exists() {
		raw := parameters.Raw
		if parameters.IsObject() && parameters.Get("$schema").Exists() {
			stripped := ojson.NewDoc([]byte(raw))
			if stripped.Delete("$schema") == nil {
				raw = string(stripped.Bytes())
			}
		}
		write("parameters", raw)
	}
	tool.ForEach(func(key, value ojson.Result) bool {
		switch key.String() {
		case "type", "name", "description", "strict", "parameters":
			return true
		}
		write(key.String(), value.Raw)
		return true
	})
	out.WriteByte('}')
	return out.String()
}

func normalizeCodexFunctionToolsDoc(doc *ojson.Doc) {
	tools := doc.Get("tools")
	if !tools.IsArray() {
		return
	}
	items := make([]string, 0, len(tools.Array()))
	changed := false
	tools.ForEach(func(_, tool ojson.Result) bool {
		raw := tool.Raw
		if tool.Get("type").String() == "function" {
			raw = normalizeCodexFunctionToolRaw(tool)
		}
		if raw != tool.Raw {
			changed = true
		}
		items = append(items, raw)
		return true
	})
	if changed {
		_ = doc.SetRawBytes("tools", []byte("["+strings.Join(items, ",")+"]"))
	}
}

// applyCodexReferenceBodyDoc applies the body edits prepareCodexRequest makes
// on every ChatGPT-backend request: function tools normalized, parallel_tool_calls defaulted to true (a Responses Lite request
// keeps the value its own rewrite chose) and client_metadata carrying the
// installation and window ids after any caller keys.
func applyCodexReferenceBodyDoc(doc *ojson.Doc, installationID, windowID string, responsesLite bool) {
	if doc == nil {
		return
	}
	normalizeCodexFunctionToolsDoc(doc)
	if parallel := doc.Get("parallel_tool_calls"); !responsesLite && (!parallel.Exists() || parallel.Type == ojson.Null) {
		_ = doc.Set("parallel_tool_calls", true)
	}
	if existing := doc.Get("client_metadata"); existing.Exists() && !existing.IsObject() {
		_ = doc.Delete("client_metadata")
	}
	if installationID != "" {
		_ = doc.Set("client_metadata."+codexInstallationIDMetadataKey, installationID)
	}
	if windowID != "" {
		_ = doc.Set("client_metadata."+codexWindowIDMetadataKey, windowID)
	}
}

// codexBaseInstructionsMessageID mirrors baseInstructionsId in cortexkit/openai-auth
// codex-instructions.ts: a v5 UUID of the instructions text inside a v5 UUID of
// the thread id, both under the OID namespace.
func codexBaseInstructionsMessageID(threadID, text string) string {
	thread := uuid.NewSHA1(uuid.NameSpaceOID, []byte(threadID))
	return "msg_" + uuid.NewSHA1(thread, []byte(text)).String()
}

func codexJSONString(text string) string {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(text)
	return strings.TrimSuffix(buf.String(), "\n")
}

// moveCodexInstructionsToDeveloperMessageDoc mirrors moveInstructionsToDeveloperMessage:
// non-empty string instructions become a developer message at the front of the
// input (after a leading additional_tools item), carrying the base-instructions
// content kind, and the top-level field is removed. A string input becomes a user
// message first. The message id is derived from prompt_cache_key when it is a string.
func moveCodexInstructionsToDeveloperMessageDoc(doc *ojson.Doc) {
	if doc == nil {
		return
	}
	instructions := doc.Get("instructions")
	if !instructions.Exists() || instructions.Type != ojson.String || instructions.String() == "" {
		return
	}
	text := instructions.String()
	var items []string
	input := doc.Get("input")
	switch {
	case input.IsArray():
		input.ForEach(func(_, item ojson.Result) bool {
			items = append(items, item.Raw)
			return true
		})
	case input.Type == ojson.String:
		items = append(items, `{"type":"message","role":"user","content":[{"type":"input_text","text":`+codexJSONString(input.String())+`}]}`)
	}
	at := 0
	if len(items) > 0 && ojson.GetBytes([]byte(items[0]), "type").String() == "additional_tools" {
		at = 1
	}
	var message strings.Builder
	message.WriteString(`{"type":"message",`)
	if threadID := doc.Get("prompt_cache_key"); threadID.Type == ojson.String {
		message.WriteString(`"id":` + codexJSONString(codexBaseInstructionsMessageID(threadID.String(), text)) + `,`)
	}
	message.WriteString(`"role":"developer","content":[{"type":"input_text","text":` + codexJSONString(text) + `}],`)
	message.WriteString(`"internal_chat_message_metadata_passthrough":{"content_item_kinds":["model.base_instructions"]}}`)
	merged := make([]string, 0, len(items)+1)
	merged = append(merged, items[:at]...)
	merged = append(merged, message.String())
	merged = append(merged, items[at:]...)
	if doc.SetRaw("input", "["+strings.Join(merged, ",")+"]") != nil {
		return
	}
	_ = doc.Delete("instructions")
}

// codexUsesReferenceBody limits the reference body shape to ChatGPT-backend
// OAuth requests: API-key, SIWC and non-ChatGPT upstreams reject or ignore the
// Codex-only fields, and the compact endpoint takes a different body.
func codexUsesReferenceBody(ctx context.Context, auth *cliproxyauth.Auth, headers http.Header, rawURL string) bool {
	if auth == nil || codexAuthUsesAPIKey(auth) || codexUseSIWCForRequest(ctx, auth, headers) {
		return false
	}
	parsed, err := neturl.Parse(rawURL)
	if err != nil || !strings.EqualFold(parsed.Hostname(), "chatgpt.com") || !strings.HasPrefix(parsed.Path, "/backend-api/codex/") {
		return false
	}
	return !strings.HasSuffix(strings.TrimSuffix(parsed.Path, "/"), "/compact")
}
