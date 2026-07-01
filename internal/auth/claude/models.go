package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	log "github.com/sirupsen/logrus"
)

// AnthropicModelsResponse represents the response from the Anthropic /v1/models endpoint.
type AnthropicModelsResponse struct {
	Data    []AnthropicModelEntry `json:"data"`
	HasMore bool                  `json:"has_more"`
	FirstID string                `json:"first_id"`
	LastID  string                `json:"last_id"`
}

// AnthropicModelEntry represents a single model entry from the Anthropic models API.
// See https://platform.claude.com/docs/en/api/models/retrieve
type AnthropicModelEntry struct {
	ID             string                 `json:"id"`
	Type           string                 `json:"type"`
	DisplayName    string                 `json:"display_name"`
	CreatedAt      string                 `json:"created_at"`
	MaxInputTokens int                    `json:"max_input_tokens"`
	MaxTokens      int                    `json:"max_tokens"`
	Capabilities   *AnthropicCapabilities `json:"capabilities,omitempty"`
}

// AnthropicCapabilities describes model capability flags from the API.
type AnthropicCapabilities struct {
	Batch             AnthropicCapabilitySupport `json:"batch"`
	Citations         AnthropicCapabilitySupport `json:"citations"`
	CodeExecution     AnthropicCapabilitySupport `json:"code_execution"`
	ImageInput        AnthropicCapabilitySupport `json:"image_input"`
	PDFInput          AnthropicCapabilitySupport `json:"pdf_input"`
	StructuredOutputs AnthropicCapabilitySupport `json:"structured_outputs"`
}

// AnthropicCapabilitySupport indicates whether a capability is supported.
type AnthropicCapabilitySupport struct {
	Supported bool `json:"supported"`
}

const (
	anthropicModelsURL     = "https://api.anthropic.com/v1/models"
	anthropicModelsTimeout = 15 * time.Second
	maxModelsResponseSize  = 2 * 1024 * 1024 // 2 MB
)

// ListModels fetches available models from the Anthropic /v1/models endpoint using an OAuth access token.
// The token must be an OAuth token (sk-ant-oat*) for this to work; regular API keys use x-api-key instead.
// Returns the parsed model list or an error.
func (o *ClaudeAuth) ListModels(ctx context.Context, accessToken string) (*AnthropicModelsResponse, error) {
	if accessToken == "" {
		return nil, fmt.Errorf("claude: access token is required for listing models")
	}

	// Determine base URL from config override, default to api.anthropic.com.
	modelsURL := anthropicModelsURL
	if o.cfg != nil {
		override := o.cfg.GetOAuthEndpointOverride("claude")
		if ep := strings.TrimSpace(override.ApiBaseURL); ep != "" {
			modelsURL = strings.TrimRight(ep, "/") + "/v1/models"
		}
	}

	reqCtx, cancel := context.WithTimeout(ctx, anthropicModelsTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("claude: failed to create models request: %w", err)
	}

	// OAuth bearer token + required beta header for OAuth model listing.
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Accept", "application/json")

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("claude: models request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("claude list models: close body error: %v", errClose)
		}
	}()

	limitedReader := io.LimitReader(resp.Body, maxModelsResponseSize)
	bodyBytes, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("claude: failed to read models response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("claude: list models failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var modelsResp AnthropicModelsResponse
	if err = json.Unmarshal(bodyBytes, &modelsResp); err != nil {
		return nil, fmt.Errorf("claude: failed to parse models response: %w", err)
	}

	return &modelsResp, nil
}

// ToModelInfo converts an AnthropicModelEntry to a registry.ModelInfo.
// Static model metadata (from GetClaudeModels) takes precedence for known models;
// this conversion is used to add dynamically-discovered models not in the static catalog.
func (e AnthropicModelEntry) ToModelInfo() *registry.ModelInfo {
	mi := &registry.ModelInfo{
		ID:           e.ID,
		Object:       "model",
		Type:         "claude",
		DisplayName:  e.DisplayName,
		OwnedBy:      "anthropic",
		Created:      parseAnthropicCreatedAt(e.CreatedAt),
		InputTokenLimit:  e.MaxInputTokens,
		MaxCompletionTokens: e.MaxTokens,
		ContextLength:     e.MaxInputTokens,
	}

	if e.Capabilities != nil {
		if e.Capabilities.ImageInput.Supported {
			mi.SupportedInputModalities = append(mi.SupportedInputModalities, "TEXT", "IMAGE")
		} else {
			mi.SupportedInputModalities = append(mi.SupportedInputModalities, "TEXT")
		}
		if e.Capabilities.PDFInput.Supported {
			mi.SupportedInputModalities = append(mi.SupportedInputModalities, "PDF")
		}
	}

	return mi
}

// parseAnthropicCreatedAt parses an RFC 3339 datetime string to a unix timestamp.
// Returns 0 on failure.
func parseAnthropicCreatedAt(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0
	}
	return t.Unix()
}
