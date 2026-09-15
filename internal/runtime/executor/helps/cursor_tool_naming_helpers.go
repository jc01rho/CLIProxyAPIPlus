package helps

import (
	"strings"
)

// Cursor wire ↔ Responses tool-name helpers (ported from opencodex/src/adapters/cursor/tool-naming.ts).
//
// These helpers complement WireNameForCursorClientTool from cursor_tool_names.go: that one
// focuses on prefix collision avoidance for client-advertised bare tools; the helpers below
// cover the round-trip between Cursor wire names and the upstream Responses/Codex catalog
// names that the model actually wrote.

// CursorNormalizeWireName strips known wire prefixes so the underlying bare tool name can be
// compared against the upstream catalog. Matches opencodex normalizeCursorWireName.
func CursorNormalizeWireName(name string) string {
	switch {
	case strings.HasPrefix(name, CursorClientToolWirePrefix):
		return strings.TrimPrefix(name, CursorClientToolWirePrefix)
	default:
		return name
	}
}

// CursorResponsesToolNameFromWire reverses CursorNormalizeWireName. The Responses catalog never
// sees the wire prefix; the wire prefix is a Cursor-protocol concern only. Matches opencodex
// responsesToolNameFromCursorWire.
func CursorResponsesToolNameFromWire(name string) string {
	return CursorNormalizeWireName(name)
}

// CursorIsCursorTextToolMarker reports whether name is one of the historical marker strings
// that some Cursor-trained models emit as text-tool calls. Mirrors
// opencodex normalizeCursorTextToolMarkers — empty for now, kept as a documented seam for the
// future port of `normalizeCursorTextToolMarkers`.
func CursorIsCursorTextToolMarker(name string) bool {
	// Reserved for future expansion; today every "cursor_*" tool in the wire is recognized
	// either by its namespace or by the proxy-owned bare set.
	_ = name
	return false
}
