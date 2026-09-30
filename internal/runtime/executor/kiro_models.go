package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// FetchKiroModels retrieves available models from the CodeWhisperer API
// (ListAvailableModels). On missing credentials or fetch failure it returns
// nil so the caller can fall back to the provider-scoped static catalog
// (GetKiroModels / GetAmazonQModels).
//
// Live success overlays static metadata onto live IDs only. Static-only
// fabricated IDs (auto / opus-4.x / gpt-4*) are not appended. The management
// API accepts every Kiro account kind; failures deliberately fall back to the
// provider-scoped static catalog. Ported from kiro-lb src/model_catalog.rs
// (1581af9).
func FetchKiroModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	if ctx == nil {
		ctx = context.Background()
	}

	accessToken, profileArn := kiroCredentials(auth)
	if accessToken == "" || cfg == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	apiModels, err := fetchKiroManagementModels(ctx, auth, cfg, profileArn)
	if err != nil {
		log.Warnf("kiro: using static models (ListAvailableModels failed: %v)", err)
		return nil
	}

	dynamicModels := registry.ConvertKiroAPIModels(apiModels)
	dynamicModels = registry.GenerateAgenticVariants(dynamicModels)
	return FilterKiroModels(registry.OverlayStaticMetadata(dynamicModels, registry.GetKiroModels()))
}

func fetchKiroManagementModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config, profileArn string) ([]*registry.KiroAPIModel, error) {
	payload := map[string]string{"origin": "AI_EDITOR"}
	if arn := getEffectiveProfileArnWithWarning(auth, profileArn); arn != "" {
		payload["profileArn"] = arn
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("https://management.%s.kiro.dev/", resolveKiroAPIRegion(auth)), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", kiroContentType)
	req.Header.Set("X-Amz-Target", "KiroControlPlaneBearerService.ListAvailableModels")
	resp, err := NewKiroExecutor(cfg).HttpRequest(ctx, auth, req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Warnf("kiro: close models response: %v", err)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("management host answered %d", resp.StatusCode)
	}
	type model struct {
		registry.KiroAPIModel
		TokenLimits struct {
			MaxInputTokens int `json:"maxInputTokens"`
		} `json:"tokenLimits"`
	}
	var response struct {
		Models          []model `json:"models"`
		AvailableModels []model `json:"availableModels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}
	if len(response.Models) == 0 {
		response.Models = response.AvailableModels
	}
	var models []*registry.KiroAPIModel
	for _, item := range response.Models {
		m := item.KiroAPIModel
		if m.ModelID == "" {
			continue
		}
		if item.TokenLimits.MaxInputTokens > 0 {
			m.MaxInputTokens = item.TokenLimits.MaxInputTokens
		}
		models = append(models, &m)
	}
	return models, nil
}

// This distinction remains useful for region resolution, not catalog support.
func isKiroRuntimeEndpoint(auth *cliproxyauth.Auth) bool {
	if auth == nil {
		return false
	}
	for _, key := range []string{"api_host", "q_host", "endpoint", "base_url"} {
		if strings.Contains(getAuthValue(auth, key), "://runtime.") {
			return true
		}
	}
	return !isKiroBuilderIDAuth(auth)
}

func isKiroBuilderIDAuth(auth *cliproxyauth.Auth) bool {
	if auth == nil || kiroHasProfileArn(auth) {
		return false
	}
	switch kiroAuthMethod(auth) {
	case "builder-id", "builderid", "builder_id", "aws-builder-id", "aws_builder_id":
		return true
	case "social", "desktop", "google", "github":
		return false
	}
	authType := getAuthValue(auth, "auth_type")
	if authType == "aws_sso_oidc" || authType == "aws-sso-oidc" {
		return true
	}
	return getAuthValue(auth, "client_id") != "" && getAuthValue(auth, "client_secret") != ""
}

func kiroAuthMethod(auth *cliproxyauth.Auth) string {
	if v := getAuthValue(auth, "auth_method"); v != "" {
		return v
	}
	return getAuthValue(auth, "authMethod")
}

func kiroHasProfileArn(auth *cliproxyauth.Auth) bool {
	_, arn := kiroCredentials(auth)
	if strings.TrimSpace(arn) != "" {
		return true
	}
	for _, key := range []string{"profile_arn", "profileArn"} {
		if strings.TrimSpace(getAuthValue(auth, key)) != "" {
			return true
		}
	}
	return false
}
