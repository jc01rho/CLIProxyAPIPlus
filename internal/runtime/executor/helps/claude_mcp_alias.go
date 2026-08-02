package helps

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"strings"
)

var claudeMCPBase32 = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// IsClaudeMCPToolName reports whether name follows Claude Code's MCP tool
// convention and contains only characters accepted by Anthropic tool names.
func IsClaudeMCPToolName(name string) bool {
	if len(name) == 0 || len(name) > 64 || !strings.HasPrefix(name, "mcp__") {
		return false
	}
	rest := strings.TrimPrefix(name, "mcp__")
	separator := strings.Index(rest, "__")
	if separator <= 0 || separator+2 >= len(rest) {
		return false
	}
	for _, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

// ClaudeMCPToolAlias derives an opaque Claude Code-style MCP tool name. All
// aliases created with the same caller secret share one virtual server name;
// original tool names affect only the tool component. A higher attempt changes
// the tool component when a request-local collision must be avoided.
func ClaudeMCPToolAlias(secret, original string, attempt uint32) string {
	serverDigest := claudeMCPAliasDigest(secret, "server", "", 0)
	toolDigest := claudeMCPAliasDigest(secret, "tool", original, attempt)
	server := claudeMCPBase32.EncodeToString(serverDigest[:])[:12]
	tool := claudeMCPBase32.EncodeToString(toolDigest[:])[:16]
	return "mcp__" + server + "__" + tool
}

func claudeMCPAliasDigest(secret, purpose, original string, attempt uint32) [sha256.Size]byte {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("cpa-claude-mcp-alias-v2\x00"))
	_, _ = mac.Write([]byte(purpose))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(original))
	var counter [4]byte
	binary.BigEndian.PutUint32(counter[:], attempt)
	_, _ = mac.Write(counter[:])
	var digest [sha256.Size]byte
	copy(digest[:], mac.Sum(nil))
	return digest
}
