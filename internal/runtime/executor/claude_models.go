package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const claudeModelsFetchTimeout = 15 * time.Second

// claudeModelsEndpoint builds the catalog URL for a configured base URL.
// Callers pass the credential's base-url override; gateways mirror Anthropic's
// layout, so the catalog lives at <base-url>/v1/models. A base URL that already
// carries the path (or a trailing slash) is accepted as-is.
func claudeModelsEndpoint(baseURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		return ""
	}
	if strings.HasSuffix(trimmed, "/v1/models") {
		return trimmed
	}
	if strings.HasSuffix(trimmed, "/v1") {
		return trimmed + "/models"
	}
	return trimmed + "/v1/models"
}

// FetchClaudeModels lists the catalog a Claude credential can actually reach.
//
// Only credentials pointed at a custom gateway are probed: Anthropic's own
// catalog is covered by the static definitions, and an OAuth credential on
// api.anthropic.com must not pay a network round trip just to render the
// auth-file model list. A third-party gateway, however, decides its own
// catalog, so the static list is meaningless there — that is the case this
// resolves. Any failure falls back to the static definitions.
func FetchClaudeModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	fallback := registry.GetClaudeModels()

	apiKey, baseURL := claudeCreds(auth)
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" || isAnthropicUpstreamBase(baseURL) {
		return fallback
	}
	endpoint := claudeModelsEndpoint(baseURL)
	if endpoint == "" {
		return fallback
	}

	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, claudeModelsFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		log.Warnf("claude: failed to create model fetch request: %v", err)
		return fallback
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	if key := strings.TrimSpace(apiKey); key != "" {
		// Gateways authenticate like Anthropic but accept the bearer form for
		// OAuth-issued tokens, which is what PrepareRequest sends off-Anthropic.
		req.Header.Set("Authorization", "Bearer "+key)
	}

	httpClient := newProxyAwareHTTPClient(ctx, cfg, auth, 0)
	resp, err := httpClient.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			log.Warnf("claude: fetch models canceled: %v", err)
		} else {
			log.Warnf("claude: using static models (API fetch failed: %v)", err)
		}
		return fallback
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("claude: close models response: %v", errClose)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Warnf("claude: failed to read models response: %v", err)
		return fallback
	}
	if resp.StatusCode != http.StatusOK {
		log.Warnf("claude: fetch models failed: status %d, body: %s", resp.StatusCode, string(body))
		return fallback
	}

	data := gjson.GetBytes(body, "data")
	if !data.Exists() {
		log.Warnf("claude: invalid models response format (expected data field)")
		return fallback
	}

	now := time.Now().Unix()
	seen := make(map[string]struct{})
	models := make([]*registry.ModelInfo, 0, 32)
	data.ForEach(func(_, value gjson.Result) bool {
		id := strings.TrimSpace(value.Get("id").String())
		if id == "" {
			return true
		}
		if _, exists := seen[id]; exists {
			return true
		}
		seen[id] = struct{}{}
		displayName := strings.TrimSpace(value.Get("display_name").String())
		if displayName == "" {
			displayName = id
		}
		models = append(models, &registry.ModelInfo{
			ID:          id,
			DisplayName: displayName,
			OwnedBy:     "anthropic",
			Type:        "claude",
			Object:      "model",
			Created:     now,
		})
		return true
	})

	// A gateway that answers with an empty catalog is reporting "nothing here",
	// but the static list would then advertise models it cannot serve. Keep the
	// fallback only when the response carried no usable entry at all.
	if len(models) == 0 {
		return fallback
	}
	return models
}
