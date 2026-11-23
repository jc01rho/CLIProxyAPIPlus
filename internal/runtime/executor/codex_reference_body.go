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
