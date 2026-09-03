package executor

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// commandCodeModelEffortProfile is one model's documented reasoning-effort
// ladder plus the public profile page that states it. Ported from the opencodex
// fork's command-code-efforts.ts table.
//
// Keys must match the EXACT upstream /provider/v1/models ids (GLM ships as
// `zai-org/GLM-5.3`, not `zai-org/glm-5.3`). Lookup is case-insensitive so
// callers may pass either case, but a case mismatch in the table itself would
// silently drop the effort for that model.
//
// These are capability facts from official Command Code model profiles, not a
// model catalog. Models remain account-scoped and come exclusively from the
// authenticated /provider/v1/models endpoint. A model absent from this table
// deliberately advertises no reasoning picker: an effort the upstream rejects
// surfaces as an error rather than silent corruption.
type commandCodeModelEffortProfile struct {
	efforts    []string
	profileURL string
}

var commandCodeModelEffortProfiles = map[string]commandCodeModelEffortProfile{
	"deepseek/deepseek-v4-pro":              {efforts: []string{"high", "max"}, profileURL: "https://commandcode.ai/models/deepseek-v4-pro"},
	"deepseek/deepseek-v4-flash":            {efforts: []string{"high", "max"}, profileURL: "https://commandcode.ai/models/deepseek-v4-flash"},
	"deepseek/deepseek-v4-flash-vision-exp": {efforts: []string{"high", "max"}, profileURL: "https://commandcode.ai/models/deepseek-v4-flash-vision-exp"},
	"gpt-5.6-luna":                          {efforts: []string{"low", "medium", "high", "xhigh", "max"}, profileURL: "https://commandcode.ai/models/gpt-5-6-luna"},
	"google/gemini-3.7-flash":               {efforts: []string{"low", "medium", "high"}, profileURL: "https://commandcode.ai/models/gemini-3-7-flash"},
	"zai-org/GLM-5":                         {efforts: []string{"high", "max"}, profileURL: "https://commandcode.ai/models/glm-5"},
	"zai-org/GLM-5.1":                       {efforts: []string{"high", "max"}, profileURL: "https://commandcode.ai/models/glm-5-1"},
	"zai-org/GLM-5.2":                       {efforts: []string{"high", "max"}, profileURL: "https://commandcode.ai/models/glm-5-2"},
	"zai-org/GLM-5.2-Fast":                  {efforts: []string{"high", "max"}, profileURL: "https://commandcode.ai/models/glm-5-2-fast"},
	"zai-org/GLM-5.3":                       {efforts: []string{"low", "high", "max"}, profileURL: "https://commandcode.ai/models/glm-5-3"},
	"z-ai/glm-5.3-flash":                    {efforts: []string{"low", "high", "max"}, profileURL: "https://commandcode.ai/models/glm-5-3-flash"},
	"meta/muse-spark-1.1":                   {efforts: []string{"low", "medium", "high", "xhigh", "max"}, profileURL: "https://commandcode.ai/models/meta-muse-spark-1.1"},
	"meta/muse-spark-1.2":                   {efforts: []string{"low", "medium", "high", "xhigh", "max"}, profileURL: "https://commandcode.ai/models/meta-muse-spark-1.2"},
	"meta/muse-spark-1.2-contributor":       {efforts: []string{"low", "medium", "high", "xhigh", "max"}, profileURL: "https://commandcode.ai/models/meta-muse-spark-1.2-contributor"},
}

// refreshedEfforts holds ladders refreshed from live profile pages; a refreshed
// entry takes precedence over the static table for that model.
var refreshedEfforts = map[string][]string{}

// commandCodeProfileFetchTimeout bounds the profile-page fetch on the request
// path so a degraded host cannot stall a rejected request indefinitely.
const commandCodeProfileFetchTimeout = 10 * time.Second

// commandCodeProfileMaxBytes bounds the profile page read before parsing.
const commandCodeProfileMaxBytes = 256 * 1024

// commandCodeReasoningEfforts returns the supported effort ladder for a model,
// or nil when the model has no documented ladder. Lookup is case-insensitive;
// a refreshed ladder (from a live profile page) wins over the static table.
func commandCodeReasoningEfforts(modelID string) []string {
	key := strings.ToLower(strings.TrimSpace(modelID))
	if efforts, ok := refreshedEfforts[key]; ok {
		return efforts
	}
	for id, profile := range commandCodeModelEffortProfiles {
		if strings.ToLower(id) == key {
			return profile.efforts
		}
	}
	return nil
}

// commandCodeProfileURL returns the public profile page for a model, or ""
// when the model has no documented profile.
func commandCodeProfileURL(modelID string) string {
	key := strings.ToLower(strings.TrimSpace(modelID))
	for id, profile := range commandCodeModelEffortProfiles {
		if strings.ToLower(id) == key {
			return profile.profileURL
		}
	}
	return ""
}

// commandCodeEffortSupported reports whether value is in the supported ladder.
func commandCodeEffortSupported(supported []string, value string) bool {
	for _, e := range supported {
		if e == value {
			return true
		}
	}
	return false
}

// commandCodeSupportedEffort resolves the wire reasoning_effort value for a
// model, or "" when the requested effort is not supported (so the field is
// omitted from the wire). Mirrors opencodex's supportedCommandCodeEffort:
//
//   - an empty or "none" request never sends the field;
//   - a model with no documented ladder never sends the field;
//   - xhigh collapses to max when the model documents max but not xhigh;
//   - ultra collapses to max only for the models whose official profile
//     documents that aliasing (deepseek v4 pro/flash, GLM-5.2); Muse Spark's
//     upstream accepts xhigh as a distinct wire value and rejects ultra, so it
//     must not be collapsed.
func commandCodeSupportedEffort(modelID, requested string) string {
	if requested == "" || requested == "none" {
		return ""
	}
	supported := commandCodeReasoningEfforts(modelID)
	if len(supported) == 0 {
		return ""
	}
	wire := requested
	lower := strings.ToLower(modelID)
	needsAlias := lower == "deepseek/deepseek-v4-pro" ||
		lower == "deepseek/deepseek-v4-flash" ||
		lower == "zai-org/glm-5.2"
	if requested == "xhigh" && !commandCodeEffortSupported(supported, "xhigh") && commandCodeEffortSupported(supported, "max") {
		wire = "max"
	} else if requested == "ultra" && needsAlias && commandCodeEffortSupported(supported, "max") {
		wire = "max"
	}
	if commandCodeEffortSupported(supported, wire) {
		return wire
	}
	return ""
}

// commandCodeIsReasoningEffortRejection reports whether a 400/422 response body
// indicates a reasoning-effort rejection (mirrors opencodex's
// isReasoningEffortRejection).
func commandCodeIsReasoningEffortRejection(status int, body string) bool {
	if status != http.StatusBadRequest && status != http.StatusUnprocessableEntity {
		return false
	}
	return commandCodeEffortRejectionRe.MatchString(body)
}

var commandCodeEffortRejectionRe = regexp.MustCompile(`(?i)reasoning[_ -]?effort|unsupported effort|invalid effort`)

// commandCodeParsedProfileEfforts parses the "Reasoning efforts ... are
// supported;" prose from a model profile page, applying any "X maps to Y"
// remaps. Returns nil when the page carries no parseable ladder.
func commandCodeParsedProfileEfforts(page string) []string {
	match := commandCodeProfileEffortsRe.FindStringSubmatch(page)
	if match == nil {
		return nil
	}
	listed := commandCodeEffortTokenRe.FindAllString(strings.ToLower(match[1]), -1)
	normalized := make(map[string]struct{}, len(listed))
	for _, e := range listed {
		normalized[e] = struct{}{}
	}
	for _, m := range commandCodeEffortMappingRe.FindAllStringSubmatch(strings.ToLower(match[2]), -1) {
		if len(m) == 3 {
			delete(normalized, m[1])
			normalized[m[2]] = struct{}{}
		}
	}
	if len(normalized) == 0 {
		return nil
	}
	out := make([]string, 0, len(normalized))
	for e := range normalized {
		out = append(out, e)
	}
	return out
}

var (
	commandCodeProfileEffortsRe = regexp.MustCompile(`(?i)Reasoning efforts\s+([^.;]+?)\s+are supported;\s*([^.]*)`)
	commandCodeEffortTokenRe    = regexp.MustCompile(`(?i)\b(?:low|medium|high|xhigh|max)\b`)
	commandCodeEffortMappingRe  = regexp.MustCompile(`(?i)\b(low|medium|high|xhigh|max)\s+maps to\s+(low|medium|high|xhigh|max)\b`)
)

// commandCodeRefreshReasoningEfforts fetches the model's profile page and, when
// it parses a ladder, updates the refreshed table. Returns the refreshed
// ladder, or nil when the fetch/parse failed (the static table is kept).
func commandCodeRefreshReasoningEfforts(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, modelID string) []string {
	url := commandCodeProfileURL(modelID)
	if url == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, commandCodeProfileFetchTimeout)
	defer cancel()
	httpClient := newProxyAwareHTTPClient(ctx, cfg, auth, 0)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "text/html")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("commandcode: close profile response body error: %v", errClose)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, commandCodeProfileMaxBytes))
	if err != nil {
		return nil
	}
	efforts := commandCodeParsedProfileEfforts(string(body))
	if efforts == nil {
		return nil
	}
	key := strings.ToLower(strings.TrimSpace(modelID))
	refreshedEfforts[key] = efforts
	return efforts
}

// commandCodeResetRefreshedEffortsForTest clears the refreshed-effort cache.
func commandCodeResetRefreshedEffortsForTest() {
	refreshedEfforts = map[string][]string{}
}
