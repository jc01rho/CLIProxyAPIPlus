package helps

// Cursor tool-name canonicalization primitives (ported from
// opencodex/src/adapters/cursor/tool-naming.ts).
//
// The Cursor wire advertises a private namespace of bare tool names (its own structured-edit
// tools, Codex execution-path aliases, etc.). When a client catalog uses one of those bare
// names, we route it under a proxy-owned prefix to keep the wire shape unambiguous; client-
// supplied bare names that don't collide are forwarded verbatim under a different reserved
// prefix.
//
// CPA usage note: the wire format is JSON, not bufbuild/protobuf. The TypeScript source used
// oneof casing; here the prefix set is the source of truth and lookup helpers are pure
// functions.

const (
	// CursorClientToolWirePrefix is the reserved prefix for client-advertised bare tool
	// names that are NOT in the proxy-owned set. Mirrors `CURSOR_CLIENT_TOOL_WIRE_PREFIX =
	// "ocx_client_"` in opencodex.
	CursorClientToolWirePrefix = "ocx_client_"

	// CursorProxyOwnedBareToolNames enumerates the bare tool names the proxy itself claims on
	// the Cursor wire. Matches `CURSOR_PROXY_OWNED_BARE_TOOL_NAMES` in opencodex.
	CursorProxyOwnedBareToolNames = "exec|wait|exec_command|shell_command|apply_patch|edit_file|multi_edit|tool_search"
)

// CursorProxyOwnedBareToolNameSet is a membership lookup for CursorProxyOwnedBareToolNames.
// Plain map[string]struct{} so a single caller can hot-test many candidate names without
// rebuilding the table.
var CursorProxyOwnedBareToolNameSet = func() map[string]struct{} {
	m := make(map[string]struct{}, 16)
	for _, name := range splitPipeList(CursorProxyOwnedBareToolNames) {
		m[name] = struct{}{}
	}
	return m
}()

// IsCursorProxyOwnedBareToolName reports whether the given bare tool name is claimed by the
// proxy on the wire. Namespaced tools (mcp__*) and unknown bare names return false.
func IsCursorProxyOwnedBareToolName(name string) bool {
	if name == "" {
		return false
	}
	_, ok := CursorProxyOwnedBareToolNameSet[name]
	return ok
}

// IsCursorBareClientToolWireAliased reports whether a client-advertised bare tool name MUST be
// routed under the client prefix to avoid colliding with Cursor's private namespace. The
// condition mirrors opencodex: bare (no namespace) AND not in the proxy-owned set.
func IsCursorBareClientToolWireAliased(namespace, name string) bool {
	if namespace != "" {
		return false
	}
	if name == "" {
		return false
	}
	return !IsCursorProxyOwnedBareToolName(name)
}

// WireNameForCursorClientTool returns the wire name Cursor receives. Namespaced tools are
// passed through (the wire already disambiguates by namespace). Proxy-owned bare names are
// passed through (the proxy's prefix already identifies them). Other bare names are rewritten
// under the client prefix.
func WireNameForCursorClientTool(namespace, name string) string {
	if namespace != "" {
		return name
	}
	if !IsCursorBareClientToolWireAliased(namespace, name) {
		return name
	}
	return CursorClientToolWirePrefix + name
}

// splitPipeList splits a literal pipe-separated token list into its constituent tokens,
// trimming ASCII whitespace around each token and skipping empty tokens.
func splitPipeList(s string) []string {
	if s == "" {
		return nil
	}
	out := make([]string, 0, 8)
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '|' {
			continue
		}
		if tok := trimASCIIWS(s[start:i]); tok != "" {
			out = append(out, tok)
		}
		start = i + 1
	}
	if tok := trimASCIIWS(s[start:]); tok != "" {
		out = append(out, tok)
	}
	return out
}

// trimASCIIWS strips spaces and tabs (matches JS-style trim across the token list).
func trimASCIIWS(s string) string {
	start, end := 0, len(s)
	for start < end {
		c := s[start]
		if c != ' ' && c != '\t' {
			break
		}
		start++
	}
	for end > start {
		c := s[end-1]
		if c != ' ' && c != '\t' {
			break
		}
		end--
	}
	return s[start:end]
}
