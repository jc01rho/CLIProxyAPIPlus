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

// FetchOpenCodeModels fetches the live OpenCode catalog for the configured
// gateway (Zen by default, Go when base-url points at /zen/go/v1). The endpoint
// answers anonymously, so an entry without a credential still lists models.
// Failures fall back to the static catalog.
func FetchOpenCodeModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	fallback := registry.GetOpenCodeModels()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	url := openCodeBaseURL(auth) + openCodeModelsEndpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		log.Warnf("opencode: failed to create model fetch request: %v", err)
		return fallback
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", openCodeUserAgent())
	if key := openCodeAPIKey(auth); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	httpClient := newProxyAwareHTTPClient(ctx, cfg, auth, 0)
	resp, err := httpClient.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			log.Warnf("opencode: fetch models canceled: %v", err)
		} else {
			log.Warnf("opencode: using static models (API fetch failed: %v)", err)
		}
		return fallback
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("opencode: close models response: %v", errClose)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Warnf("opencode: failed to read models response: %v", err)
		return fallback
	}
	if resp.StatusCode != http.StatusOK {
		log.Warnf("opencode: fetch models failed: status %d, body: %s", resp.StatusCode, string(body))
		return fallback
	}

	data := gjson.GetBytes(body, "data")
	if !data.Exists() {
		log.Warnf("opencode: invalid models response format (expected data field)")
		return fallback
	}

	now := time.Now().Unix()
	seen := make(map[string]struct{})
	merged := make([]*registry.ModelInfo, 0, 128)
	for _, model := range fallback {
		if model == nil || model.ID == "" {
			continue
		}
		seen[model.ID] = struct{}{}
		merged = append(merged, model)
	}
	data.ForEach(func(_, value gjson.Result) bool {
		id := strings.TrimSpace(value.Get("id").String())
		// Regional aliases ("<model>:global") are internal routing entries and
		// are hidden from the public catalog by the gateway itself.
		if id == "" || strings.HasSuffix(id, ":global") {
			return true
		}
		if _, exists := seen[id]; exists {
			return true
		}
		seen[id] = struct{}{}
		displayName := strings.TrimSpace(value.Get("name").String())
		if displayName == "" {
			displayName = id
		}
		merged = append(merged, &registry.ModelInfo{
			ID:          id,
			DisplayName: displayName,
			OwnedBy:     "opencode",
			Type:        "opencode",
			Object:      "model",
			Created:     now,
		})
		return true
	})

	if len(merged) == 0 {
		return fallback
	}
	return merged
}
