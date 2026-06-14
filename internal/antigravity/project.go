package antigravity

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const projectContextCacheTTL = 30 * time.Minute

// OAuthAuthDetails mirrors the Antigravity OAuth auth details shape.
type OAuthAuthDetails struct {
	Type    string `json:"type"`
	Refresh string `json:"refresh"`
	Access  string `json:"access,omitempty"`
	Expires int64  `json:"expires,omitempty"`
}

// RefreshParts contains the segments packed into an OAuth refresh value.
type RefreshParts struct {
	RefreshToken     string
	ProjectID        string
	ManagedProjectID string
}

// ProjectContextResult contains the updated auth value and effective project ID.
type ProjectContextResult struct {
	Auth               OAuthAuthDetails
	EffectiveProjectID string
}

// AntigravityUserTier represents an Antigravity allowed tier entry.
type AntigravityUserTier struct {
	ID                                 string `json:"id,omitempty"`
	IsDefault                          bool   `json:"isDefault,omitempty"`
	UserDefinedCloudaicompanionProject bool   `json:"userDefinedCloudaicompanionProject,omitempty"`
}

// LoadCodeAssistPayload is the subset of loadCodeAssist data used for project resolution.
type LoadCodeAssistPayload struct {
	CloudaicompanionProject json.RawMessage         `json:"cloudaicompanionProject,omitempty"`
	CurrentTier             *AntigravityCurrentTier `json:"currentTier,omitempty"`
	AllowedTiers            []AntigravityUserTier   `json:"allowedTiers,omitempty"`
}

// AntigravityCurrentTier represents the current tier block in loadCodeAssist responses.
type AntigravityCurrentTier struct {
	ID string `json:"id,omitempty"`
}

type onboardUserPayload struct {
	Done     bool `json:"done,omitempty"`
	Response struct {
		CloudaicompanionProject struct {
			ID string `json:"id,omitempty"`
		} `json:"cloudaicompanionProject,omitempty"`
	} `json:"response,omitempty"`
}

type cachedProjectContext struct {
	result   ProjectContextResult
	cachedAt time.Time
}

type pendingProjectContext struct {
	done   chan struct{}
	result ProjectContextResult
}

var (
	projectContextMu           sync.Mutex
	projectContextResultCache  = make(map[string]cachedProjectContext)
	projectContextPendingCache = make(map[string]*pendingProjectContext)
	provisionFailedKeys        = make(map[string]struct{})
)

func buildBootstrapRequestBody(extra map[string]any) map[string]any {
	requestBody := make(map[string]any, len(extra)+1)
	for key, value := range extra {
		requestBody[key] = value
	}
	requestBody["metadata"] = BuildAntigravityLoadCodeAssistMetadata()
	return requestBody
}

func getDefaultTierID(allowedTiers []AntigravityUserTier) string {
	if len(allowedTiers) == 0 {
		return ""
	}
	for _, tier := range allowedTiers {
		if tier.IsDefault {
			return tier.ID
		}
	}
	return allowedTiers[0].ID
}

func wait(ctx context.Context, delay time.Duration) {
	if delay <= 0 {
		return
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func extractManagedProjectID(payload *LoadCodeAssistPayload) string {
	if payload == nil || len(payload.CloudaicompanionProject) == 0 || string(payload.CloudaicompanionProject) == "null" {
		return ""
	}
	var projectString string
	if err := json.Unmarshal(payload.CloudaicompanionProject, &projectString); err == nil {
		return projectString
	}
	var projectObject struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(payload.CloudaicompanionProject, &projectObject); err == nil {
		return projectObject.ID
	}
	return ""
}

func getCacheKey(auth OAuthAuthDetails) string {
	refresh := strings.TrimSpace(auth.Refresh)
	if refresh == "" {
		return ""
	}
	return refresh
}

// ParseRefreshParts splits a packed refresh string into its refresh token and project IDs.
func ParseRefreshParts(refresh string) RefreshParts {
	parts := strings.Split(refresh, "|")
	for len(parts) < 3 {
		parts = append(parts, "")
	}
	return RefreshParts{
		RefreshToken:     parts[0],
		ProjectID:        parts[1],
		ManagedProjectID: parts[2],
	}
}

// FormatRefreshParts serializes refresh token parts into the stored string format.
func FormatRefreshParts(parts RefreshParts) string {
	base := parts.RefreshToken + "|" + parts.ProjectID
	if parts.ManagedProjectID != "" {
		return base + "|" + parts.ManagedProjectID
	}
	return base
}

// InvalidateProjectContextCache clears cached project context globally or for a refresh key.
func InvalidateProjectContextCache(refresh string) {
	projectContextMu.Lock()
	defer projectContextMu.Unlock()
	if refresh == "" {
		projectContextPendingCache = make(map[string]*pendingProjectContext)
		projectContextResultCache = make(map[string]cachedProjectContext)
		provisionFailedKeys = make(map[string]struct{})
		return
	}
	delete(projectContextPendingCache, refresh)
	delete(projectContextResultCache, refresh)
	delete(provisionFailedKeys, refresh)
}

// ClearProvisionFailedKeys clears the managed-project provisioning failure memo.
func ClearProvisionFailedKeys() {
	projectContextMu.Lock()
	defer projectContextMu.Unlock()
	provisionFailedKeys = make(map[string]struct{})
}

// LoadManagedProject loads managed project information for the access token.
func LoadManagedProject(ctx context.Context, accessToken string, projectID string) *LoadCodeAssistPayload {
	_ = projectID
	if ctx == nil {
		ctx = context.Background()
	}
	rawBody, errMarshal := json.Marshal(buildBootstrapRequestBody(nil))
	if errMarshal != nil {
		log.WithError(errMarshal).Debug("Failed to marshal managed project request")
		return nil
	}
	loadHeaders := BuildAntigravityHarnessBootstrapHeaders(accessToken)
	loadEndpoints := uniqueStrings(append(append([]string{}, AntigravityLoadEndpoints...), AntigravityEndpointFallbacks...))

	for _, baseEndpoint := range loadEndpoints {
		response, err := FetchWithAgyCLITransport(ctx, baseEndpoint+"/v1internal:loadCodeAssist", AgyRequestInit{
			Method:  "POST",
			Headers: loadHeaders,
			Body:    rawBody,
		}, AgyTransportOptions{})
		if err != nil {
			log.WithFields(log.Fields{"endpoint": baseEndpoint, "error": err.Error()}).Debug("Failed to load managed project")
			continue
		}

		payload, ok := decodeLoadManagedProjectResponse(response, baseEndpoint)
		if ok {
			return payload
		}
	}
	return nil
}

func decodeLoadManagedProjectResponse(response *http.Response, baseEndpoint string) (*LoadCodeAssistPayload, bool) {
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, false
	}
	var payload LoadCodeAssistPayload
	if errDecode := json.NewDecoder(response.Body).Decode(&payload); errDecode != nil {
		log.WithFields(log.Fields{"endpoint": baseEndpoint, "error": errDecode.Error()}).Debug("Failed to decode managed project")
		return nil, false
	}
	return &payload, true
}

// OnboardManagedProject onboards a managed project, retrying until completion when needed.
func OnboardManagedProject(ctx context.Context, accessToken, tierID, projectID string, attempts int, delay time.Duration) string {
	if ctx == nil {
		ctx = context.Background()
	}
	if attempts <= 0 {
		attempts = 10
	}
	if delay <= 0 {
		delay = 5 * time.Second
	}
	requestBody := map[string]any{"tierId": tierID}
	rawBody, errMarshal := json.Marshal(requestBody)
	if errMarshal != nil {
		log.WithError(errMarshal).Debug("Failed to marshal onboard request")
		return ""
	}
	onboardEndpoints := uniqueStrings(append([]string{AntigravityEndpointProd}, append(AntigravityLoadEndpoints, AntigravityEndpointFallbacks...)...))

	for _, baseEndpoint := range onboardEndpoints {
		for attempt := 0; attempt < attempts; attempt++ {
			response, err := FetchWithAgyCLITransport(ctx, baseEndpoint+"/v1internal:onboardUser", AgyRequestInit{
				Method:  "POST",
				Headers: BuildAntigravityHarnessBootstrapHeaders(accessToken),
				Body:    rawBody,
			}, AgyTransportOptions{})
			if err != nil {
				log.WithFields(log.Fields{"endpoint": baseEndpoint, "error": err.Error()}).Debug("Failed to onboard managed project")
				break
			}

			managedProjectID, shouldRetry := decodeOnboardManagedProjectResponse(response, baseEndpoint, projectID)
			if managedProjectID != "" {
				return managedProjectID
			}
			if !shouldRetry {
				break
			}
			wait(ctx, delay)
		}
	}
	return ""
}

func decodeOnboardManagedProjectResponse(response *http.Response, baseEndpoint string, projectID string) (string, bool) {
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		log.WithFields(log.Fields{"endpoint": baseEndpoint, "status": response.StatusCode, "statusText": response.Status}).Debug("Onboard request failed")
		return "", false
	}
	var payload onboardUserPayload
	if errDecode := json.NewDecoder(response.Body).Decode(&payload); errDecode != nil {
		log.WithFields(log.Fields{"endpoint": baseEndpoint, "error": errDecode.Error()}).Debug("Failed to decode onboard response")
		return "", false
	}
	managedProjectID := payload.Response.CloudaicompanionProject.ID
	if payload.Done && managedProjectID != "" {
		return managedProjectID, false
	}
	if payload.Done && projectID != "" {
		return projectID, false
	}
	return "", true
}

// EnsureProjectContext resolves the effective project ID for the current auth state.
func EnsureProjectContext(ctx context.Context, auth OAuthAuthDetails) ProjectContextResult {
	if ctx == nil {
		ctx = context.Background()
	}
	accessToken := auth.Access
	if accessToken == "" {
		return ProjectContextResult{Auth: auth, EffectiveProjectID: ""}
	}

	cacheKey := getCacheKey(auth)
	if cacheKey != "" {
		projectContextMu.Lock()
		cached, hasCached := projectContextResultCache[cacheKey]
		if hasCached && time.Since(cached.cachedAt) < projectContextCacheTTL {
			projectContextMu.Unlock()
			return cached.result
		}
		if hasCached {
			delete(projectContextResultCache, cacheKey)
		}
		if pending := projectContextPendingCache[cacheKey]; pending != nil {
			projectContextMu.Unlock()
			<-pending.done
			return pending.result
		}
		pending := &pendingProjectContext{done: make(chan struct{})}
		projectContextPendingCache[cacheKey] = pending
		projectContextMu.Unlock()

		result := resolveProjectContext(ctx, auth, accessToken, cacheKey)
		pending.result = result
		projectContextMu.Lock()
		delete(projectContextPendingCache, cacheKey)
		nextKey := getCacheKey(result.Auth)
		if nextKey == "" {
			nextKey = cacheKey
		}
		projectContextResultCache[nextKey] = cachedProjectContext{result: result, cachedAt: time.Now()}
		if nextKey != cacheKey {
			delete(projectContextResultCache, cacheKey)
		}
		close(pending.done)
		projectContextMu.Unlock()
		return result
	}

	return resolveProjectContext(ctx, auth, accessToken, cacheKey)
}

func resolveProjectContext(ctx context.Context, auth OAuthAuthDetails, accessToken string, cacheKey string) ProjectContextResult {
	parts := ParseRefreshParts(auth.Refresh)
	if parts.ManagedProjectID != "" {
		return ProjectContextResult{Auth: auth, EffectiveProjectID: parts.ManagedProjectID}
	}
	fallbackProjectID := AntigravityDefaultProjectID

	if cacheKey != "" {
		projectContextMu.Lock()
		_, failed := provisionFailedKeys[cacheKey]
		projectContextMu.Unlock()
		if failed {
			effectiveProjectID := parts.ProjectID
			if effectiveProjectID == "" {
				effectiveProjectID = fallbackProjectID
			}
			return ProjectContextResult{Auth: auth, EffectiveProjectID: effectiveProjectID}
		}
	}

	persistManagedProject := func(managedProjectID string) ProjectContextResult {
		updatedAuth := auth
		updatedAuth.Refresh = FormatRefreshParts(RefreshParts{
			RefreshToken:     parts.RefreshToken,
			ProjectID:        parts.ProjectID,
			ManagedProjectID: managedProjectID,
		})
		return ProjectContextResult{Auth: updatedAuth, EffectiveProjectID: managedProjectID}
	}

	projectIDForLoad := parts.ProjectID
	if projectIDForLoad == "" {
		projectIDForLoad = fallbackProjectID
	}
	loadPayload := LoadManagedProject(ctx, accessToken, projectIDForLoad)
	if resolvedManagedProjectID := extractManagedProjectID(loadPayload); resolvedManagedProjectID != "" {
		return persistManagedProject(resolvedManagedProjectID)
	}

	tierID := getDefaultTierID(nil)
	if loadPayload != nil {
		tierID = getDefaultTierID(loadPayload.AllowedTiers)
	}
	if tierID == "" {
		tierID = "free-tier"
	}
	log.WithFields(log.Fields{"tierId": tierID, "projectId": parts.ProjectID}).Debug("Auto-provisioning managed project")
	provisionedProjectID := OnboardManagedProject(ctx, accessToken, tierID, parts.ProjectID, 10, 5*time.Second)
	if provisionedProjectID != "" {
		log.WithField("provisionedProjectId", provisionedProjectID).Debug("Successfully provisioned managed project")
		return persistManagedProject(provisionedProjectID)
	}

	log.WithField("hasProjectId", parts.ProjectID != "").Warn("Failed to provision managed project - account may not work correctly")
	if cacheKey != "" {
		projectContextMu.Lock()
		provisionFailedKeys[cacheKey] = struct{}{}
		projectContextMu.Unlock()
	}
	if parts.ProjectID != "" {
		return ProjectContextResult{Auth: auth, EffectiveProjectID: parts.ProjectID}
	}
	return ProjectContextResult{Auth: auth, EffectiveProjectID: fallbackProjectID}
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
