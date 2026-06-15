package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/gitlab"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

type gitlabPATRequest struct {
	BaseURL             string `json:"base_url"`
	PersonalAccessToken string `json:"personal_access_token"`
}

// RequestGitLabPATToken validates a GitLab personal access token, fetches user
// and model gateway details, and persists the credential as an auth file.
func (h *Handler) RequestGitLabPATToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	var req gitlabPATRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid body"})
		return
	}

	baseURL := gitlab.NormalizeBaseURL(strings.TrimSpace(req.BaseURL))
	pat := strings.TrimSpace(req.PersonalAccessToken)
	if baseURL == "" || pat == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "base_url and personal_access_token are required"})
		return
	}

	client := gitlab.NewAuthClient(h.cfg)

	user, err := client.GetCurrentUser(ctx, baseURL, pat)
	if err != nil {
		log.WithError(err).Error("failed to validate GitLab personal access token")
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "failed to validate GitLab token"})
		return
	}

	_, err = client.GetPersonalAccessTokenSelf(ctx, baseURL, pat)
	if err != nil {
		log.WithError(err).Warn("failed to fetch GitLab PAT self info; continuing with token validation")
	}

	direct, err := client.FetchDirectAccess(ctx, baseURL, pat)
	if err != nil {
		log.WithError(err).Warn("failed to fetch GitLab direct access info; continuing with basic token validation")
		direct = &gitlab.DirectAccessResponse{
			BaseURL: baseURL,
			Token:   pat,
			Headers: map[string]string{},
		}
	}

	modelProvider := "anthropic"
	modelName := "claude-sonnet-4-5"
	if direct != nil && direct.ModelDetails != nil {
		if direct.ModelDetails.ModelProvider != "" {
			modelProvider = direct.ModelDetails.ModelProvider
		}
		if direct.ModelDetails.ModelName != "" {
			modelName = direct.ModelDetails.ModelName
		}
	}

	email := strings.TrimSpace(user.Email)
	if email == "" {
		email = strings.TrimSpace(user.PublicEmail)
	}
	if email == "" {
		email = fmt.Sprintf("%s@%s", user.Username, baseURL)
	}

	fileName := fmt.Sprintf("gitlab-%s.json", safeFileName(email))
	metadata := map[string]any{
		"type":              "gitlab",
		"auth_kind":         "personal_access_token",
		"base_url":          baseURL,
		"user_id":           user.ID,
		"username":          user.Username,
		"email":             email,
		"model_provider":    modelProvider,
		"model_name":        modelName,
		"duo_gateway_token": direct.Token,
	}
	for k, v := range direct.Headers {
		metadata["duo_gateway_header_"+k] = v
	}

	record := &coreauth.Auth{
		ID:       fileName,
		Provider: "gitlab",
		FileName: fileName,
		Label:    fmt.Sprintf("%s@%s", user.Username, baseURL),
		Metadata: metadata,
	}

	if _, err := h.saveTokenRecord(ctx, record); err != nil {
		log.WithError(err).Error("failed to save GitLab auth record")
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "failed to save auth record"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":         "ok",
		"model_provider": modelProvider,
		"model_name":     modelName,
	})
}

func safeFileName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' || r == '@' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	if b.String() == "" {
		return "unknown"
	}
	return b.String()
}

// jsonString returns a compact JSON string or an empty string on error.
func jsonString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
