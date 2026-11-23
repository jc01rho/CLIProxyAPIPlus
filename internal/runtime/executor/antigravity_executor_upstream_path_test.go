package executor

import "github.com/router-for-me/CLIProxyAPI/v8/internal/config"

// newAntigravityExecutorUpstreamNonStream builds an executor that sends
// non-stream requests to :generateContent like upstream. Upstream issue tests
// (7749/7751/7888) stub only that endpoint; the fork default streams non-stream
// requests through :streamGenerateContent (agy CLI wire), which those stubs do
// not model, so they opt into the upstream path here.
func newAntigravityExecutorUpstreamNonStream(cfg *config.Config) *AntigravityExecutor {
	executor := NewAntigravityExecutor(cfg)
	executor.generateContentForNonStream = true
	return executor
}
