package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const mimocodeModelsFetchTimeout = 15 * time.Second

// FetchMimocodeModels returns the live OpenAI-compatible catalog when available
// and the supported static catalog otherwise.
func FetchMimocodeModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	fallback := registry.GetMimocodeModels()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, mimocodeModelsFetchTimeout)
	defer cancel()
	prepared := prepareMimocodeAuth(auth)
	baseURL := strings.TrimRight(prepared.Attributes["base_url"], "/")
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
	if errRequest != nil {
		return fallback
	}
	request.Header.Set("Accept", "application/json")
	if apiKey := strings.TrimSpace(prepared.Attributes["api_key"]); apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	util.ApplyCustomHeadersFromAttrs(request, prepared.Attributes)
	response, errDo := newProxyAwareHTTPClient(ctx, cfg, prepared, 0).Do(request)
	if errDo != nil {
		return fallback
	}
	defer func() {
		if errClose := response.Body.Close(); errClose != nil {
			log.Debugf("mimocode: close models response body error: %v", errClose)
		}
	}()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fallback
	}
	body, errRead := io.ReadAll(response.Body)
	if errRead != nil {
		return fallback
	}
	models := parseMimocodeModels(body, fallback)
	if len(models) == 0 {
		return fallback
	}
	return models
}

func parseMimocodeModels(body []byte, fallback []*registry.ModelInfo) []*registry.ModelInfo {
	data := gjson.GetBytes(body, "data")
	if !data.IsArray() {
		data = gjson.ParseBytes(body)
		if !data.IsArray() {
			return nil
		}
	}
	staticByID := make(map[string]*registry.ModelInfo, len(fallback))
	for _, model := range fallback {
		if model != nil {
			staticByID[model.ID] = model
		}
	}
	seen := make(map[string]struct{})
	models := make([]*registry.ModelInfo, 0, len(fallback))
	data.ForEach(func(_, value gjson.Result) bool {
		id := strings.TrimSpace(value.Get("id").String())
		if id == "" {
			return true
		}
		if _, duplicate := seen[id]; duplicate {
			return true
		}
		seen[id] = struct{}{}
		if staticModel := staticByID[id]; staticModel != nil {
			clone := *staticModel
			models = append(models, &clone)
			return true
		}
		models = append(models, &registry.ModelInfo{
			ID: id, Name: id, Object: "model", OwnedBy: "xiaomi", Type: "mimocode",
			DisplayName: id, Created: time.Now().Unix(), SupportedEndpoints: []string{"/chat/completions"},
		})
		return true
	})
	return models
}
