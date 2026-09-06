package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type aliasAvailabilityFixture struct {
	manager  *Manager
	executor *aliasModelPoolExecutor
	auth     *Auth
	route    string
	targets  []string
	stateKey string
}

func newAliasAvailabilityFixture(t *testing.T, selector Selector, shape string, seedCooldowns bool) aliasAvailabilityFixture {
	t.Helper()
	f := aliasAvailabilityFixture{route: "glm", targets: []string{"glm-5.3", "glm-5.3-flash"}}
	f.auth = &Auth{ID: "alias-availability-" + t.Name(), Provider: "zcode", Status: StatusActive}
	cfg := &internalconfig.Config{}
	oauth := false
	switch shape {
	case "oauth-glm":
		oauth = true
	case "oauth-opus":
		oauth = true
		f.route, f.targets, f.auth.Provider = "opus", []string{"kiro-claude-opus-5-agentic"}, "kiro"
	case "claude-key-opus":
		f.route, f.targets, f.auth.Provider = "opus", []string{"claude-opus-5"}, "claude"
		f.auth.Attributes = map[string]string{AttributeAPIKey: "synthetic-alias-key"}
		cfg.ClaudeKey = []internalconfig.ClaudeKey{{APIKey: "synthetic-alias-key", Models: []internalconfig.ClaudeModel{{Name: f.targets[0], Alias: f.route}}}}
	case "claude-opus-pool":
		f.route, f.targets, f.auth.Provider = "opus", []string{"claude-opus-5", "claude-sonnet-4-6"}, "claude"
		f.auth.Attributes = map[string]string{AttributeAPIKey: "synthetic-alias-key"}
		cfg.ClaudeKey = []internalconfig.ClaudeKey{{APIKey: "synthetic-alias-key", Models: []internalconfig.ClaudeModel{
			{Name: f.targets[0], Alias: f.route}, {Name: f.targets[1], Alias: f.route},
		}}}
	case "compat-glm-pool":
		f.auth.Provider = openAICompatPoolProviderKey
		f.auth.Attributes = map[string]string{AttributeAPIKey: "synthetic-alias-key", "compat_name": "pool", "provider_key": openAICompatPoolProviderKey}
		cfg.OpenAICompatibility = []internalconfig.OpenAICompatibility{{Name: "pool", Models: []internalconfig.OpenAICompatibilityModel{
			{Name: f.targets[0], Alias: f.route}, {Name: f.targets[1], Alias: f.route},
		}}}
	default:
		t.Fatalf("unknown fixture shape %s", shape)
	}
	f.manager = NewManager(nil, selector, nil)
	f.manager.SetConfig(cfg)
	f.manager.SetRetryConfig(0, 0, 0)
	f.executor = &aliasModelPoolExecutor{provider: f.auth.Provider}
	f.manager.RegisterExecutor(f.executor)
	models := []*registry.ModelInfo{{ID: f.route}}
	if oauth {
		models[0].ExecutionTarget = f.targets[0]
		for _, target := range f.targets {
			models = append(models, &registry.ModelInfo{ID: target})
		}
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(f.auth.ID, f.auth.Provider, models)
	t.Cleanup(func() { reg.UnregisterClient(f.auth.ID) })
	if _, err := f.manager.Register(context.Background(), f.auth); err != nil {
		t.Fatal(err)
	}
	if seedCooldowns {
		f.cool("unrelated-model", false)
		if oauth || len(f.targets) > 1 {
			f.cool(f.route, false)
		}
	}
	f.stateKey = f.route
	if oauth {
		aliases := make([]internalconfig.OAuthModelAlias, 0, len(f.targets))
		for _, target := range f.targets {
			aliases = append(aliases, internalconfig.OAuthModelAlias{Name: target, Alias: f.route, Fork: true})
		}
		f.manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{f.auth.Provider: aliases})
		// OAuth aliases resolve one target, unlike configured same-key model pools.
		f.targets = f.targets[:1]
		f.stateKey = f.targets[0]
	} else if len(f.targets) > 1 {
		// A cooling first target must not hide a healthy later pool member.
		if seedCooldowns {
			f.cool(f.targets[0], false)
		}
		f.stateKey = f.targets[1]
	}
	return f
}

func (f aliasAvailabilityFixture) cool(model string, credentialScope bool) {
	retryAfter := time.Hour
	f.manager.MarkResult(context.Background(), Result{
		AuthID: f.auth.ID, Provider: f.auth.Provider, Model: model, CredentialScope: credentialScope, RetryAfter: &retryAfter,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "synthetic quota"},
	})
}

func (f aliasAvailabilityFixture) run(t *testing.T, path, mode string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	opts := cliproxyexecutor.Options{Metadata: map[string]any{}}
	switch mode {
	case "explicit":
		opts.Headers = http.Header{"X-Session-Id": {"alias-availability-session"}}
	case "lcp":
		opts.SourceFormat = sdktranslator.FormatOpenAI
		opts.OriginalRequest = []byte(`{"messages":[{"role":"system","content":"alias availability"},{"role":"user","content":"hello"}]}`)
		opts.Metadata[cliproxyexecutor.CallerScopeMetadataKey] = t.Name()
	}
	req := cliproxyexecutor.Request{Model: f.route}
	var payload string
	var err error
	switch path {
	case "select":
		var selected *Auth
		selected, err = f.manager.SelectAuth(ctx, f.auth.Provider, f.route, opts)
		if selected != nil {
			payload = selected.ID
			stored, _ := f.manager.GetByID(f.auth.ID)
			if state := stored.ModelStates["unrelated-model"]; state != nil && state.Quota.Exceeded {
				if selected.ModelStates["unrelated-model"] == nil || !selected.ModelStates["unrelated-model"].Quota.Exceeded {
					t.Error("selection lost unrelated cooldown state")
				}
			}
		}
	case "execute":
		var response cliproxyexecutor.Response
		response, err = f.manager.Execute(ctx, []string{f.auth.Provider}, req, opts)
		payload = string(response.Payload)
	case "stream":
		opts.Stream = true
		var stream *cliproxyexecutor.StreamResult
		stream, err = f.manager.ExecuteStream(ctx, []string{f.auth.Provider}, req, opts)
		if err == nil {
			for stream.Chunks != nil {
				select {
				case chunk, ok := <-stream.Chunks:
					if !ok {
						stream.Chunks = nil
						continue
					}
					if chunk.Err != nil {
						err = chunk.Err
					}
					payload += string(chunk.Payload)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
		}
	}
	if err == nil && mode == "lcp" && path == "select" {
		affinity := f.manager.Selector().(*SessionAffinitySelector)
		namespace := lcpAffinityNamespace(f.auth.Provider, f.route, opts.Metadata)
		match, ok := affinity.matcher.Match(namespace, cliproxysession.ExtractCanonicalTurns(opts.SourceFormat, opts.OriginalRequest))
		if !ok || match.AuthID != f.auth.ID || match.SessionID == "" {
			t.Errorf("expected LCP binding for selected auth, got %+v, found=%v", match, ok)
		}
	}
	return payload, err
}

func TestManagerAliasAvailabilityMatchesExecution(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for strategy, newSelector := range map[string]func() Selector{
		"weight-robin":         func() Selector { return &WeightedRobinSelector{} },
		"round-robin":          func() Selector { return &RoundRobinSelector{} },
		"weighted-round-robin": func() Selector { return &WeightedRoundRobinSelector{} },
		"fill-first":           func() Selector { return &FillFirstSelector{} },
	} {
		for _, mode := range []string{"unwrapped", "no-session", "explicit", "lcp"} {
			for _, shape := range []string{"oauth-glm", "oauth-opus", "claude-key-opus", "compat-glm-pool", "claude-opus-pool"} {
				for _, path := range []string{"select", "execute", "stream"} {
					for _, cooldown := range []string{"target", "account"} {
						t.Run(strategy+"/"+mode+"/"+shape+"/"+path+"/"+cooldown, func(t *testing.T) {
							selector := newSelector()
							if mode != "unwrapped" {
								affinity := NewSessionAffinitySelector(selector)
								t.Cleanup(affinity.Stop)
								selector = affinity
							}
							f := newAliasAvailabilityFixture(t, selector, shape, true)
							want := f.auth.ID
							if path != "select" {
								want += ":" + f.targets[len(f.targets)-1]
							}
							if got, err := f.run(t, path, mode); err != nil || got != want {
								t.Fatalf("healthy target: payload=%q error=%v, want %q", got, err, want)
							}
							f.cool(f.stateKey, cooldown == "account")
							before := len(f.executor.ExecuteCalls()) + len(f.executor.StreamCalls())
							if _, err := f.run(t, path, mode); statusCodeFromError(err) != http.StatusTooManyRequests {
								t.Fatalf("%s cooldown: error=%v, want 429", cooldown, err)
							}
							if after := len(f.executor.ExecuteCalls()) + len(f.executor.StreamCalls()); after != before {
								t.Fatalf("upstream executed while blocked: calls before=%d after=%d", before, after)
							}
						})
					}
				}
			}
		}
	}
}
