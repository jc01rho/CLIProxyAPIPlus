package kiro

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseKiroAvailableModelsAcceptsBothKeys(t *testing.T) {
	models := parseKiroAvailableModels([]byte(`{
		"models": [
			{"modelId":"claude-sonnet-5","modelName":"Claude Sonnet 5","rateMultiplier":1.3,"tokenLimits":{"maxInputTokens":1000000}},
			{"modelId":"claude-sonnet-5","modelName":"dup"}
		]
	}`))
	if len(models) != 1 || models[0].ModelID != "claude-sonnet-5" || models[0].MaxInputTokens != 1000000 {
		t.Fatalf("models payload = %+v", models)
	}

	available := parseKiroAvailableModels([]byte(`{
		"availableModels": [
			{"modelId":"gpt-5.6-sol","modelName":"GPT-5.6 Sol","tokenLimits":{"maxInputTokens":272000}}
		]
	}`))
	if len(available) != 1 || available[0].ModelID != "gpt-5.6-sol" {
		t.Fatalf("availableModels payload = %+v", available)
	}

	if got := parseKiroAvailableModels([]byte(`{"oops":[]}`)); len(got) != 0 {
		t.Fatalf("empty payload = %+v", got)
	}
}

func TestListAvailableModelsOriginFirstThenProfileArnRetry(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RawQuery)
		if !strings.Contains(r.URL.RawQuery, "profileArn=") {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"message":"builder-id"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"availableModels":[{"modelId":"claude-sonnet-5","modelName":"Claude Sonnet 5"}]}`)
	}))
	t.Cleanup(server.Close)

	auth := &KiroAuth{
		httpClient: &http.Client{
			Transport: &rewriteTransport{
				base:      server.Client().Transport,
				targetURL: server.URL,
			},
		},
	}

	models, err := auth.ListAvailableModels(context.Background(), &KiroTokenData{
		AccessToken:  "tok",
		ProfileArn:   "arn:aws:codewhisperer:us-east-1:123456789012:profile/ABC",
		RefreshToken: "rt",
		ClientID:     "cid",
	})
	if err != nil {
		t.Fatalf("ListAvailableModels: %v", err)
	}
	if len(models) != 1 || models[0].ModelID != "claude-sonnet-5" {
		t.Fatalf("models = %+v", models)
	}
	if len(paths) != 2 {
		t.Fatalf("calls = %v, want origin-only then profileArn retry", paths)
	}
	if strings.Contains(paths[0], "profileArn=") {
		t.Fatalf("first call must be origin-only, got %s", paths[0])
	}
	if !strings.Contains(paths[1], "profileArn=") {
		t.Fatalf("retry must include profileArn, got %s", paths[1])
	}
}
