// Package logging provides Gin middleware for HTTP request logging and panic recovery.
// It integrates Gin web framework with logrus for structured logging of HTTP requests,
// responses, and error handling with panic recovery capabilities.
package logging

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// aiAPIPrefixes defines path prefixes for AI API requests that should have request ID tracking.
var aiAPIPrefixes = []string{
	"/v1/chat/completions",
	"/v1/completions",
	"/v1/messages",
	"/v1/responses",
	"/v1beta/models/",
	"/api/provider/",
}

const skipGinLogKey = "__gin_skip_request_logging__"
const requestBodyKey = "__gin_request_body__"
const providerAuthContextKey = "cliproxy.provider_auth"
const ginProviderAuthKey = "providerAuth"

func getProviderAuthFromContext(c *gin.Context) (provider, authID, authLabel string) {
	if c == nil {
		return "", "", ""
	}

	// First try to get from Gin context (set by conductor.go)
	if v, exists := c.Get(ginProviderAuthKey); exists {
		if authInfo, ok := v.(map[string]string); ok {
			return authInfo["provider"], authInfo["auth_id"], authInfo["auth_label"]
		}
	}

	// Fallback to request context
	if c.Request == nil {
		return "", "", ""
	}
	ctx := c.Request.Context()
	if ctx == nil {
		return "", "", ""
	}
	if v, ok := ctx.Value(providerAuthContextKey).(map[string]string); ok {
		return v["provider"], v["auth_id"], v["auth_label"]
	}
	return "", "", ""
}

// GinLogrusLogger returns a Gin middleware handler that logs HTTP requests and responses
// using logrus. It captures request details including method, path, status code, latency,
// client IP, model name, and auth key name. Request ID is only added for AI API requests.
//
// Output format (AI API): [2025-12-23 20:14:10] [info ] | a1b2c3d4 | 200 |       23.559s | ... | model (auth)
// Output format (others): [2025-12-23 20:14:10] [info ] | -------- | 200 |       23.559s | ...
//
// Returns:
//   - gin.HandlerFunc: A middleware handler for request logging
func GinLogrusLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		raw := util.MaskSensitiveQuery(c.Request.URL.RawQuery)

		var requestBody []byte
		if isAIAPIPath(path) && c.Request.Body != nil {
			requestBody, _ = io.ReadAll(c.Request.Body)
			c.Request.Body = io.NopCloser(bytes.NewReader(requestBody))
			c.Set(requestBodyKey, requestBody)
		}

		// Only generate request ID for AI API paths
		var requestID string
		if isAIAPIPath(path) {
			requestID = GenerateRequestID()
			SetGinRequestID(c, requestID)
			ctx := WithRequestID(c.Request.Context(), requestID)
			ctx = context.WithValue(ctx, "gin", c)
			c.Request = c.Request.WithContext(ctx)
		}

		c.Next()

		if shouldSkipGinRequestLogging(c) {
			return
		}

		if raw != "" {
			path = path + "?" + raw
		}

		latency := time.Since(start)
		if latency > time.Minute {
			latency = latency.Truncate(time.Second)
		} else {
			latency = latency.Truncate(time.Millisecond)
		}

		statusCode := c.Writer.Status()
		clientIP := c.ClientIP()
		method := c.Request.Method
		errorMessage := c.Errors.ByType(gin.ErrorTypePrivate).String()

		modelName := ""
		if len(requestBody) == 0 {
			if storedBody, exists := c.Get(requestBodyKey); exists {
				if bodyBytes, ok := storedBody.([]byte); ok {
					requestBody = bodyBytes
				}
			}
		}
		if len(requestBody) > 0 {
			modelName = gjson.GetBytes(requestBody, "model").String()
			modelName = strings.TrimSpace(modelName)
		}

		authKeyName := ""
		if apiKey, exists := c.Get("apiKey"); exists {
			if keyStr, ok := apiKey.(string); ok {
				authKeyName = keyStr
			}
		}

		provider, authID, authLabel := getProviderAuthFromContext(c)
		providerInfo := ""
		if provider != "" {
			displayAuth := authLabel
			if displayAuth == "" {
				displayAuth = authID
			}
			if displayAuth != "" {
				providerInfo = fmt.Sprintf("%s:%s", provider, displayAuth)
			} else {
				providerInfo = provider
			}
		}

		if requestID == "" {
			requestID = "--------"
		}

		logLine := fmt.Sprintf("%3d | %13v | %15s | %-7s \"%s\"", statusCode, latency, clientIP, method, path)

		if isAIAPIPath(path) && (modelName != "" || providerInfo != "" || authKeyName != "") {
			if modelName != "" && providerInfo != "" {
				logLine = logLine + " | " + fmt.Sprintf("%s | %s", modelName, providerInfo)
			} else if modelName != "" {
				logLine = logLine + " | " + modelName
			} else if providerInfo != "" {
				logLine = logLine + " | " + providerInfo
			}
			if authKeyName != "" && providerInfo == "" {
				logLine = logLine + " | (" + authKeyName + ")"
			}
		}

		if errorMessage != "" {
			logLine = logLine + " | " + errorMessage
		}

		entry := log.WithField("request_id", requestID)

		switch {
		case statusCode >= http.StatusInternalServerError:
			entry.Error(logLine)
		case statusCode >= http.StatusBadRequest:
			entry.Warn(logLine)
		default:
			entry.Info(logLine)
		}
	}
}

// isAIAPIPath checks if the given path is an AI API endpoint that should have request ID tracking.
func isAIAPIPath(path string) bool {
	for _, prefix := range aiAPIPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// GinLogrusRecovery returns a Gin middleware handler that recovers from panics and logs
// them using logrus. When a panic occurs, it captures the panic value, stack trace,
// and request path, then returns a 500 Internal Server Error response to the client.
//
// Returns:
//   - gin.HandlerFunc: A middleware handler for panic recovery
func GinLogrusRecovery() gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, recovered interface{}) {
		if err, ok := recovered.(error); ok && errors.Is(err, http.ErrAbortHandler) {
			// Let net/http handle ErrAbortHandler so the connection is aborted without noisy stack logs.
			panic(http.ErrAbortHandler)
		}

		log.WithFields(log.Fields{
			"panic": recovered,
			"stack": string(debug.Stack()),
			"path":  c.Request.URL.Path,
		}).Error("recovered from panic")

		c.AbortWithStatus(http.StatusInternalServerError)
	})
}

// SkipGinRequestLogging marks the provided Gin context so that GinLogrusLogger
// will skip emitting a log line for the associated request.
func SkipGinRequestLogging(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(skipGinLogKey, true)
}

func shouldSkipGinRequestLogging(c *gin.Context) bool {
	if c == nil {
		return false
	}
	val, exists := c.Get(skipGinLogKey)
	if !exists {
		return false
	}
	flag, ok := val.(bool)
	return ok && flag
}

// GetRequestBody retrieves the request body from context or reads it from the request.
// This allows handlers to read the body multiple times.
func GetRequestBody(c *gin.Context) []byte {
	if c == nil {
		return nil
	}
	if body, exists := c.Get(requestBodyKey); exists {
		if bodyBytes, ok := body.([]byte); ok {
			return bodyBytes
		}
	}
	if c.Request.Body != nil {
		body, _ := io.ReadAll(c.Request.Body)
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		c.Set(requestBodyKey, body)
		return body
	}
	return nil
}
