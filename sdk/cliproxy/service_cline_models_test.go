package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type clineCatalogTransport struct {
	target *url.URL
	next   http.RoundTripper
}

func (tr clineCatalogTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	forward := req.Clone(req.Context())
	forward.URL.Scheme = tr.target.Scheme
	forward.URL.Host = tr.target.Host
	return tr.next.RoundTrip(forward)
}

func TestClineRegistrationFollowsLiveFreeFeed(t *testing.T) {
	var phase atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if phase.Load() == 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		switch r.URL.Path {
		case "/api/v1/ai/cline/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"vendor/paid","pricing":{"prompt":"1","completion":"1"}},{"id":"stealth/zero","pricing":{"prompt":"0","completion":"0"}}]}`))
		case "/api/v1/ai/cline/recommended-models":
			switch phase.Load() {
			case 0:
				_, _ = w.Write([]byte(`{"free":[{"id":"cline-free/dynamic-first","name":"First"}]}`))
			case 1:
				_, _ = w.Write([]byte(`{"free":[{"id":"cline-free/dynamic-second","name":"Second"}]}`))
			default:
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", clineCatalogTransport{target, server.Client().Transport})
	service := &Service{cfg: &config.Config{ClineFreeModelsOnly: true}}
	auth := &coreauth.Auth{ID: t.Name(), Provider: "cline", Status: coreauth.StatusActive}
	reg := registry.GetGlobalRegistry()
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	for step, want := range [][]string{
		{"stealth/zero", "cline-free/dynamic-first"},
		{"stealth/zero", "cline-free/dynamic-second"},
		{"stealth/zero"},
		{},
	} {
		phase.Store(int32(step))
		service.registerModelsForAuth(ctx, auth)
		models := reg.GetModelsForClient(auth.ID)
		seen := make(map[string]bool, len(models))
		for _, model := range models {
			seen[model.ID] = true
		}
		if len(seen) != len(want) {
			t.Fatalf("step %d: registered %v, want %v", step, seen, want)
		}
		for _, id := range want {
			if !seen[id] {
				t.Fatalf("step %d: missing %q in %v", step, id, seen)
			}
			assertOpenAIModel(t, reg.GetAvailableModels("openai"), id)
		}
	}
}
