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
//
// kiro-lb only calls ListAvailableModels for Builder ID (SSO OIDC without
// profileArn). Social/IdC/desktop tokens are issued for runtime.kiro.dev;
// sending them to q.{region}.amazonaws.com returns 403 invalid bearer.
func FetchKiroModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	if ctx == nil {
		ctx = context.Background()
	}

	accessToken, profileArn := kiroCredentials(auth)
	if accessToken == "" || cfg == nil {
		return nil
	}
	if isKiroRuntimeEndpoint(auth) {
		log.Debugf("kiro: using static models (ListAvailableModels is builder-id only)")
		return nil
	}

	tokenData := &kiroauth.KiroTokenData{
		AccessToken: accessToken,
		ProfileArn:  profileArn,
		AuthMethod:  kiroAuthMethod(auth),
	}
	if auth != nil && auth.Metadata != nil {
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

// isKiroRuntimeEndpoint reports hosts/accounts that do not expose
// ListAvailableModels. kiro-lb sets api_host to runtime.{region}.kiro.dev
// for every non-builder-id account; Builder ID stays on q.{region}.amazonaws.com.
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

// isKiroBuilderIDAuth matches kiro-lb: AWS_SSO_OIDC and not profile_arn.
// Explicit auth_method=builder-id wins even if a leftover profileArn is stored.
func isKiroBuilderIDAuth(auth *cliproxyauth.Auth) bool {
	if auth == nil {
		return false
	}
	switch kiroAuthMethod(auth) {
	case "builder-id", "builderid", "builder_id", "aws-builder-id", "aws_builder_id":
		return true
	case "social", "idc", "desktop", "google", "github":
		return false
	}
	authType := getAuthValue(auth, "auth_type")
	if authType == "aws_sso_oidc" || authType == "aws-sso-oidc" {
		return !kiroHasProfileArn(auth)
	}
	if kiroHasProfileArn(auth) {
		return false
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
