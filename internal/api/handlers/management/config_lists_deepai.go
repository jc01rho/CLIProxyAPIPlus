package management

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// deepai-api-key: []DeepAIKey
func (h *Handler) GetDeepAIKeys(c *gin.Context) {
	c.JSON(200, gin.H{"deepai-api-key": h.deepAIKeysWithAuthIndex()})
}

func (h *Handler) PutDeepAIKeys(c *gin.Context) {
	data, errRead := c.GetRawData()
	if errRead != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	var arr []config.DeepAIKey
	if errUnmarshal := json.Unmarshal(data, &arr); errUnmarshal != nil {
		var obj struct {
			Items []config.DeepAIKey `json:"items"`
		}
		if errObj := json.Unmarshal(data, &obj); errObj != nil {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		arr = obj.Items
	}
	for providerIndex := range arr {
		for keyIndex := range arr[providerIndex].APIKeyEntries {
			field := fmt.Sprintf("deepai-api-key[%d].api-key-entries[%d].weight", providerIndex, keyIndex)
			if rejectInvalidCredentialWeight(c, field, arr[providerIndex].APIKeyEntries[keyIndex].Weight) {
				return
			}
		}
	}
	candidate := &config.Config{DeepAIKey: append([]config.DeepAIKey(nil), arr...)}
	candidate.SanitizeDeepAIKeys()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.DeepAIKey = candidate.DeepAIKey
	h.persistLocked(c)
}

func (h *Handler) PatchDeepAIKey(c *gin.Context) {
	type deepAIKeyPatch struct {
		APIKey         *string                             `json:"api-key"`
		Priority       *int                                `json:"priority"`
		Prefix         *string                             `json:"prefix"`
		BaseURL        *string                             `json:"base-url"`
		ProxyURL       *string                             `json:"proxy-url"`
		BillingClass   *string                             `json:"billing-class"`
		Models         *[]config.DeepAIModel               `json:"models"`
		Headers        *map[string]string                  `json:"headers"`
		ExcludedModels *[]string                           `json:"excluded-models"`
		DisableCooling *bool                               `json:"disable-cooling"`
		APIKeyEntries  *[]config.OpenAICompatibilityAPIKey `json:"api-key-entries"`
	}
	var body struct {
		Index *int            `json:"index"`
		Match *string         `json:"match"`
		Value *deepAIKeyPatch `json:"value"`
	}
	if errBind := c.ShouldBindJSON(&body); errBind != nil || body.Value == nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	targetIndex := -1
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(h.cfg.DeepAIKey) {
		targetIndex = *body.Index
	}
	if targetIndex == -1 && body.Match != nil {
		match := strings.TrimSpace(*body.Match)
		for i := range h.cfg.DeepAIKey {
			if h.cfg.DeepAIKey[i].ContainsAPIKey(match) || strings.TrimSpace(h.cfg.DeepAIKey[i].Comment) == match {
				targetIndex = i
				break
			}
		}
	}
	if targetIndex == -1 {
		c.JSON(404, gin.H{"error": "item not found"})
		return
	}

	entry := h.cfg.DeepAIKey[targetIndex]
	if body.Value.APIKey != nil {
		entry.APIKey = strings.TrimSpace(*body.Value.APIKey)
	}
	if body.Value.Priority != nil {
		entry.Priority = *body.Value.Priority
	}
	if body.Value.Prefix != nil {
		entry.Prefix = strings.TrimSpace(*body.Value.Prefix)
	}
	if body.Value.BaseURL != nil {
		trimmed := strings.TrimSpace(*body.Value.BaseURL)
		if trimmed == "" {
			h.cfg.DeepAIKey = append(h.cfg.DeepAIKey[:targetIndex], h.cfg.DeepAIKey[targetIndex+1:]...)
			h.cfg.SanitizeDeepAIKeys()
			h.persistLocked(c)
			return
		}
		entry.BaseURL = trimmed
	}
	if body.Value.ProxyURL != nil {
		entry.ProxyURL = strings.TrimSpace(*body.Value.ProxyURL)
	}
	if body.Value.BillingClass != nil {
		entry.BillingClass = config.BillingClass(normalizeBillingClassValue(*body.Value.BillingClass))
	}
	if body.Value.Models != nil {
		entry.Models = append([]config.DeepAIModel(nil), (*body.Value.Models)...)
	}
	if body.Value.Headers != nil {
		entry.Headers = config.NormalizeHeaders(*body.Value.Headers)
	}
	if body.Value.ExcludedModels != nil {
		entry.ExcludedModels = config.NormalizeExcludedModels(*body.Value.ExcludedModels)
	}
	if body.Value.DisableCooling != nil {
		entry.DisableCooling = *body.Value.DisableCooling
	}
	if body.Value.APIKeyEntries != nil {
		entry.APIKeyEntries = append([]config.OpenAICompatibilityAPIKey(nil), (*body.Value.APIKeyEntries)...)
	}
	normalizeDeepAIKey(&entry)
	h.cfg.DeepAIKey[targetIndex] = entry
	h.cfg.SanitizeDeepAIKeys()
	h.persistLocked(c)
}

func (h *Handler) DeleteDeepAIKey(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if val := strings.TrimSpace(c.Query("api-key")); val != "" {
		if baseRaw, okBase := c.GetQuery("base-url"); okBase {
			base := strings.TrimSpace(baseRaw)
			out := make([]config.DeepAIKey, 0, len(h.cfg.DeepAIKey))
			for _, entry := range h.cfg.DeepAIKey {
				if entry.ContainsAPIKey(val) && strings.TrimSpace(entry.BaseURL) == base {
					continue
				}
				out = append(out, entry)
			}
			h.cfg.DeepAIKey = out
			h.cfg.SanitizeDeepAIKeys()
			h.persistLocked(c)
			return
		}

		matchIndex := -1
		matchCount := 0
		for i := range h.cfg.DeepAIKey {
			if h.cfg.DeepAIKey[i].ContainsAPIKey(val) {
				matchCount++
			}
		}
		if matchCount == 1 {
			for i := range h.cfg.DeepAIKey {
				if h.cfg.DeepAIKey[i].ContainsAPIKey(val) {
					matchIndex = i
					break
				}
			}
		}
		if matchIndex != -1 {
			h.cfg.DeepAIKey = append(h.cfg.DeepAIKey[:matchIndex], h.cfg.DeepAIKey[matchIndex+1:]...)
		}
		h.cfg.SanitizeDeepAIKeys()
		h.persistLocked(c)
		return
	}
	if idxStr := c.Query("index"); idxStr != "" {
		var idx int
		_, errScan := fmt.Sscanf(idxStr, "%d", &idx)
		if errScan == nil && idx >= 0 && idx < len(h.cfg.DeepAIKey) {
			h.cfg.DeepAIKey = append(h.cfg.DeepAIKey[:idx], h.cfg.DeepAIKey[idx+1:]...)
			h.cfg.SanitizeDeepAIKeys()
			h.persistLocked(c)
			return
		}
	}
	c.JSON(400, gin.H{"error": "missing api-key or index"})
}

// normalizeDeepAIKey trims a DeepAI config entry before it is persisted.
func normalizeDeepAIKey(entry *config.DeepAIKey) {
	if entry == nil {
		return
	}
	entry.APIKey = strings.TrimSpace(entry.APIKey)
	entry.Comment = strings.TrimSpace(entry.Comment)
	entry.Prefix = strings.TrimSpace(entry.Prefix)
	entry.BaseURL = strings.TrimSpace(entry.BaseURL)
	entry.ProxyURL = strings.TrimSpace(entry.ProxyURL)
	nested := make([]config.OpenAICompatibilityAPIKey, 0, len(entry.APIKeyEntries))
	for _, apiKeyEntry := range entry.APIKeyEntries {
		apiKeyEntry.APIKey = strings.TrimSpace(apiKeyEntry.APIKey)
		apiKeyEntry.ProxyURL = strings.TrimSpace(apiKeyEntry.ProxyURL)
		apiKeyEntry.Comment = strings.TrimSpace(apiKeyEntry.Comment)
		if apiKeyEntry.APIKey != "" {
			nested = append(nested, apiKeyEntry)
		}
	}
	entry.APIKeyEntries = nested
	config.FoldDeepAILegacyAPIKey(entry)
	entry.BillingClass = config.BillingClass(normalizeBillingClassValue(string(entry.BillingClass)))
	entry.Headers = config.NormalizeHeaders(entry.Headers)
	entry.ExcludedModels = config.NormalizeExcludedModels(entry.ExcludedModels)
	for i := range entry.Models {
		entry.Models[i].Name = strings.TrimSpace(entry.Models[i].Name)
		entry.Models[i].Alias = strings.TrimSpace(entry.Models[i].Alias)
	}
}
