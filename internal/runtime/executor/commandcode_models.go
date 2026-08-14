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

const (
	// commandCodeLiveModelsEndpointPath is the OpenAI-style /provider/v1/models
	// route used to enumerate CommandCode's live catalog.
	commandCodeLiveModelsEndpointPath = "/provider/v1/models"

	// commandCodeModelsFetchTimeout caps the time spent waiting for the live
	// catalog fetch. CommandCode responds in well under a second on the public
	// route, but the proxy must still bail out before the conductor budget
	// expires if the host is degraded.
	commandCodeModelsFetchTimeout = 15 * time.Second
)

// commandCodeNormalizeHost strips catalog or OpenAI-compat suffixes so generate
// and models URLs can be rebuilt from the same host. Accepts the bare default
// host, a trailing /v1, /v1/models, /provider, /provider/v1, or the full
// /provider/v1/models catalog URL that operators commonly paste as base-url.
func commandCodeNormalizeHost(baseURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		return strings.TrimRight(commandCodeBaseURL, "/")
	}
	lower := strings.ToLower(trimmed)
	switch {
	case strings.HasSuffix(lower, commandCodeLiveModelsEndpointPath):
		return trimmed[:len(trimmed)-len(commandCodeLiveModelsEndpointPath)]
	case strings.HasSuffix(lower, "/provider/v1"):
		return trimmed[:len(trimmed)-len("/provider/v1")]
	case strings.HasSuffix(lower, "/provider"):
		return trimmed[:len(trimmed)-len("/provider")]
	case strings.HasSuffix(lower, "/v1/models"):
		return trimmed[:len(trimmed)-len("/v1/models")]
	case strings.HasSuffix(lower, "/v1"):
		return trimmed[:len(trimmed)-len("/v1")]
	default:
		return trimmed
	}
}

// commandCodeBaseURLForAuth mirrors commandCodeGenerateURL's host selection so
// the model fetch targets the same base the executor will call later. It
// accepts an override via auth.Attributes["base_url"] exactly like
// commandCodeGenerateURL.
func commandCodeBaseURLForAuth(auth *cliproxyauth.Auth) string {
	if auth != nil && auth.Attributes != nil {
		if configured := strings.TrimSpace(auth.Attributes["base_url"]); configured != "" {
			return commandCodeNormalizeHost(configured)
		}
	}
	return commandCodeNormalizeHost("")
}

// commandCodeBuildModelsURL resolves the OpenAI-style /provider/v1/models URL
// against the same host selection the executor uses for /alpha/generate.
func commandCodeBuildModelsURL(baseURL string) string {
	return commandCodeNormalizeHost(baseURL) + commandCodeLiveModelsEndpointPath
}

// FetchCommandCodeModels retrieves the live CommandCode catalog from
// <base>/provider/v1/models. Failures (network error, non-2xx, malformed body)
// fall back to the static catalog so a configured provider still surfaces
// models through the OpenAI /v1/models surface even when the upstream is down.
//
// The endpoint is publicly readable: it returns an OpenAI-shaped
// {"object":"list","data":[{"id":..., "name":..., "context_length":...}]}
// envelope, and we deliberately send the configured API key as a bearer so a
// per-tenant catalog (if the upstream ever gates it) takes precedence over the
// anonymous one. An empty credential is permitted and falls back to the public
// catalog, mirroring the executor's runtime behavior.
func FetchCommandCodeModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	return fetchCommandCodeModels(ctx, auth, cfg, registry.GetCommandCodeModels())
}

func fetchCommandCodeModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config, fallback []*registry.ModelInfo) []*registry.ModelInfo {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, commandCodeModelsFetchTimeout)
	defer cancel()

	base := commandCodeBaseURLForAuth(auth)
	url := commandCodeBuildModelsURL(base)
	httpClient := newProxyAwareHTTPClient(ctx, cfg, auth, 0)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		log.Warnf("commandcode: failed to create models request: %v", err)
		return fallback
	}
	req.Header.Set("Accept", "application/json")
	if apiKey := commandCodeAPIKey(auth); apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("x-command-code-version", commandCodeVersion)
	req.Header.Set("x-cli-environment", "production")
	req.Header.Set("x-project-slug", commandCodeProject)

	resp, err := httpClient.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			log.Warnf("commandcode: models fetch canceled: %v", err)
		} else {
			log.Warnf("commandcode: using static models (API fetch failed: %v)", err)
		}
		return fallback
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("commandcode: close models response body error: %v", errClose)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Warnf("commandcode: failed to read models response: %v", err)
		return fallback
	}
	if resp.StatusCode != http.StatusOK {
		log.Warnf("commandcode: fetch models failed: status %d, body: %s", resp.StatusCode, string(body))
		return fallback
	}

	merged := mergeCommandCodeCatalog(fallback, body)
	if len(merged) == 0 {
		return fallback
	}
	return merged
}

// mergeCommandCodeCatalog overlays the live response onto the static catalog
// without dropping static-only entries. It preserves static display names and
// context lengths when the upstream omits them, while always trusting the live
// `id` so newly-released models show up without a server restart.
func mergeCommandCodeCatalog(fallback []*registry.ModelInfo, body []byte) []*registry.ModelInfo {
	if len(body) == 0 {
		return nil
	}
	result := gjson.GetBytes(body, "data")
	if !result.Exists() || !result.IsArray() {
		result = gjson.ParseBytes(body)
		if !result.IsArray() {
			log.Warnf("commandcode: invalid models response (expected array or data field with array)")
			return nil
		}
	}

	staticByID := make(map[string]*registry.ModelInfo, len(fallback))
	for _, model := range fallback {
		if model == nil {
			continue
		}
		staticByID[model.ID] = model
	}

	seen := make(map[string]struct{})
	merged := make([]*registry.ModelInfo, 0, len(fallback)+8)

	result.ForEach(func(_, value gjson.Result) bool {
		id := strings.TrimSpace(value.Get("id").String())
		if id == "" {
			return true
		}
		if _, exists := seen[id]; exists {
			return true
		}
		seen[id] = struct{}{}

		displayName := strings.TrimSpace(value.Get("name").String())
		contextLength := int(value.Get("context_length").Int())
		if displayName == "" || contextLength == 0 {
			if staticModel, ok := staticByID[id]; ok && staticModel != nil {
				if displayName == "" {
					displayName = strings.TrimSpace(staticModel.DisplayName)
					if displayName == "" {
						displayName = id
					}
				}
				if contextLength == 0 {
					contextLength = staticModel.ContextLength
				}
			} else if displayName == "" {
				displayName = id
			}
		}

		created := value.Get("created").Int()
		if created == 0 {
			created = time.Now().Unix()
		}

		merged = append(merged, &registry.ModelInfo{
			ID:            id,
			Object:        "model",
			Created:       created,
			OwnedBy:       "commandcode",
			Type:          "commandcode",
			DisplayName:   displayName,
			Name:          id,
			ContextLength: contextLength,
		})
		return true
	})

	// Preserve static-only entries (e.g., models the upstream dropped) so an
	// existing alias config still resolves.
	for _, model := range fallback {
		if model == nil || model.ID == "" {
			continue
		}
		if _, exists := seen[model.ID]; exists {
			continue
		}
		seen[model.ID] = struct{}{}
		merged = append(merged, model)
	}

	return merged
}
