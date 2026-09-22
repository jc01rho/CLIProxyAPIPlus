package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestFetchMimocodeModelsPrefersLiveCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("path = %q, want /models", r.URL.Path)
		}
		if got := r.Header.Get("X-Mimo-Source"); got != mimocodeSourceValue {
			t.Fatalf("X-Mimo-Source = %q", got)
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"mimo-v2.5-pro"},{"id":"future-mimo"}]}`))
	}))
	defer server.Close()
	models := FetchMimocodeModels(context.Background(), &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key": "secret", "base_url": server.URL,
	}}, &config.Config{})
	if len(models) != 2 || models[0].ID != "mimo-v2.5-pro" || models[1].ID != "future-mimo" {
		t.Fatalf("models = %+v", models)
	}
	if models[0].ContextLength != 1_000_000 || models[0].MaxCompletionTokens != 131072 {
		t.Fatalf("static metadata was not retained: %+v", models[0])
	}
}
