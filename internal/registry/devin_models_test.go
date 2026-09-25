package registry

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestValidateDevinModelsJSON(t *testing.T) {
	t.Run("valid envelope devin", func(t *testing.T) {
		data := []byte(`{
			"devin": [
				{
					"id": "swe-2",
					"display_name": "SWE-2",
					"owned_by": "cognition",
					"context_length": 262000
				}
			]
		}`)
		models, err := ValidateDevinModelsJSON(data)
		if err != nil {
			t.Fatalf("expected valid, got error: %v", err)
		}
		if len(models) != 1 || models[0].ID != "swe-2" {
			t.Fatalf("unexpected models: %+v", models)
		}
		if models[0].Type != "devin" {
			t.Errorf("expected type 'devin', got %q", models[0].Type)
		}
	})

	t.Run("valid envelope models", func(t *testing.T) {
		data := []byte(`{
			"models": [
				{
					"id": "glm-5-2",
					"display_name": "GLM-5.2"
				}
			]
		}`)
		models, err := ValidateDevinModelsJSON(data)
		if err != nil {
			t.Fatalf("expected valid, got error: %v", err)
		}
		if len(models) != 1 || models[0].ID != "glm-5-2" {
			t.Fatalf("unexpected models: %+v", models)
		}
	})

	t.Run("valid direct array", func(t *testing.T) {
		data := []byte(`[
			{
				"id": "deepseek-v4-flash",
				"display_name": "DeepSeek V4 Flash"
			}
		]`)
		models, err := ValidateDevinModelsJSON(data)
		if err != nil {
			t.Fatalf("expected valid, got error: %v", err)
		}
		if len(models) != 1 || models[0].ID != "deepseek-v4-flash" {
			t.Fatalf("unexpected models: %+v", models)
		}
	})

	t.Run("clean id without devin prefix kept bare", func(t *testing.T) {
		data := []byte(`{
			"devin": [
				{
					"id": "swe-2",
					"display_name": "SWE-2"
				}
			]
		}`)
		models, err := ValidateDevinModelsJSON(data)
		if err != nil {
			t.Fatalf("expected valid, got error: %v", err)
		}
		if len(models) != 1 || models[0].ID != "swe-2" {
			t.Fatalf("expected bare id swe-2 preserved, got: %q", models[0].ID)
		}
	})

	t.Run("empty payload", func(t *testing.T) {
		_, err := ValidateDevinModelsJSON([]byte(`   `))
		if err == nil {
			t.Fatal("expected error on empty payload, got nil")
		}
	})

	t.Run("empty model id", func(t *testing.T) {
		data := []byte(`{"devin": [{"id": "", "display_name": "No ID"}]}`)
		_, err := ValidateDevinModelsJSON(data)
		if err == nil {
			t.Fatal("expected error on empty model id, got nil")
		}
	})

	t.Run("duplicate model id", func(t *testing.T) {
		data := []byte(`{"devin": [{"id": "swe-2"}, {"id": "swe-2"}]}`)
		_, err := ValidateDevinModelsJSON(data)
		if err == nil {
			t.Fatal("expected error on duplicate model id, got nil")
		}
	})
}

func TestEmbeddedDevinModelsLoadedOnStartup(t *testing.T) {
	models := GetDevinModels()
	if len(models) < 30 {
		t.Fatalf("expected at least 30 embedded Devin models, got %d", len(models))
	}

	foundMap := make(map[string]bool)
	for _, m := range models {
		foundMap[m.ID] = true
	}

	expectedIDs := []string{
		"swe-2",
		"glm-5-2",
		"glm-5-3",
		"deepseek-v4-flash",
		"deepseek-v4-1-flash",
		"gemini-3-8-flash",
		"grok-4-6",
		"claude-fable-5-1",
		"gpt-6-astra",
	}

	for _, id := range expectedIDs {
		if !foundMap[id] {
			t.Errorf("expected embedded catalog to contain %q", id)
		}
	}
}
func TestDevinModelsRemoteFetchFallback(t *testing.T) {
	// Test remote failure maintains existing embedded data
	origURLs := devinModelsURLs
	defer func() { devinModelsURLs = origURLs }()

	// Point to failing server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
	defer ts.Close()

	devinModelsURLs = []string{ts.URL + "/devin_models.json"}

	initialCount := len(GetDevinModels())
	if initialCount == 0 {
		t.Fatal("expected non-empty initial Devin models")
	}

	// Attempt refresh from failing remote
	tryRefreshDevinModels(context.Background(), "test failing refresh")

	afterCount := len(GetDevinModels())
	if afterCount != initialCount {
		t.Fatalf("expected catalog to remain intact with %d models, got %d", initialCount, afterCount)
	}

	// Point to succeeding server with valid update
	validUpdate := []byte(`{
		"devin": [
			{
				"id": "devin/custom-test-model",
				"display_name": "Custom Test Model",
				"owned_by": "custom"
			}
		]
	}`)

	tsValid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(validUpdate)
	}))
	defer tsValid.Close()

	devinModelsURLs = []string{tsValid.URL + "/devin_models.json"}
	tryRefreshDevinModels(context.Background(), "test succeeding refresh")

	updatedModels := GetDevinModels()
	if len(updatedModels) != 1 || updatedModels[0].ID != "devin/custom-test-model" {
		t.Fatalf("expected catalog to be updated to custom-test-model, got: %+v", updatedModels)
	}

	// Restore original embedded data for following tests
	_, _ = loadDevinModelsFromBytes(embeddedDevinModelsJSON, "restore-embed")
}

type devinFetchTestRoundTripper func(*http.Request) (*http.Response, error)

func (f devinFetchTestRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type devinFetchTestBody struct {
	ctx             context.Context
	readStarted     chan struct{}
	releaseBody     chan struct{}
	data            *bytes.Reader
	readStartedOnce sync.Once
}

func (b *devinFetchTestBody) Read(p []byte) (int, error) {
	b.readStartedOnce.Do(func() { close(b.readStarted) })
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	select {
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	case <-b.releaseBody:
	}
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	return b.data.Read(p)
}

func (*devinFetchTestBody) Close() error { return nil }

func TestFetchDevinModelsFromRemote_ContextNotCanceledBeforeRead_Issue6095(t *testing.T) {
	origURLs := devinModelsURLs
	defer func() { devinModelsURLs = origURLs }()

	readStarted := make(chan struct{})
	releaseBody := make(chan struct{})
	devinModelsURLs = []string{"https://models.example.test/devin_models.json"}
	client := &http.Client{
		Timeout: modelsFetchTimeout,
		Transport: devinFetchTestRoundTripper(func(req *http.Request) (*http.Response, error) {
			body := &devinFetchTestBody{
				ctx:         req.Context(),
				readStarted: readStarted,
				releaseBody: releaseBody,
				data:        bytes.NewReader([]byte(`{"devin": [{"id": "devin/swe-2"}]}`)),
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       body,
				Request:    req,
			}, nil
		}),
	}
	result := make(chan struct {
		body   []byte
		source string
	}, 1)
	go func() {
		body, source := fetchDevinModelsFromRemoteWithClient(context.Background(), client)
		result <- struct {
			body   []byte
			source string
		}{body: body, source: source}
	}()

	select {
	case <-readStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("fetch did not begin reading the response body")
	}
	close(releaseBody)

	var fetched struct {
		body   []byte
		source string
	}
	select {
	case fetched = <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("fetch did not finish reading the response body")
	}
	if fetched.body == nil || fetched.source == "" {
		t.Fatalf("expected successful fetch of Devin models, got nil body (source=%q)", fetched.source)
	}
	expectedBody := `{"devin": [{"id": "devin/swe-2"}]}`
	if string(fetched.body) != expectedBody {
		t.Fatalf("expected body %q, got %q", expectedBody, string(fetched.body))
	}
	expectedSource := "https://models.example.test/devin_models.json"
	if fetched.source != expectedSource {
		t.Fatalf("expected source %q, got %q", expectedSource, fetched.source)
	}
}
