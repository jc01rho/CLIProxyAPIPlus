package executor

import (
	"context"
	"strings"
	"time"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// FetchKiroModels retrieves available models from the CodeWhisperer API
// (ListAvailableModels). On missing credentials or fetch failure it returns
// nil so the caller can fall back to the provider-scoped static catalog
// (GetKiroModels / GetAmazonQModels).
//
// runtime.kiro.dev does not implement ListAvailableModels (kiro-lb). Live
// success overlays static metadata onto live IDs only. Static-only fabricated
// IDs (auto / opus-4.x / gpt-4*) are not appended.
func FetchKiroModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	if ctx == nil {
		ctx = context.Background()
	}

	accessToken, profileArn := kiroCredentials(auth)
	if accessToken == "" || cfg == nil {
		return nil
	}
	if isKiroRuntimeEndpoint(auth) {
		log.Debugf("kiro: using static models (runtime.kiro.dev does not provide ListAvailableModels)")
		return nil
	}

	tokenData := &kiroauth.KiroTokenData{
		AccessToken: accessToken,
		ProfileArn:  profileArn,
	}
	if auth != nil && auth.Metadata != nil {
		if v, ok := auth.Metadata["auth_method"].(string); ok {
			tokenData.AuthMethod = v
		}
		if v, ok := auth.Metadata["client_id"].(string); ok {
			tokenData.ClientID = v
		}
		if v, ok := auth.Metadata["refresh_token"].(string); ok {
			tokenData.RefreshToken = v
		}
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	kiroAuth := kiroauth.NewKiroAuth(cfg)
	kiroModels, err := kiroAuth.ListAvailableModels(ctx, tokenData)
	if err != nil {
		log.Warnf("kiro: using static models (ListAvailableModels failed: %v)", err)
		return nil
	}
	if len(kiroModels) == 0 {
		return nil
	}

	apiModels := make([]*registry.KiroAPIModel, 0, len(kiroModels))
	for _, km := range kiroModels {
		if km == nil || km.ModelID == "" {
			continue
		}
		apiModels = append(apiModels, &registry.KiroAPIModel{
			ModelID:        km.ModelID,
			ModelName:      km.ModelName,
			Description:    km.Description,
			RateMultiplier: km.RateMultiplier,
			RateUnit:       km.RateUnit,
			MaxInputTokens: km.MaxInputTokens,
		})
	}

	dynamicModels := registry.ConvertKiroAPIModels(apiModels)
	dynamicModels = registry.GenerateAgenticVariants(dynamicModels)
	return FilterKiroModels(registry.OverlayStaticMetadata(dynamicModels, registry.GetKiroModels()))
}

// isKiroRuntimeEndpoint reports desktop/runtime hosts that do not expose
// ListAvailableModels. Builder ID stays on q.{region}.amazonaws.com.
func isKiroRuntimeEndpoint(auth *cliproxyauth.Auth) bool {
	for _, key := range []string{"api_host", "q_host", "endpoint", "base_url"} {
		if strings.Contains(getAuthValue(auth, key), "://runtime.") {
			return true
		}
	}
	return false
}
