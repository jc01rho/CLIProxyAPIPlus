package copilot

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
)

// copilotPolicyConcurrency mirrors senpi's COPILOT_POLICY_CONCURRENCY: model
// policy enablement requests run in small batches so login stays fast without
// hammering the Copilot API.
const copilotPolicyConcurrency = 4

// EnableModelPolicy enables a model for the user's GitHub Copilot account.
// Some models (Claude, Grok, ...) require accepting a usage policy before
// they can be used; this call performs that acceptance programmatically.
//
// Mirrors senpi's enableGitHubCopilotModel(): POST {base}/models/{id}/policy
// with {"state":"enabled"} and the chat-policy intent headers.
func (c *CopilotAuth) EnableModelPolicy(ctx context.Context, apiToken *CopilotAPIToken, modelID string) error {
	if apiToken == nil || strings.TrimSpace(apiToken.Token) == "" {
		return fmt.Errorf("copilot: api token is required to enable model policy")
	}
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return fmt.Errorf("copilot: model id is required to enable model policy")
	}

	baseURL := strings.TrimRight(c.ResolveAPIBaseURL(apiToken), "/")
	policyURL := fmt.Sprintf("%s/models/%s/policy", baseURL, modelID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, policyURL, strings.NewReader(`{"state":"enabled"}`))
	if err != nil {
		return fmt.Errorf("copilot: failed to create model policy request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiToken.Token)
	req.Header.Set("User-Agent", copilotUserAgent)
	req.Header.Set("Editor-Version", copilotEditorVersion)
	req.Header.Set("Editor-Plugin-Version", copilotPluginVersion)
	req.Header.Set("Copilot-Integration-Id", copilotIntegrationID)
	req.Header.Set("Openai-Intent", "chat-policy")
	req.Header.Set("X-Interaction-Type", "chat-policy")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("copilot: model policy request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			return
		}
	}()

	if !isHTTPSuccess(resp.StatusCode) {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return fmt.Errorf("copilot: enable model policy for %s failed: status %d: %s", modelID, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// EnableAllModelPolicies enables every model in modelIDs on a best-effort
// basis, running at most copilotPolicyConcurrency requests at a time. It
// returns the number of models that were enabled successfully; individual
// failures are collected but never abort the batch (some models cannot be
// self-enabled, e.g. organization-managed policies).
//
// Mirrors senpi's enableAllGitHubCopilotModels().
func (c *CopilotAuth) EnableAllModelPolicies(ctx context.Context, apiToken *CopilotAPIToken, modelIDs []string) int {
	if len(modelIDs) == 0 {
		return 0
	}

	var (
		mu      sync.Mutex
		enabled int
		wg      sync.WaitGroup
	)
	sem := make(chan struct{}, copilotPolicyConcurrency)
	for _, modelID := range modelIDs {
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := c.EnableModelPolicy(ctx, apiToken, id); err != nil {
				log.Debugf("copilot: enable model policy for %s skipped: %v", id, err)
				return
			}
			mu.Lock()
			enabled++
			mu.Unlock()
		}(modelID)
	}
	wg.Wait()
	return enabled
}

// EnableAllModelsForEntries is a convenience wrapper that enables the policy
// for every model entry returned by the Copilot /models endpoint.
func (c *CopilotAuth) EnableAllModelsForEntries(ctx context.Context, apiToken *CopilotAPIToken, entries []CopilotModelEntry) int {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.ID) != "" {
			ids = append(ids, entry.ID)
		}
	}
	return c.EnableAllModelPolicies(ctx, apiToken, ids)
}
