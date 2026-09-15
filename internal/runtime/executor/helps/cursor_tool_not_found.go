package helps

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Cursor typed tool-not-found payload (ported from opencodex/src/adapters/cursor/native-exec-mcp.ts).
//
// opencodex returns a typed `McpToolNotFoundSchema` carrying the requested name AND the list of
// available tools, so the model can correct its call rather than retry into a dead end.
// CPA's wire is JSON (not protobuf), so we encode the equivalent JSON shape and let the
// caller marshal it onto the Cursor response. The signature mirrors the protobuf oneof
// contract: result === { case: "toolNotFound", value: { name, availableTools } }.

// CursorToolNotFound is the typed payload Cursor receives when a tool call resolves to "no
// such tool". Keep the JSON keys stable — they are part of the wire contract.
type CursorToolNotFound struct {
	Name           string   `json:"name"`
	AvailableTools []string `json:"availableTools"`
}

// BuildCursorToolNotFound constructs a typed payload with a sorted, de-duplicated available
// tools list. Sorting keeps diffs stable across requests so log analytics don't churn on
// catalog reshuffles.
func BuildCursorToolNotFound(name string, availableTools []string) CursorToolNotFound {
	seen := make(map[string]struct{}, len(availableTools))
	dedup := make([]string, 0, len(availableTools))
	for _, t := range availableTools {
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		dedup = append(dedup, t)
	}
	sort.Strings(dedup)
	return CursorToolNotFound{Name: name, AvailableTools: dedup}
}

// MarshalCursorToolNotFound renders the typed payload as a stable JSON object. Returned as a
// map so callers can splat it into a larger envelope without re-encoding.
func MarshalCursorToolNotFound(payload CursorToolNotFound) (map[string]interface{}, error) {
	out := map[string]interface{}{
		"name": payload.Name,
	}
	if len(payload.AvailableTools) == 0 {
		// Explicit empty list — never omit, the field is part of the contract.
		out["availableTools"] = []string{}
	} else {
		out["availableTools"] = payload.AvailableTools
	}
	return out, nil
}

// CursorToolNotFoundJSON is a convenience wrapper for callers that want bytes directly. Used
// in tests and in JSON-stream envelopes; non-stream callers should use MarshalCursorToolNotFound.
func CursorToolNotFoundJSON(name string, availableTools []string) ([]byte, error) {
	payload := BuildCursorToolNotFound(name, availableTools)
	encoded, err := MarshalCursorToolNotFound(payload)
	if err != nil {
		return nil, fmt.Errorf("cursor: marshal tool-not-found: %w", err)
	}
	return json.Marshal(encoded)
}
