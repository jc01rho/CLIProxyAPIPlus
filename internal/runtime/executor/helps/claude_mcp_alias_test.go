package helps

import (
	"regexp"
	"strings"
	"testing"
)

func TestIsClaudeMCPToolName(t *testing.T) {
	for _, name := range []string{
		"mcp__context7__query-docs",
		"mcp__amber_cedar__quiet_harbor",
		"mcp__server__tool__variant",
	} {
		if !IsClaudeMCPToolName(name) {
			t.Fatalf("IsClaudeMCPToolName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{
		"context7__query-docs",
		"mcp____query-docs",
		"mcp__context7__",
		"mcp__context7__query.docs",
		"mcp__context7__" + strings.Repeat("x", 64),
	} {
		if IsClaudeMCPToolName(name) {
			t.Fatalf("IsClaudeMCPToolName(%q) = true, want false", name)
		}
	}
}

func TestClaudeMCPToolAlias(t *testing.T) {
	first := ClaudeMCPToolAlias("credential-secret", "search_web", 0)
	if second := ClaudeMCPToolAlias("credential-secret", "search_web", 0); second != first {
		t.Fatalf("alias is not deterministic: %q != %q", first, second)
	}
	caseDistinct := ClaudeMCPToolAlias("credential-secret", "Search_Web", 0)
	if first == caseDistinct {
		t.Fatalf("case-distinct names produced the same initial alias: %q", first)
	}
	retry := ClaudeMCPToolAlias("credential-secret", "search_web", 1)
	if first == retry {
		t.Fatalf("collision retry did not change alias: %q", first)
	}
	if !IsClaudeMCPToolName(first) {
		t.Fatalf("generated alias %q is not a valid MCP tool name", first)
	}
	if strings.Contains(first, "search") || strings.Contains(first, "web") {
		t.Fatalf("generated alias %q reveals the original tool name", first)
	}
	if matched, _ := regexp.MatchString(`^mcp__[a-z2-7]{12}__[a-z2-7]{16}$`, first); !matched {
		t.Fatalf("generated alias %q is not keyed lowercase Base32", first)
	}
	server := strings.Split(first, "__")[1]
	if got := strings.Split(caseDistinct, "__")[1]; got != server {
		t.Fatalf("case-distinct tool server = %q, want shared caller server %q", got, server)
	}
	if got := strings.Split(retry, "__")[1]; got != server {
		t.Fatalf("retry server = %q, want shared caller server %q", got, server)
	}
	if got := strings.Split(ClaudeMCPToolAlias("other-caller", "search_web", 0), "__")[1]; got == server {
		t.Fatalf("different caller unexpectedly shared server %q", server)
	}
}
