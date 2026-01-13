// Package warmup provides automatic credential warmup functionality.
// It periodically sends minimal requests to keep rate limit timers active,
// ensuring fresh quota is available when needed.
package warmup

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

// Manager orchestrates periodic warmup requests for configured providers.
type Manager struct {
	authManager *auth.Manager
	cfg         atomic.Pointer[config.WarmupConfig]
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	mu          sync.Mutex
	running     bool
}

// NewManager creates a new warmup manager.
func NewManager(authManager *auth.Manager) *Manager {
	return &Manager{
		authManager: authManager,
	}
}

// Start begins the warmup scheduler for all configured providers.
// It spawns a goroutine for each enabled provider that ticks at the configured interval.
func (m *Manager) Start(ctx context.Context, cfg *config.Config) {
	if m == nil || cfg == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running {
		return
	}

	if !cfg.Warmup.Enabled {
		log.Debug("warmup: feature disabled")
		return
	}

	warmupCfg := cfg.Warmup
	m.cfg.Store(&warmupCfg)

	ctx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.running = true

	for provider, providerCfg := range warmupCfg.Providers {
		if !providerCfg.Enabled || providerCfg.Interval <= 0 {
			continue
		}
		m.startProviderWarmup(ctx, provider, providerCfg)
	}

	log.Infof("warmup: started for %d provider(s)", len(warmupCfg.Providers))
}

// Stop gracefully stops all warmup goroutines.
func (m *Manager) Stop() {
	if m == nil {
		return
	}

	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return
	}
	m.running = false
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Unlock()

	m.wg.Wait()
	log.Debug("warmup: stopped")
}

// UpdateConfig updates the warmup configuration.
// This restarts warmup goroutines with new settings.
func (m *Manager) UpdateConfig(cfg *config.Config) {
	if m == nil || cfg == nil {
		return
	}

	m.mu.Lock()
	wasRunning := m.running
	m.mu.Unlock()

	if wasRunning {
		m.Stop()
	}

	if cfg.Warmup.Enabled {
		m.Start(context.Background(), cfg)
	}
}

// startProviderWarmup launches a goroutine that periodically warms up credentials for a provider.
func (m *Manager) startProviderWarmup(ctx context.Context, provider string, cfg config.ProviderWarmupConfig) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()

		log.Infof("warmup: starting for %s (interval: %v)", provider, cfg.Interval)

		ticker := time.NewTicker(cfg.Interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				log.Debugf("warmup: stopping for %s", provider)
				return
			case <-ticker.C:
				m.warmupProvider(ctx, provider, cfg)
			}
		}
	}()
}

// warmupProvider executes warmup requests for all credentials of a provider.
func (m *Manager) warmupProvider(ctx context.Context, provider string, cfg config.ProviderWarmupConfig) {
	if m.authManager == nil {
		return
	}

	auths := m.authManager.ListByProvider(provider)
	if len(auths) == 0 {
		log.Debugf("warmup: no credentials found for %s", provider)
		return
	}

	log.Debugf("warmup: starting for %s with %d credential(s)", provider, len(auths))

	for _, a := range auths {
		if a.Disabled {
			log.Debugf("warmup: skipping disabled credential %s for %s", a.EnsureIndex(), provider)
			continue
		}

		// Execute warmup with timeout
		warmupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := m.executeWarmup(warmupCtx, provider, a, cfg)
		cancel()

		if err != nil {
			log.Warnf("warmup: failed for %s auth %s: %v", provider, a.EnsureIndex(), err)
		} else {
			log.Infof("warmup: completed for %s auth %s", provider, a.EnsureIndex())
		}

		// Small delay between credentials to avoid burst
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// executeWarmup sends a minimal request to warm up a credential.
func (m *Manager) executeWarmup(ctx context.Context, provider string, a *auth.Auth, cfg config.ProviderWarmupConfig) error {
	message := cfg.Message
	if message == "" {
		message = "hi"
	}

	model := cfg.Model
	if model == "" {
		model = getDefaultModelForProvider(provider)
	}

	// Build minimal chat completion payload
	payload := buildMinimalChatPayload(message)

	req := executor.Request{
		Model:   model,
		Payload: payload,
	}

	opts := executor.Options{
		Stream: false,
		Metadata: map[string]any{
			"warmup": true,
		},
	}

	_, err := m.authManager.ExecuteWithAuth(ctx, a, req, opts)
	return err
}

// getDefaultModelForProvider returns a default model name for warmup.
// This can be overridden in the config.
func getDefaultModelForProvider(provider string) string {
	provider = strings.ToLower(provider)
	switch provider {
	case "gemini-cli", "gemini":
		return "gemini-2.0-flash"
	case "claude", "anthropic":
		return "claude-sonnet-4-20250514"
	case "antigravity":
		return "gemini-2.5-pro"
	default:
		return "gpt-4o-mini"
	}
}

// buildMinimalChatPayload creates a minimal OpenAI-compatible chat payload.
func buildMinimalChatPayload(message string) []byte {
	payload := map[string]any{
		"messages": []map[string]string{
			{
				"role":    "user",
				"content": message,
			},
		},
		"max_tokens": 1,
	}
	data, _ := json.Marshal(payload)
	return data
}
