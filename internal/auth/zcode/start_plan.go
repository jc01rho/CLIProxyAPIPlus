package zcode

import (
	"context"
	"fmt"
	"strings"
)

// DefaultStartPlanBalanceURL is the ZCode gateway billing/balance endpoint.
// Verified from the ZCode desktop app (app.asar buildZCodeEndpointUrls +
// Fd/Ld: zcodePlanBillingBalanceUrl on the zcode.z.ai origin). The app
// authenticates this call with the broker zcode JWT (credential key
// "zcodejwttoken"), not with the provisioned Z.AI API key.
const DefaultStartPlanBalanceURL = "https://zcode.z.ai/api/v1/zcode-plan/billing/balance"

// StartPlanInfo holds the result of a start plan billing balance check.
type StartPlanInfo struct {
	Active bool
	Limit  int64 // start plan quota limit (0 if unknown)
	Used   int64 // start plan usage so far (0 if unknown)
}

// CheckStartPlan queries the ZCode gateway billing/balance endpoint to detect
// whether a start plan is active. Verified from app.asar hasActiveStartPlan
// (Pae): data.plans[] entries with status "active" and a plan_id or name
// containing "start-plan" (or, when both identifiers are absent, any active
// plan counts, matching the app's isZaiStartPlanIdentity fallback). The broker
// zcode JWT must be passed as the bearer token. Returns nil when the request
// fails or the response carries no plans.
func (o *OAuth) CheckStartPlan(ctx context.Context, zcodeJWT string) *StartPlanInfo {
	var resp struct {
		Data struct {
			Plans []struct {
				Status string         `json:"status"`
				PlanID string         `json:"plan_id"`
				Name   string         `json:"name"`
				Limit  FlexibleString `json:"limit"`
				Used   FlexibleString `json:"used"`
			} `json:"plans"`
		} `json:"data"`
	}
	// A failed probe is not evidence of entitlement. The ZCode app decides start
	// plan availability from an explicit entitlement check and marks the provider
	// "unavailable" (reason coding_plan_not_entitled) when it does not hold one,
	// rather than routing optimistically and learning from a rejection
	// (app.asar resolveCodingPlanAvailability).
	//
	// Assuming Active on failure pins every account to the zcode-plan gateway,
	// and because routing swaps the credential to the broker JWT there is no
	// second attempt with the provisioned api.z.ai key: an account without a
	// start plan then fails every request with 401.
	if err := o.getJSON(ctx, o.startPlanBalanceURL, zcodeJWT, "billing/balance", &resp); err != nil {
		return nil
	}
	for _, p := range resp.Data.Plans {
		status := strings.ToLower(strings.TrimSpace(p.Status))
		if status != "active" {
			continue
		}
		planID := strings.ToLower(strings.TrimSpace(p.PlanID))
		name := strings.ToLower(strings.TrimSpace(p.Name))
		// App fallback: when both identifiers are missing, any active plan is
		// treated as the start plan identity.
		if planID == "" && name == "" {
			spi := &StartPlanInfo{Active: true}
			spi.Limit = parseStartPlanCount(p.Limit)
			spi.Used = parseStartPlanCount(p.Used)
			return spi
		}
		if strings.Contains(planID, "start-plan") || strings.Contains(planID, "start plan") ||
			strings.Contains(name, "start-plan") || strings.Contains(name, "start plan") {
			spi := &StartPlanInfo{Active: true}
			spi.Limit = parseStartPlanCount(p.Limit)
			spi.Used = parseStartPlanCount(p.Used)
			return spi
		}
	}
	return &StartPlanInfo{Active: false}
}

// parseStartPlanCount parses a plan limit/used value that may be numeric or a
// human string; returns 0 when unknown.
func parseStartPlanCount(raw FlexibleString) int64 {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return 0
	}
	var n float64
	if _, err := fmt.Sscanf(s, "%g", &n); err != nil || n <= 0 {
		return 0
	}
	return int64(n)
}
