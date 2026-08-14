package auth

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

const providerAuthContextKey = "cliproxy.provider_auth"
const GinProviderAuthKey = "providerAuth"
const fallbackInfoContextKey = "cliproxy.fallback_info"
const GinFallbackInfoKey = "fallbackInfo"
const billingDecisionContextKey = "cliproxy.billing_decision"
const GinBillingDecisionKey = "billingClassDecision"

func normalizeRuntimeBillingClass(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "metered":
		return "metered"
	case "per_request", "per-request":
		return "per-request"
	default:
		return ""
	}
}

func SetProviderAuthInContext(ctx context.Context, provider, authID, authLabel string) context.Context {
	authInfo := map[string]string{"provider": provider, "auth_id": authID, "auth_label": authLabel}
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil {
		ginCtx.Set(GinProviderAuthKey, authInfo)
	}
	return context.WithValue(ctx, providerAuthContextKey, authInfo)
}

func GetProviderAuthFromContext(ctx context.Context) (provider, authID, authLabel string) {
	if ctx == nil {
		return "", "", ""
	}
	if value, ok := ctx.Value(providerAuthContextKey).(map[string]string); ok {
		return value["provider"], value["auth_id"], value["auth_label"]
	}
	return "", "", ""
}

func SetFallbackInfoInContext(ctx context.Context, requestedModel, actualModel string) context.Context {
	if requestedModel == "" || actualModel == "" || requestedModel == actualModel {
		return ctx
	}
	info := map[string]string{"requested_model": requestedModel, "actual_model": actualModel}
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil {
		ginCtx.Set(GinFallbackInfoKey, info)
	}
	return context.WithValue(ctx, fallbackInfoContextKey, info)
}

func GetFallbackInfoFromContext(ctx context.Context) (requestedModel, actualModel string) {
	if ctx == nil {
		return "", ""
	}
	if value, ok := ctx.Value(fallbackInfoContextKey).(map[string]string); ok {
		return value["requested_model"], value["actual_model"]
	}
	return "", ""
}

func SetBillingDecisionInContext(ctx context.Context, billingClass, reason string) context.Context {
	decision := map[string]string{}
	if normalized := normalizeRuntimeBillingClass(billingClass); normalized != "" {
		decision["billing_class"] = normalized
	}
	if trimmed := strings.TrimSpace(reason); trimmed != "" {
		decision["reason"] = trimmed
	}
	if len(decision) == 0 {
		return ctx
	}
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil {
		ginCtx.Set(GinBillingDecisionKey, decision)
	}
	return context.WithValue(ctx, billingDecisionContextKey, decision)
}

func GetBillingDecisionFromContext(ctx context.Context) (billingClass, reason string) {
	if ctx == nil {
		return "", ""
	}
	if value, ok := ctx.Value(billingDecisionContextKey).(map[string]string); ok {
		return value["billing_class"], value["reason"]
	}
	return "", ""
}

func downstreamAPIKeyFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return ""
	}
	if ginCtx.Request != nil {
		if key := util.ExtractDownstreamAPIKey(ginCtx.Request.Header); key != "" {
			return key
		}
	}
	if raw, exists := ginCtx.Get("userApiKey"); exists {
		if key, isString := raw.(string); isString {
			if trimmed := strings.TrimSpace(key); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}
