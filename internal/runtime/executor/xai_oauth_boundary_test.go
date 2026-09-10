package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestXAIOAuthHTTPBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		attrs                     map[string]string
		alt                       string
		media                     bool
		url, userAgent, tokenAuth string
	}{
		{name: "API key", attrs: map[string]string{"api_key": "synthetic-key"}, url: xaiauth.DefaultAPIBaseURL + "/responses"},
		{name: "API key explicit CLI lane", attrs: map[string]string{"api_key": "synthetic-key", "using_api": "false"}, url: xaiauth.CLIChatProxyBaseURL + "/responses", userAgent: "xai-grok-workspace/0.2.120", tokenAuth: "xai-grok-cli"},
		{name: "OAuth explicit API", attrs: map[string]string{"auth_kind": "oauth", "using_api": "true"}, url: xaiauth.DefaultAPIBaseURL + "/responses"},
		{name: "OAuth explicit API with CLI base", attrs: map[string]string{"auth_kind": "oauth", "using_api": "true", "base_url": xaiauth.CLIChatProxyBaseURL}, url: xaiauth.CLIChatProxyBaseURL + "/responses"},
		{name: "OAuth custom gateway", attrs: map[string]string{"auth_kind": "oauth", "base_url": "https://gateway.example.test/v1"}, url: "https://gateway.example.test/v1/responses"},
		{name: "OAuth compact", attrs: map[string]string{"auth_kind": "oauth", "base_url": xaiauth.CLIChatProxyBaseURL}, alt: "responses/compact", url: xaiauth.DefaultAPIBaseURL + "/responses/compact"},
		{name: "OAuth media", attrs: map[string]string{"auth_kind": "oauth"}, media: true, url: xaiauth.CLIChatProxyBaseURL + "/images/generations"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != tc.url {
					t.Errorf("URL = %s, want %s", r.URL, tc.url)
				}
				for header, want := range map[string]string{"User-Agent": tc.userAgent, "x-xai-token-auth": tc.tokenAuth, "x-grok-session-id": "", "x-grok-req-id": "", "Accept-Encoding": ""} {
					if got := r.Header.Get(header); got != want {
						t.Errorf("%s = %q, want %q", header, got, want)
					}
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					return nil, err
				}
				if !tc.media {
					if got := gjson.GetBytes(body, "prompt_cache_key").String(); got != "host-session" {
						t.Errorf("legacy body key = %q", got)
					}
					if got := r.Header.Get("x-grok-conv-id"); got != "host-session" {
						t.Errorf("legacy conversation ID = %q", got)
					}
				}
				response, contentType := xaiOAuthCompletedSSE, "text/event-stream"
				if tc.alt != "" {
					response, contentType = `{"id":"compact","object":"response.compaction","output":[]}`, "application/json"
				}
				if tc.media {
					response, contentType = `{"created":123,"data":[{"b64_json":"AA=="}]}`, "application/json"
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(response))}, nil
			})
			ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), "cliproxy.roundtripper", rt), 10*time.Second)
			defer cancel()
			auth := &cliproxyauth.Auth{ID: "boundary", Provider: "xai", Attributes: tc.attrs, Metadata: map[string]any{"access_token": "synthetic-token"}}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Alt: tc.alt, Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "host-session"}}
			req := cliproxyexecutor.Request{Model: "grok-4.3", Payload: []byte(`{"model":"grok-4.3","input":[],"prompt_cache_key":"  abc  "}`)}
			if tc.media {
				opts.SourceFormat = sdktranslator.FromString("openai-image")
				req.Model = "grok-imagine-image"
				req.Payload = []byte(`{"model":"grok-imagine-image","prompt":"an apple"}`)
			}
			if _, err := NewXAIExecutor(&config.Config{}).Execute(ctx, auth, req, opts); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Errorf("requests = %d, want 1", calls)
			}
		})
	}
}

func TestXAIOAuthHTTPLeavesWebsocketHeadersUnchanged(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"auth_kind": "oauth"}}
	headers := applyXAIWebsocketHeaders(context.Background(), nil, auth, "synthetic-token", "host-session")
	if got := headers.Get("x-grok-conv-id"); got != "host-session" {
		t.Errorf("WS conversation = %q", got)
	}
	for _, header := range []string{"User-Agent", "x-grok-client-identifier", "x-grok-client-version", "x-grok-session-id", "x-grok-req-id", "x-xai-token-auth", "Accept-Encoding"} {
		if got := headers.Get(header); got != "" {
			t.Errorf("unexpected WS %s = %q", header, got)
		}
	}
}
