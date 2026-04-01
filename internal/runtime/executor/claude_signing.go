package executor

import (
	"fmt"
	"regexp"
	"strings"

	xxHash64 "github.com/pierrec/xxHash/xxHash64"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

const claudeCCHSeed uint64 = 0x6E52736AC806831E

var claudeBillingHeaderPlaceholderPattern = regexp.MustCompile(`(x-anthropic-billing-header:[^"]*?\bcch=)(00000)(;)`)

func signAnthropicMessagesBody(body []byte) []byte {
	if !claudeBillingHeaderPlaceholderPattern.Match(body) {
		return body
	}

	cch := fmt.Sprintf("%05x", xxHash64.Checksum(body, claudeCCHSeed)&0xFFFFF)
	return claudeBillingHeaderPlaceholderPattern.ReplaceAll(body, []byte("${1}"+cch+"${3}"))
}

func resolveClaudeKeyConfig(cfg *config.Config, auth *cliproxyauth.Auth) *config.ClaudeKey {
	if cfg == nil || auth == nil {
		return nil
	}

	apiKey, baseURL := claudeCreds(auth)
	if apiKey == "" {
		return nil
	}

	for i := range cfg.ClaudeKey {
		entry := &cfg.ClaudeKey[i]
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if !strings.EqualFold(cfgKey, apiKey) {
			continue
		}
		if baseURL != "" && cfgBase != "" && !strings.EqualFold(cfgBase, baseURL) {
			continue
		}
		return entry
	}

	return nil
}

// resolveClaudeKeyCloakConfig finds the matching ClaudeKey config and returns its CloakConfig.
func resolveClaudeKeyCloakConfig(cfg *config.Config, auth *cliproxyauth.Auth) *config.CloakConfig {
	entry := resolveClaudeKeyConfig(cfg, auth)
	if entry == nil {
		return nil
	}
	return entry.Cloak
}

func experimentalCCHSigningEnabled(cfg *config.Config, auth *cliproxyauth.Auth) bool {
	entry := resolveClaudeKeyConfig(cfg, auth)
	return entry != nil && entry.ExperimentalCCHSigning
}
