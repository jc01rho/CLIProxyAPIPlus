package config

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestLoadConfigSecretRewriteKeepsConcurrentManagementSave(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "config.yaml")
	base := []byte("remote-management:\n  secret-key: plain-secret\nrequest-retry: 3\n")
	if errWrite := os.WriteFile(configFile, base, 0o600); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}

	saveDone := make(chan error, 1)
	saveWaiting := make(chan struct{})
	var waitSignaled, rewriteHooked atomic.Bool
	onConfigFileLockWait = func() {
		if waitSignaled.CompareAndSwap(false, true) {
			close(saveWaiting)
		}
	}
	// The first read-modify-write is LoadConfig hashing the plaintext secret. A
	// management save starts right after that rewrite read the file; the hook
	// returns once the save either finished or is blocked on the config lock.
	afterConfigRewriteRead = func() {
		if !rewriteHooked.CompareAndSwap(false, true) {
			return
		}
		go func() {
			saveDone <- SaveConfigPreserveCommentsUpdateNestedScalar(configFile, []string{"proxy-url"}, "socks5://saved")
		}()
		select {
		case errSave := <-saveDone:
			saveDone <- errSave
		case <-saveWaiting:
		}
	}
	t.Cleanup(func() {
		onConfigFileLockWait = nil
		afterConfigRewriteRead = nil
	})

	if _, errLoad := LoadConfig(configFile); errLoad != nil {
		t.Fatalf("LoadConfig: %v", errLoad)
	}
	if errSave := <-saveDone; errSave != nil {
		t.Fatalf("management save: %v", errSave)
	}

	reloaded, errReload := LoadConfig(configFile)
	if errReload != nil {
		t.Fatalf("config.yaml is no longer valid: %v", errReload)
	}
	if reloaded.ProxyURL != "socks5://saved" {
		t.Fatalf("management save lost to the secret rewrite: proxy-url=%q, want socks5://saved", reloaded.ProxyURL)
	}
	if !looksLikeBcrypt(reloaded.RemoteManagement.SecretKey) {
		t.Fatalf("secret key was not hashed: %q", reloaded.RemoteManagement.SecretKey)
	}
	if reloaded.RequestRetry != 3 {
		t.Fatalf("request-retry=%d, want 3", reloaded.RequestRetry)
	}
}
