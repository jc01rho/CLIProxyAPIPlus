package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	zcodeModelsEndpoint     = "/v1/models"
	zcodeModelsFetchTimeout = 5 * time.Second

	// zcodeAnthropicVersion is the Anthropic API version the ZCode client sends
	// alongside its source headers.
	zcodeAnthropicVersion = "2023-06-01"

	// zcodeMaxDisplayNameLen bounds the provider-controlled model name that ends
	// up rendered in model listings.
	zcodeMaxDisplayNameLen = 200
)

// FetchZcodeModels retrieves the live GLM catalog from the Anthropic-compatible
// /v1/models endpoint using the provisioned Z.AI API key. It returns nil when no
// key is available or the fetch yields nothing, so the caller falls back to the
// static catalog (registry.GetZcodeModels).
//
// Live IDs the static catalog also knows about inherit the static capability
// metadata (context window, max completion tokens, type) that the remote
// catalog omits; genuinely new IDs are advertised as discovered.
func FetchZcodeModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	if ctx == nil {
		ctx = context.Background()
	}
	apiKey := zcodeCreds(auth)
	if apiKey == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, zcodeModelsFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, zcodeModelsURL(auth), nil)
	if err != nil {
		log.Debugf("zcode: failed to create models request: %v", err)
		return nil
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("anthropic-version", zcodeAnthropicVersion)
	for k, vs := range buildZCodeSourceHeaders() {
		for _, v := range vs {
			req.Header.Set(k, v)
		}
	}

	resp, err := newProxyAwareHTTPClient(ctx, cfg, auth, 0).Do(req)
	if err != nil {
		log.Debugf("zcode: models request failed: %v", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Debugf("zcode: models request returned status %d", resp.StatusCode)
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Debugf("zcode: failed to read models response: %v", err)
		return nil
	}
	dynamic := parseZcodeModels(body)
	if len(dynamic) == 0 {
		return nil
	}
	return registry.OverlayStaticMetadata(dynamic, registry.GetZcodeModels())
}

// zcodeModelsURL resolves the models endpoint. The base is pinned to api.z.ai
// unless the auth record carries an explicit override.
func zcodeModelsURL(auth *cliproxyauth.Auth) string {
	base := ZCodeAnthropicBaseURL
	for _, key := range []string{"models_base_url", "base_url"} {
		if v := strings.TrimSpace(getAuthValue(auth, key)); v != "" {
			base = v
			break
		}
	}
	return strings.TrimSuffix(base, "/") + zcodeModelsEndpoint
}

// parseZcodeModels converts the OpenAI-compatible {"data":[{"id","name"}]}
// catalog into model infos.
func parseZcodeModels(body []byte) []*registry.ModelInfo {
	var payload struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			ContextLength int    `json:"context_length"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		log.Debugf("zcode: failed to decode models response: %v", err)
		return nil
	}
	models := make([]*registry.ModelInfo, 0, len(payload.Data))
	for _, entry := range payload.Data {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			continue
		}
		displayName := sanitizeZcodeModelName(entry.Name)
		if displayName == "" {
			displayName = id
		}
		models = append(models, &registry.ModelInfo{
			ID:                        id,
			Object:                    "model",
			Type:                      "claude",
			DisplayName:               displayName,
			ContextLength:             entry.ContextLength,
			SupportedInputModalities:  []string{"TEXT"},
			SupportedOutputModalities: []string{"TEXT"},
		})
	}
	return models
}

// sanitizeZcodeModelName strips control characters from the provider-controlled
// model name, collapses whitespace, and bounds the length before the value can
// reach a model listing.
func sanitizeZcodeModelName(name string) string {
	if name == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(r)
	}
	cleaned := strings.Join(strings.Fields(b.String()), " ")
	if len(cleaned) > zcodeMaxDisplayNameLen {
		cleaned = strings.TrimSpace(cleaned[:zcodeMaxDisplayNameLen])
	}
	return cleaned
}
