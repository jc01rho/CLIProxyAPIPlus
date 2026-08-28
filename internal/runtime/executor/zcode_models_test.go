package executor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// zcodeAuthWithKey builds a zcode auth record carrying a provisioned Z.AI API key.
func zcodeAuthWithKey(key, baseURL string) *cliproxyauth.Auth {
	attrs := map[string]string{"api_key": key}
	if baseURL != "" {
		attrs["models_base_url"] = baseURL
	}
	return &cliproxyauth.Auth{Provider: "zcode", Attributes: attrs}
}

// TestFetchZcodeModelsAdvertisesRemoteCatalog pins that discovery reads the
// live /v1/models catalog, authenticates with the provisioned key, identifies
// itself with the ZCode source headers, and surfaces newly released model IDs
// that the static catalog does not know about.
func TestFetchZcodeModelsAdvertisesRemoteCatalog(t *testing.T) {
	var gotAuth, gotAgent, gotVersion, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAgent = r.Header.Get("X-ZCode-Agent")
		gotVersion = r.Header.Get("anthropic-version")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{
				{"id": "glm-5.2", "name": "GLM-5.2"},
				{"id": "glm-5.3", "name": "GLM-5.3", "context_length": 200000},
			},
		})
	}))
	defer srv.Close()

	models := FetchZcodeModels(nil, zcodeAuthWithKey("key-1.secret", srv.URL), &config.Config{})
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2 (%+v)", len(models), models)
	}
	ids := map[string]*struct{ ctx, max int }{}
	for _, m := range models {
		ids[m.ID] = &struct{ ctx, max int }{m.ContextLength, m.MaxCompletionTokens}
	}
	if _, ok := ids["glm-5.3"]; !ok {
		t.Errorf("newly released glm-5.3 missing from advertised catalog: %+v", models)
	}
	if gotPath != "/v1/models" {
		t.Errorf("request path = %q, want /v1/models", gotPath)
	}
	if gotAuth != "Bearer key-1.secret" {
		t.Errorf("Authorization = %q, want Bearer key-1.secret", gotAuth)
	}
	if gotAgent != "glm" {
		t.Errorf("X-ZCode-Agent = %q, want glm", gotAgent)
	}
	if gotVersion != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want 2023-06-01", gotVersion)
	}
}

// TestFetchZcodeModelsOverlaysStaticMetadata pins that a live model ID also
// present in the static catalog inherits the static capability metadata the
// remote catalog omits, mirroring the reference resolveReference merge.
func TestFetchZcodeModelsOverlaysStaticMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Remote omits context/max tokens entirely.
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{{"id": "glm-5.2"}},
		})
	}))
	defer srv.Close()

	models := FetchZcodeModels(nil, zcodeAuthWithKey("key-1.secret", srv.URL), &config.Config{})
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(models))
	}
	m := models[0]
	if m.ContextLength != 1000000 {
		t.Errorf("ContextLength = %d, want 1000000 from static overlay", m.ContextLength)
	}
	if m.MaxCompletionTokens != 131072 {
		t.Errorf("MaxCompletionTokens = %d, want 131072 from static overlay", m.MaxCompletionTokens)
	}
	if m.Type != "claude" {
		t.Errorf("Type = %q, want claude from static overlay", m.Type)
	}
}

// TestFetchZcodeModelsFallsBackToStatic pins the nil-return contract that lets
// the caller fall back to the static catalog: no provisioned key, an HTTP
// error, and an empty remote catalog all yield nil.
func TestFetchZcodeModelsFallsBackToStatic(t *testing.T) {
	t.Run("no api key", func(t *testing.T) {
		if models := FetchZcodeModels(nil, &cliproxyauth.Auth{Provider: "zcode"}, &config.Config{}); models != nil {
			t.Errorf("models = %+v, want nil so the caller uses the static catalog", models)
		}
	})
	t.Run("http error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer srv.Close()
		if models := FetchZcodeModels(nil, zcodeAuthWithKey("key-1.secret", srv.URL), &config.Config{}); models != nil {
			t.Errorf("models = %+v, want nil on HTTP error", models)
		}
	})
	t.Run("empty catalog", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]interface{}{"data": []map[string]interface{}{}})
		}))
		defer srv.Close()
		if models := FetchZcodeModels(nil, zcodeAuthWithKey("key-1.secret", srv.URL), &config.Config{}); models != nil {
			t.Errorf("models = %+v, want nil on empty remote catalog", models)
		}
	})
}

// TestFetchZcodeModelsSanitizesRemoteDisplayName pins that the remote name is
// treated as provider-controlled text: control sequences are stripped and the
// value is length-bounded before it reaches a model listing.
func TestFetchZcodeModelsSanitizesRemoteDisplayName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{
				{"id": "glm-9.9", "name": "GLM\u001b[31m-9.9\u0007\tEVIL"},
			},
		})
	}))
	defer srv.Close()

	models := FetchZcodeModels(nil, zcodeAuthWithKey("key-1.secret", srv.URL), &config.Config{})
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(models))
	}
	name := models[0].DisplayName
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("DisplayName retains control character %q in %q", r, name)
		}
	}
	if len(name) > 200 {
		t.Errorf("len(DisplayName) = %d, want <= 200", len(name))
	}
}
