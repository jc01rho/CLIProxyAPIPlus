package executor

import (
	"context"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestOpenAICompatTLSFingerprintSelectsChromeClient(t *testing.T) {
	newExecutor := func(fingerprint string) (*OpenAICompatExecutor, *cliproxyauth.Auth) {
		cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{Name: "rainby", TLSFingerprint: fingerprint}}}
		auth := &cliproxyauth.Auth{Provider: "rainby", Attributes: map[string]string{"compat_name": "rainby"}}
		return &OpenAICompatExecutor{cfg: cfg}, auth
	}
	plainExec, plainAuth := newExecutor("")
	chromeExec, chromeAuth := newExecutor("Chrome")
	plain := plainExec.newHTTPClient(context.Background(), plainAuth)
	chrome := chromeExec.newHTTPClient(context.Background(), chromeAuth)
	if reflect.TypeOf(plain.Transport) == reflect.TypeOf(chrome.Transport) {
		t.Fatalf("tls-fingerprint: chrome did not change the transport type (%T)", chrome.Transport)
	}
}
