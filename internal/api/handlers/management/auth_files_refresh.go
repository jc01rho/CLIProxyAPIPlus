package management

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// RefreshAuthFiles triggers active refresh for a single auth file or all auth files.
// Accepts query parameters (?all=true, ?name=file.json) or JSON body ({"all": true, "name": "file.json"}).
func (h *Handler) RefreshAuthFiles(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	var req struct {
		Name      string `json:"name"`
		AuthIndex string `json:"auth_index"`
		All       bool   `json:"all"`
	}
	if c.Request.Body != nil && c.Request.ContentLength != 0 {
		if errBind := c.ShouldBindJSON(&req); errBind != nil && !errors.Is(errBind, io.EOF) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + errBind.Error()})
			return
		}
	}
	if c.Query("all") == "true" {
		req.All = true
	}
	if queryName := strings.TrimSpace(c.Query("name")); queryName != "" && req.Name == "" {
		req.Name = queryName
	}
	if queryAuthIndex := strings.TrimSpace(c.Query("auth_index")); queryAuthIndex != "" && req.AuthIndex == "" {
		req.AuthIndex = queryAuthIndex
	}

	ctx := c.Request.Context()

	if req.All {
		results := h.authManager.ForceRefreshAll(ctx)
		resultIndexes := make(map[string]int, len(results))
		for index := range results {
			resultIndexes[results[index].ID] = index
		}
		for _, auth := range h.authManager.List() {
			if auth == nil || auth.ID == "" || auth.Disabled {
				continue
			}
			index, refreshedToken := resultIndexes[auth.ID]
			if refreshedToken && !results[index].Success {
				continue
			}
			if !refreshedToken && !auth.ExpiresNever() {
				continue
			}
			if errRefresh := h.refreshAuthModelRegistration(ctx, auth); errRefresh != nil {
				if refreshedToken {
					results[index].Success = false
					results[index].Error = errRefresh.Error()
				} else {
					results = append(results, coreauth.ForceRefreshResult{ID: auth.ID, Success: false, Error: errRefresh.Error()})
				}
				continue
			}
			if !refreshedToken {
				results = append(results, coreauth.ForceRefreshResult{ID: auth.ID, Success: true})
			}
		}
		c.JSON(http.StatusOK, gin.H{
			"ok":      true,
			"results": results,
		})
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name or all=true is required"})
		return
	}

	targetAuth, ok := h.lookupAuthFile(name, req.AuthIndex)
	if !ok || targetAuth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth file not found"})
		return
	}

	refreshed := targetAuth.Clone()
	if !targetAuth.ExpiresNever() {
		var errRefresh error
		refreshed, errRefresh = h.authManager.ForceRefreshAuth(ctx, targetAuth.ID)
		if errRefresh != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": errRefresh.Error(),
			})
			return
		}
	}
	if errRefreshModels := h.refreshAuthModelRegistration(ctx, refreshed); errRefreshModels != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": errRefreshModels.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":   true,
		"auth": refreshed,
	})
}

func (h *Handler) refreshAuthModelRegistration(ctx context.Context, auth *coreauth.Auth) error {
	if h == nil || h.modelRefreshHook == nil || auth == nil {
		return nil
	}
	refreshCtx := context.Background()
	if ctx != nil {
		refreshCtx = context.WithoutCancel(ctx)
	}
	return h.modelRefreshHook(refreshCtx, auth)
}
