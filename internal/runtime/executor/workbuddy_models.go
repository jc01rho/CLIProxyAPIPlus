package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const workBuddyModelsFetchTimeout = 15 * time.Second

var workBuddyModelPaths = []string{
	"/v3/config",
	"/v2/enterprises/personal/models",
	"/console/enterprises/personal/models",
}

// FetchWorkBuddyModels discovers the global WorkBuddy catalog and falls back to the static snapshot.
func FetchWorkBuddyModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	fallback := registry.GetWorkBuddyModels()
	accessToken, userID, enterpriseID, domain := workBuddyCredentials(auth)
	if accessToken == "" {
		return fallback
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, workBuddyModelsFetchTimeout)
	defer cancel()
	client := newProxyAwareHTTPClient(ctx, cfg, auth, 0)
	exec := NewWorkBuddyExecutor(cfg)
	var lastErr error
	for _, path := range workBuddyModelPaths {
		requestURL := strings.TrimRight(workBuddyBaseURL, "/") + path
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
		if err != nil {
			lastErr = err
			continue
		}
		exec.applyHeaders(req, accessToken, userID, enterpriseID, domain)
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, errRead := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()
		if errRead != nil {
			lastErr = errRead
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
			continue
		}
		models := parseWorkBuddyModels(body)
		if len(models) > 0 {
			return registry.OverlayStaticMetadataWithRequiredIDs(models, fallback, workBuddyAliasBaseModelIDs(cfg))
		}
		lastErr = fmt.Errorf("empty or malformed catalog")
	}
	if lastErr != nil {
		log.Debugf("workbuddy: using static models after discovery failed: %v", lastErr)
	}
	return fallback
}

type workBuddyModelEntry struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Description     string   `json:"descriptionZh"`
	Disabled        bool     `json:"disabled"`
	MaxInputTokens  int      `json:"maxInputTokens"`
	MaxOutputTokens int      `json:"maxOutputTokens"`
	SupportsImages  bool     `json:"supportsImages"`
	Tags            []string `json:"tags"`
	Reasoning       struct {
		SupportedEfforts []string `json:"supportedEfforts"`
		DefaultEffort    string   `json:"defaultEffort"`
	} `json:"reasoning"`
}

func workBuddyAliasBaseModelIDs(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	aliases := cfg.OAuthModelAlias["workbuddy"]
	if len(aliases) == 0 {
		return nil
	}
	ids := make([]string, 0, len(aliases))
	seen := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		if !alias.Fork {
			continue
		}
		id := strings.TrimSpace(alias.Name)
		key := strings.ToLower(id)
		if id == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func parseWorkBuddyModels(body []byte) []*registry.ModelInfo {
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			Models []workBuddyModelEntry `json:"models"`
			Agents []struct {
				Name   string   `json:"name"`
				Models []string `json:"models"`
			} `json:"agents"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Code == 0 && len(envelope.Data.Models) > 0 {
		allowed := make(map[string]struct{})
		for _, agent := range envelope.Data.Agents {
			if strings.EqualFold(agent.Name, "cli") {
				for _, id := range agent.Models {
					allowed[id] = struct{}{}
				}
			}
		}
		models := make([]*registry.ModelInfo, 0, len(envelope.Data.Models))
		seen := make(map[string]struct{}, len(envelope.Data.Models))
		for _, entry := range envelope.Data.Models {
			id := strings.TrimSpace(entry.ID)
			if id == "" {
				id = strings.TrimSpace(entry.Name)
			}
			if id == "" || entry.Disabled || workBuddyNonChatModel(id, entry.MaxOutputTokens, entry.Tags) {
				continue
			}
			if len(allowed) > 0 {
				if _, ok := allowed[id]; !ok {
					continue
				}
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			displayName := strings.TrimSpace(entry.Name)
			if displayName == "" {
				displayName = id
			}
			model := &registry.ModelInfo{
				ID:                  id,
				Object:              "model",
				Created:             time.Now().Unix(),
				OwnedBy:             "workbuddy",
				Type:                "workbuddy",
				DisplayName:         displayName,
				Name:                id,
				Description:         entry.Description,
				ContextLength:       entry.MaxInputTokens,
				MaxCompletionTokens: entry.MaxOutputTokens,
				SupportedEndpoints:  []string{"/chat/completions"},
			}
			if entry.SupportsImages {
				model.SupportedInputModalities = []string{"TEXT", "IMAGE"}
			}
			if len(entry.Reasoning.SupportedEfforts) > 0 || strings.TrimSpace(entry.Reasoning.DefaultEffort) != "" {
				model.Thinking = &registry.ThinkingSupport{
					Levels:        entry.Reasoning.SupportedEfforts,
					DefaultEffort: strings.TrimSpace(entry.Reasoning.DefaultEffort),
					ZeroAllowed:   true,
				}
			}
			models = append(models, model)
		}
		return models
	}
	var narrow struct {
		Code int      `json:"code"`
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(body, &narrow); err != nil || narrow.Code != 0 {
		return nil
	}
	models := make([]*registry.ModelInfo, 0, len(narrow.Data))
	seen := make(map[string]struct{}, len(narrow.Data))
	for _, rawID := range narrow.Data {
		id := strings.TrimSpace(rawID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, &registry.ModelInfo{
			ID: id, Object: "model", Created: time.Now().Unix(), OwnedBy: "workbuddy", Type: "workbuddy",
			DisplayName: id, Name: id, SupportedEndpoints: []string{"/chat/completions"},
		})
	}
	return models
}

func workBuddyNonChatModel(id string, maxOutputTokens int, tags []string) bool {
	lowerID := strings.ToLower(strings.TrimSpace(id))
	for _, prefix := range []string{"nes-", "completion-", "codewise-"} {
		if strings.HasPrefix(lowerID, prefix) {
			return true
		}
	}
	if maxOutputTokens > 0 && maxOutputTokens <= 256 {
		return true
	}
	for _, tag := range tags {
		if strings.EqualFold(strings.TrimSpace(tag), "text-to-image") {
			return true
		}
	}
	return false
}
