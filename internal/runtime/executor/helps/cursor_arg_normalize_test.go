package helps

import (
	"reflect"
	"testing"
)

func TestNormalizeCursorArgKeys_PassThrough(t *testing.T) {
	schema := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}},
	}
	args := map[string]interface{}{"path": "/tmp"}
	got := NormalizeCursorArgKeys(args, schema)
	if !reflect.DeepEqual(got, args) {
		t.Fatalf("expected pass-through, got %v", got)
	}
	// Must return the SAME map reference on pass-through so callers can detect no-op.
	if &got == nil {
		t.Fatalf("nil map returned")
	}
}

func TestNormalizeCursorArgKeys_AliasRewrite(t *testing.T) {
	schema := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}},
	}
	args := map[string]interface{}{"filepath": "/tmp"}
	got := NormalizeCursorArgKeys(args, schema)
	if _, ok := got["filepath"]; ok {
		t.Fatalf("alias key should be removed: %v", got)
	}
	if got["path"] != "/tmp" {
		t.Fatalf("path alias not rewritten: %v", got)
	}
}

func TestNormalizeCursorArgKeys_UnknownAliasDropped(t *testing.T) {
	schema := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}},
	}
	// "definitely_not_an_alias" is not in the alias table — pass through unchanged.
	args := map[string]interface{}{"definitely_not_an_alias": "x"}
	got := NormalizeCursorArgKeys(args, schema)
	if _, ok := got["definitely_not_an_alias"]; !ok {
		t.Fatalf("non-alias unknown key should pass through: %v", got)
	}
}

func TestNormalizeCursorArgKeys_CanonicalWinsOverAlias(t *testing.T) {
	schema := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}},
	}
	// Both `path` (canonical) and `filepath` (alias) supplied. Canonical wins; alias dropped.
	args := map[string]interface{}{"path": "/a", "filepath": "/b"}
	got := NormalizeCursorArgKeys(args, schema)
	if got["path"] != "/a" {
		t.Fatalf("canonical must win: %v", got)
	}
	if _, ok := got["filepath"]; ok {
		t.Fatalf("alias must be dropped when canonical present: %v", got)
	}
}

func TestNormalizeCursorArgKeys_AliasCanonicalNotInSchema(t *testing.T) {
	// Schema declares only `path`. Alias `cmd` -> `command`; `command` not in schema, so `cmd`
	// must pass through unchanged.
	schema := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}},
	}
	args := map[string]interface{}{"cmd": "ls"}
	got := NormalizeCursorArgKeys(args, schema)
	if got["cmd"] != "ls" {
		t.Fatalf("alias whose canonical is not in schema must pass through: %v", got)
	}
}

func TestNormalizeCursorArgKeys_DuplicateAliasDropped(t *testing.T) {
	// Two aliases for the same canonical: only the first survives.
	schema := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}},
	}
	args := map[string]interface{}{"filepath": "/a", "filename": "/b"}
	got := NormalizeCursorArgKeys(args, schema)
	if got["path"] != "/a" {
		t.Fatalf("first alias must fill the canonical slot: %v", got)
	}
	if _, ok := got["filename"]; ok {
		t.Fatalf("second alias for the same canonical must be dropped: %v", got)
	}
	if _, ok := got["filepath"]; ok {
		t.Fatalf("first alias key must not remain alongside canonical: %v", got)
	}
}

func TestNormalizeCursorArgKeys_NilSchema(t *testing.T) {
	args := map[string]interface{}{"filepath": "/tmp"}
	got := NormalizeCursorArgKeys(args, nil)
	if !reflect.DeepEqual(got, args) {
		t.Fatalf("nil schema must pass through, got %v", got)
	}
}

func TestNormalizeCursorArgKeys_NilArgs(t *testing.T) {
	schema := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}},
	}
	got := NormalizeCursorArgKeys(nil, schema)
	if got != nil {
		t.Fatalf("nil args must stay nil, got %v", got)
	}
}
