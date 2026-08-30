package common

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	KiroToolNameMaxLength        = 64
	KiroToolUseIDMaxLength       = 64
	KiroMaxToolCount             = 48
	KiroToolCatalogBudgetBytes   = 96_000
	KiroDefaultToolDescription   = 1024
	KiroSolToolDescription       = 9216
	KiroMaxImagesPerMessage      = 20
	KiroImageBase64BudgetBytes   = 18 * 1024 * 1024
	kiroIdentifierHashHexLength  = 16
	kiroIdentifierSeparatorBytes = 1
)

var (
	kiroDateSuffixPattern       = regexp.MustCompile(`-\d{8}$`)
	kiroEffortSuffixPattern     = regexp.MustCompile(`-(?:low|medium|high|xhigh|max)$`)
	kiroClaudeFamilyFirst       = regexp.MustCompile(`^claude-(opus|sonnet|haiku)-(\d+)-(\d+)$`)
	kiroClaudeVersionFirst      = regexp.MustCompile(`^claude-(\d+)-(\d+)-(opus|sonnet|haiku)$`)
	kiroVersionedModelComponent = regexp.MustCompile(`^(gpt-\d+|minimax-m\d+)-(\d+)(-.+)?$`)
	kiroInvalidIdentifier       = regexp.MustCompile(`[^A-Za-z0-9_-]+`)
)

// KiroImage is the provider-native image representation shared by source
// translators.
type KiroImage struct {
	Format string          `json:"format"`
	Source KiroImageSource `json:"source"`
}

type KiroImageSource struct {
	Bytes string `json:"bytes"`
}

// NormalizeKiroModelID canonicalizes aliases accepted by opencodex and the
// local Kiro catalog while leaving unknown future identifiers intact.
func NormalizeKiroModelID(modelID string) string {
	id := strings.ToLower(strings.TrimSpace(modelID))
	if idx := strings.Index(id, KiroUnsupportedContext1MSuffix); idx >= 0 {
		id = id[:idx]
	}
	id = strings.TrimPrefix(id, "kiro/")
	id = strings.TrimPrefix(id, "kiro-")
	id = strings.TrimPrefix(id, "amazonq-")
	for {
		next := strings.TrimSuffix(id, "-agentic")
		next = strings.TrimSuffix(next, "-chat")
		next = strings.TrimSuffix(next, "-thinking")
		next = strings.TrimSuffix(next, "-latest")
		next = kiroEffortSuffixPattern.ReplaceAllString(next, "")
		next = kiroDateSuffixPattern.ReplaceAllString(next, "")
		if next == id {
			break
		}
		id = next
	}

	switch id {
	case "auto", "auto-kiro":
		return "auto"
	}

	if matches := kiroClaudeVersionFirst.FindStringSubmatch(id); len(matches) == 4 {
		return fmt.Sprintf("claude-%s-%s.%s", matches[3], matches[1], matches[2])
	}
	if matches := kiroClaudeFamilyFirst.FindStringSubmatch(id); len(matches) == 4 {
		return fmt.Sprintf("claude-%s-%s.%s", matches[1], matches[2], matches[3])
	}
	if matches := kiroVersionedModelComponent.FindStringSubmatch(id); len(matches) == 4 {
		return matches[1] + "." + matches[2] + matches[3]
	}
	return id
}

// SanitizeKiroToolSchema recursively removes JSON Schema keywords rejected by
// Kiro while retaining all supported structure.
func SanitizeKiroToolSchema(schema map[string]any) map[string]any {
	sanitized := make(map[string]any, len(schema))
	for key, value := range schema {
		if key == "additionalProperties" {
			continue
		}
		switch typed := value.(type) {
		case map[string]any:
			sanitized[key] = SanitizeKiroToolSchema(typed)
		case []any:
			if key == "required" && len(typed) == 0 {
				continue
			}
			items := make([]any, len(typed))
			for index, item := range typed {
				if nested, ok := item.(map[string]any); ok {
					items[index] = SanitizeKiroToolSchema(nested)
				} else {
					items[index] = item
				}
			}
			sanitized[key] = items
		default:
			sanitized[key] = value
		}
	}
	return sanitized
}

func KiroToolDescriptionLimit(modelID string) int {
	const fallback = 10_000
	value := strings.TrimSpace(os.Getenv("TOOL_DESCRIPTION_MAX_LENGTH"))
	if value == "" {
		return fallback
	}
	limit, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return limit
}

func PrepareKiroToolDescription(name, description, modelID string) (string, string) {
	limit := KiroToolDescriptionLimit(modelID)
	if limit <= 0 || utf8.RuneCountInString(description) <= limit {
		return description, ""
	}
	return fmt.Sprintf("Tool documentation moved to the system prompt: %s.", name),
		fmt.Sprintf("## Tool: %s\n%s", name, description)
}

// KiroModelContextWindow returns the concrete Kiro context window used for
// percentage-based usage checkpoints. Auto intentionally returns zero because
// its concrete model is not known until the response identifies it.
func KiroModelContextWindow(modelID string) int64 {
	switch NormalizeKiroModelID(modelID) {
	case "auto":
		return 0
	case "claude-opus-4.6", "claude-sonnet-4.6":
		return 1_000_000
	case "claude-opus-4.7", "claude-opus-4.8", "claude-opus-5", "claude-sonnet-5":
		return 666_667
	case "claude-opus-4.5", "claude-sonnet-4.5", "claude-sonnet-4", "claude-haiku-4.5", "glm-5":
		return 200_000
	case "deepseek-3.2":
		return 164_000
	case "minimax-m2.1", "minimax-m2.5":
		return 196_000
	case "qwen3-coder-next":
		return 256_000
	case "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna":
		return 272_000
	default:
		return 0
	}
}

func NormalizeKiroToolName(name string) string {
	return normalizeKiroIdentifier(name, KiroToolNameMaxLength, "tool")
}

func NormalizeKiroToolUseID(id string) string {
	return normalizeKiroIdentifier(id, KiroToolUseIDMaxLength, "toolu")
}

func normalizeKiroIdentifier(value string, maxLength int, fallback string) string {
	normalized := strings.Trim(kiroInvalidIdentifier.ReplaceAllString(strings.TrimSpace(value), "_"), "_")
	if normalized == "" {
		normalized = fallback
	}
	if len(normalized) <= maxLength {
		return normalized
	}

	hash := sha256.Sum256([]byte(normalized))
	suffix := hex.EncodeToString(hash[:])[:kiroIdentifierHashHexLength]
	prefixLength := maxLength - kiroIdentifierSeparatorBytes - len(suffix)
	return strings.TrimRight(normalized[:prefixLength], "_") + "_" + suffix
}

// LimitKiroImages preserves the newest images while enforcing Kiro's
// per-message count and aggregate base64 budget.
func LimitKiroImages(images []KiroImage) ([]KiroImage, int) {
	if len(images) == 0 {
		return nil, 0
	}

	start := 0
	if len(images) > KiroMaxImagesPerMessage {
		start = len(images) - KiroMaxImagesPerMessage
	}
	kept := append([]KiroImage(nil), images[start:]...)
	dropped := start

	total := 0
	for _, image := range kept {
		total += len(image.Source.Bytes)
	}
	for len(kept) > 0 && total > KiroImageBase64BudgetBytes {
		total -= len(kept[0].Source.Bytes)
		kept = kept[1:]
		dropped++
	}
	return kept, dropped
}
