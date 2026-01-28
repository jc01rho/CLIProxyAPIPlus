package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/trae"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/sjson"
)

type TraeExecutor struct {
	cfg *config.Config
}

func NewTraeExecutor(cfg *config.Config) *TraeExecutor {
	return &TraeExecutor{cfg: cfg}
}

func (e *TraeExecutor) Provider() string {
	return "trae"
}

func (e *TraeExecutor) Identifier() string {
	return "trae"
}

// traeCreds extracts access token and host from auth metadata.
func traeCreds(auth *coreauth.Auth) (accessToken, host string) {
	host = "https://api-sg-central.trae.ai" // default host
	if auth == nil || auth.Metadata == nil {
		return "", host
	}
	if v, ok := auth.Metadata["access_token"].(string); ok && v != "" {
		accessToken = v
	}
	if v, ok := auth.Metadata["host"].(string); ok && v != "" {
		host = v
	}
	return accessToken, host
}

func (e *TraeExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := req.Model

	// Get access token and host from auth metadata
	accessToken, host := traeCreds(auth)
	if accessToken == "" {
		return resp, fmt.Errorf("trae: missing access token")
	}

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai") // Trae uses OpenAI-compatible format

	originalPayload := bytes.Clone(req.Payload)
	if len(opts.OriginalRequest) > 0 {
		originalPayload = bytes.Clone(opts.OriginalRequest)
	}

	body := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), false)
	body, _ = sjson.SetBytes(body, "model", baseModel)
	body, _ = sjson.SetBytes(body, "stream", false)

	url := fmt.Sprintf("%s/v1/chat/completions", host)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}

	httpReq.Header.Set("Authorization", "Bearer "+accessToken)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	if auth != nil && auth.Attributes != nil {
		util.ApplyCustomHeadersFromAttrs(httpReq, auth.Attributes)
	}

	var authID string
	if auth != nil {
		authID = auth.ID
	}

	log.WithFields(log.Fields{
		"auth_id":  authID,
		"provider": e.Identifier(),
		"model":    baseModel,
		"url":      url,
		"method":   http.MethodPost,
	}).Infof("external HTTP request: POST %s", url)

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return resp, fmt.Errorf("trae: request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return resp, fmt.Errorf("trae: failed to read response: %w", err)
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return resp, fmt.Errorf("trae: API error %d: %s", httpResp.StatusCode, string(respBody))
	}

	// Translate response back to source format
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, bytes.Clone(originalPayload), body, respBody, &param)

	return cliproxyexecutor.Response{Payload: []byte(out)}, nil
}

func (e *TraeExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	baseModel := req.Model

	accessToken, host := traeCreds(auth)
	if accessToken == "" {
		return nil, fmt.Errorf("trae: missing access token")
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")

	originalPayload := bytes.Clone(req.Payload)
	if len(opts.OriginalRequest) > 0 {
		originalPayload = bytes.Clone(opts.OriginalRequest)
	}

	body := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), true)
	body, _ = sjson.SetBytes(body, "model", baseModel)
	body, _ = sjson.SetBytes(body, "stream", true)

	url := fmt.Sprintf("%s/v1/chat/completions", host)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	httpReq.Header.Set("Authorization", "Bearer "+accessToken)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	if auth != nil && auth.Attributes != nil {
		util.ApplyCustomHeadersFromAttrs(httpReq, auth.Attributes)
	}

	var authID string
	if auth != nil {
		authID = auth.ID
	}

	log.WithFields(log.Fields{
		"auth_id":  authID,
		"provider": e.Identifier(),
		"model":    baseModel,
		"url":      url,
		"method":   http.MethodPost,
	}).Infof("external HTTP stream request: POST %s", url)

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("trae: stream request failed: %w", err)
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		return nil, fmt.Errorf("trae: API error %d: %s", httpResp.StatusCode, string(respBody))
	}

	ch := make(chan cliproxyexecutor.StreamChunk, 100)

	go func() {
		defer close(ch)
		defer httpResp.Body.Close()

		var param any
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				break
			}

			translated := sdktranslator.TranslateStream(ctx, to, from, req.Model, originalPayload, body, []byte(data), &param)
			for _, line := range translated {
				ch <- cliproxyexecutor.StreamChunk{
					Payload: []byte(line),
					Err:     nil,
				}
			}
		}

		if err := scanner.Err(); err != nil {
			ch <- cliproxyexecutor.StreamChunk{Err: err}
		}
	}()

	return ch, nil
}

func (e *TraeExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("trae: CountTokens not implemented")
}

func (e *TraeExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("trae executor: auth is nil")
	}
	var refreshToken string
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["refresh_token"].(string); ok && v != "" {
			refreshToken = v
		}
	}
	if refreshToken == "" && auth.Attributes != nil {
		refreshToken = auth.Attributes["refresh_token"]
	}
	if refreshToken == "" {
		return auth, nil
	}

	svc := trae.NewTraeAuth(e.cfg)
	td, err := svc.RefreshTokens(ctx, refreshToken)
	if err != nil {
		return nil, err
	}

	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = td.AccessToken
	if td.RefreshToken != "" {
		auth.Metadata["refresh_token"] = td.RefreshToken
	}
	auth.Metadata["email"] = td.Email
	auth.Metadata["expired"] = td.Expire
	auth.Metadata["type"] = "trae"

	return auth, nil
}

func (e *TraeExecutor) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("trae executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}

	httpReq := req.WithContext(ctx)

	accessToken := ""
	if auth != nil && auth.Metadata != nil {
		if v, ok := auth.Metadata["access_token"].(string); ok && v != "" {
			accessToken = v
		}
	}

	if accessToken == "" && auth != nil && auth.Attributes != nil {
		if v, ok := auth.Attributes["access_token"]; ok && v != "" {
			accessToken = v
		}
	}

	if accessToken == "" {
		return nil, fmt.Errorf("trae executor: missing access token in auth metadata or attributes")
	}

	httpReq.Header.Set("Authorization", "Bearer "+accessToken)

	if auth != nil && auth.Attributes != nil {
		util.ApplyCustomHeadersFromAttrs(httpReq, auth.Attributes)
	}

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}
