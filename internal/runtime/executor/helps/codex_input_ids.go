package helps

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	codexInputItemIDLimit                 = 64
	codexMessageItemIDPrefix              = "msg"
	codexReasoningItemIDPrefix            = "rs"
	codexFunctionCallItemIDPrefix         = "fc"
	codexCustomToolCallItemIDPrefix       = "ctc"
	codexCustomToolCallOutputItemIDPrefix = "ctco"
	codexFallbackInputItemName            = "unknown"

	codexInputItemIDOccupied  uint8 = 1 << 0
	codexInputItemIDPreserved uint8 = 1 << 1
)

// ChatGPT Codex backend rejects these public Responses item fields as unknown_parameter.
var codexRejectedInputItemFields = []string{"status", "phase", "namespace"}

// SanitizeCodexInputItemIDs normalizes supported input item IDs for Codex, removes encrypted
// reasoning items whose IDs exceed the Codex limit, fills missing tool-call names, strips
// item fields the ChatGPT Codex backend rejects (status, phase, namespace), and
// deterministically shortens other overlong input item IDs.
func SanitizeCodexInputItemIDs(body []byte) []byte {
	input := util.GetGJSONBytesNoCopy(body, "input")
	if !input.IsArray() {
		return body
	}

	items := input.Array()
	idStates := make(map[string]uint8, len(items))
	for _, item := range items {
		if shouldDropCodexEncryptedReasoningItem(item) {
			continue
		}
		itemID := item.Get("id")
		if itemID.Type != gjson.String {
			continue
		}
		originalID := itemID.String()
		id := normalizeCodexInputItemID(item, originalID)
		state := idStates[id]
		if id == originalID {
			state |= codexInputItemIDPreserved
		}
		if len([]rune(id)) <= codexInputItemIDLimit {
			state |= codexInputItemIDOccupied
		}
		if state != 0 {
			idStates[id] = state
		}
	}

	var mapped map[string]string
	var collisionMapped map[string]string
	rebuilt := make([]string, 0, len(items))
	changed := false
	for _, item := range items {
		if shouldDropCodexEncryptedReasoningItem(item) {
			changed = true
			continue
		}

		raw := item.Raw
		stripped, strippedOK := stripCodexRejectedInputItemFields(raw, item)
		if strippedOK {
			raw = stripped
			changed = true
		}
		switch item.Get("type").String() {
		case "function_call", "custom_tool_call":
			if strings.TrimSpace(item.Get("name").String()) == "" {
				next, errSet := sjson.SetBytes([]byte(raw), "name", codexFallbackInputItemName)
				if errSet == nil {
					raw = string(next)
					changed = true
				}
			}
		}
		itemID := item.Get("id")
		if itemID.Type == gjson.String {
			originalID := itemID.String()
			id := normalizeCodexInputItemID(item, originalID)
			if id != originalID && idStates[id]&codexInputItemIDPreserved != 0 {
				collisionID, ok := collisionMapped[id]
				if !ok {
					for attempt := 0; ; attempt++ {
						collisionID = codexInputItemIDWithHashSuffix(id, attempt)
						if idStates[collisionID]&codexInputItemIDOccupied != 0 {
							continue
						}
						if collisionMapped == nil {
							collisionMapped = make(map[string]string)
						}
						collisionMapped[id] = collisionID
						idStates[collisionID] |= codexInputItemIDOccupied
						break
					}
				}
				id = collisionID
			}
			if len([]rune(id)) > codexInputItemIDLimit {
				shortened, ok := mapped[id]
				if !ok {
					shortened = shortenCodexInputItemID(id)
					for attempt := 1; ; attempt++ {
						if idStates[shortened]&codexInputItemIDOccupied == 0 {
							break
						}
						shortened = shortenCodexInputItemIDWithAttempt(id, attempt)
					}
					if mapped == nil {
						mapped = make(map[string]string)
					}
					mapped[id] = shortened
					idStates[shortened] |= codexInputItemIDOccupied
				}
				id = shortened
			}

			if id != originalID {
				next, errSet := sjson.SetBytes([]byte(raw), "id", id)
				if errSet == nil {
					raw = string(next)
					changed = true
				}
			}
		}
		rebuilt = append(rebuilt, raw)
	}
	if !changed {
		return body
	}

	updated, errSet := sjson.SetRawBytes(body, "input", []byte("["+strings.Join(rebuilt, ",")+"]"))
	if errSet != nil {
		return body
	}
	return updated
}

func stripCodexRejectedInputItemFields(raw string, item gjson.Result) (string, bool) {
	changed := false
	for _, field := range codexRejectedInputItemFields {
		if !item.Get(field).Exists() {
			continue
		}
		next, errDel := sjson.DeleteBytes([]byte(raw), field)
		if errDel != nil {
			continue
		}
		raw = string(next)
		changed = true
	}
	return raw, changed
}

func normalizeCodexInputItemID(item gjson.Result, id string) string {
	var prefix string
	switch item.Get("type").String() {
	case "message":
		prefix = codexMessageItemIDPrefix
	case "reasoning":
		prefix = codexReasoningItemIDPrefix
	case "function_call":
		prefix = codexFunctionCallItemIDPrefix
	case "custom_tool_call":
		prefix = codexCustomToolCallItemIDPrefix
	case "custom_tool_call_output":
		prefix = codexCustomToolCallOutputItemIDPrefix
	default:
		return id
	}
	if id == "" || strings.HasPrefix(id, prefix) {
		return normalizeCodexInputItemIDCharacters(id)
	}
	return normalizeCodexInputItemIDCharacters(prefix + "_" + id)
}

func normalizeCodexInputItemIDCharacters(id string) string {
	if id == "" {
		return id
	}

	var normalized strings.Builder
	normalized.Grow(len(id))
	hasInvalidCharacter := false
	for _, char := range id {
		if isCodexInputItemIDCharacter(char) {
			normalized.WriteRune(char)
			continue
		}
		normalized.WriteByte('_')
		hasInvalidCharacter = true
	}
	if !hasInvalidCharacter {
		return id
	}

	sum := sha256.Sum256([]byte(id))
	normalized.WriteByte('_')
	normalized.WriteString(hex.EncodeToString(sum[:8]))
	return normalized.String()
}

func isCodexInputItemIDCharacter(char rune) bool {
	return (char >= 'a' && char <= 'z') ||
		(char >= 'A' && char <= 'Z') ||
		(char >= '0' && char <= '9') ||
		char == '_' ||
		char == '-'
}

func shouldDropCodexEncryptedReasoningItem(item gjson.Result) bool {
	if item.Get("type").String() != "reasoning" {
		return false
	}
	itemID := item.Get("id")
	if itemID.Type != gjson.String || len([]rune(itemID.String())) <= codexInputItemIDLimit {
		return false
	}
	encryptedContent := item.Get("encrypted_content")
	return encryptedContent.Type == gjson.String && encryptedContent.String() != ""
}

func shortenCodexInputItemID(id string) string {
	return shortenCodexInputItemIDWithAttempt(id, 0)
}

func shortenCodexInputItemIDWithAttempt(id string, attempt int) string {
	runes := []rune(id)
	if len(runes) <= codexInputItemIDLimit {
		return id
	}
	return codexInputItemIDWithHashSuffixRunes(id, runes, attempt)
}

func codexInputItemIDWithHashSuffix(id string, attempt int) string {
	return codexInputItemIDWithHashSuffixRunes(id, []rune(id), attempt)
}

func codexInputItemIDWithHashSuffixRunes(id string, runes []rune, attempt int) string {
	hashInput := id
	if attempt > 0 {
		hashInput += "\x00" + strconv.Itoa(attempt)
	}
	sum := sha256.Sum256([]byte(hashInput))
	suffix := "_" + hex.EncodeToString(sum[:8])
	prefixLength := codexInputItemIDLimit - len(suffix)
	if len(runes) < prefixLength {
		prefixLength = len(runes)
	}
	return string(runes[:prefixLength]) + suffix
}
