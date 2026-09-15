package helps

import "strings"

// Cursor argument-key normalization (ported from opencodex/src/adapters/cursor/arg-normalize.ts).
//
// Cursor-trained models sometimes emit argument keys that differ from the tool's declared schema
// (e.g. `filepath` instead of `path`, `cmd` instead of `command`). This module maps common
// aliases to the canonical key name ONLY when the tool schema declares that canonical key.
// Keys already matching the schema are never touched.
//
// Mirrors `opencode-cursor/src/provider/tool-schema-compat.ts` per the opencodex header comment.

// cursorKeyAliases holds the lowercase alias -> canonical-name mapping. Lookup is case
// insensitive (callers lowercase the key before lookup). Keys are added to the map once.
var cursorKeyAliases = map[string]string{
	"filepath":         "path",
	"filename":         "path",
	"file":             "path",
	"targetpath":       "path",
	"directorypath":    "path",
	"dir":              "path",
	"folder":           "path",
	"directory":        "path",
	"targetdirectory":  "path",
	"targetfile":       "path",
	"globpattern":      "pattern",
	"filepattern":      "pattern",
	"searchpattern":    "pattern",
	"includepattern":   "include",
	"workingdirectory": "cwd",
	"workdir":          "cwd",
	"currentdirectory": "cwd",
	"cmd":              "command",
	"script":           "command",
	"shellcommand":     "command",
	"terminalcommand":  "command",
	"contents":         "content",
	"text":             "content",
	"body":             "content",
	"data":             "content",
	"payload":          "content",
	"streamcontent":    "content",
	"oldstring":        "old_string",
	"newstring":        "new_string",
	"oldtext":          "old_string",
	"newtext":          "new_string",
	"oldcontent":       "old_string",
	"newcontent":       "new_string",
	"recursive":        "force",
}

// schemaPropertyNames extracts the set of declared property names from a JSON Schema
// parameters object of the common `{ type: "object", properties: { ... } }` shape.
func cursorSchemaPropertyNames(schema interface{}) map[string]struct{} {
	if schema == nil {
		return nil
	}
	m, ok := schema.(map[string]interface{})
	if !ok {
		return nil
	}
	props, ok := m["properties"].(map[string]interface{})
	if !ok {
		return nil
	}
	out := make(map[string]struct{}, len(props))
	for k := range props {
		out[k] = struct{}{}
	}
	return out
}

// NormalizeCursorArgKeys rewrites args to use schema-declared canonical keys when the original
// key has a known alias. Keys already matching the schema are preserved. Conflicting aliases
// (same canonical key already supplied explicitly) drop the alias entirely rather than
// creating a duplicate. Returns the original map reference if no changes were made so callers
// can skip downstream work on the common pass-through path.
func NormalizeCursorArgKeys(args map[string]interface{}, schema interface{}) map[string]interface{} {
	declared := cursorSchemaPropertyNames(schema)
	if len(declared) == 0 {
		return args
	}
	suppliedCanonical := make(map[string]struct{}, len(args))
	for k := range args {
		if _, ok := declared[k]; ok {
			suppliedCanonical[k] = struct{}{}
		}
	}
	changed := false
	result := make(map[string]interface{}, len(args))
	for k, v := range args {
		if _, ok := declared[k]; ok {
			result[k] = v
			continue
		}
		canonical, ok := cursorKeyAliases[strings.ToLower(k)]
		if !ok || !declaredContains(declared, canonical) {
			result[k] = v
			continue
		}
		// Canonical was explicitly supplied (any order) — drop the alias entirely.
		if _, dup := suppliedCanonical[canonical]; dup {
			changed = true
			continue
		}
		// First alias fills the canonical slot; later aliases for the same canonical are dropped.
		if _, dup := result[canonical]; dup {
			changed = true
			continue
		}
		result[canonical] = v
		changed = true
	}
	if !changed {
		return args
	}
	return result
}

func declaredContains(set map[string]struct{}, name string) bool {
	_, ok := set[name]
	return ok
}
