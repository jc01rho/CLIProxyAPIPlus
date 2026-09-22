package executor

import (
	"context"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

const (
	mimocodeDefaultBaseURL = "https://api.xiaomimimo.com/v1"
	mimocodeSourceHeader   = "X-Mimo-Source"
	mimocodeSourceValue    = "mimocode-cli"
)

// MimocodeExecutor reuses the OpenAI-compatible execution path while applying
// MiMo's endpoint and client-source defaults.
type MimocodeExecutor struct {
	inner *OpenAICompatExecutor
}

func NewMimocodeExecutor(cfg *config.Config) *MimocodeExecutor {
	return &MimocodeExecutor{inner: NewOpenAICompatExecutor("mimocode", cfg)}
}

func (e *MimocodeExecutor) Identifier() string { return "mimocode" }

func (e *MimocodeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	resp, err := e.inner.Execute(ctx, prepareMimocodeAuth(auth), req, opts)
	return resp, relabelMimocodeError(err)
}

func (e *MimocodeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	resp, err := e.inner.ExecuteStream(ctx, prepareMimocodeAuth(auth), req, opts)
	return resp, relabelMimocodeError(err)
}

func (e *MimocodeExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.inner.CountTokens(ctx, prepareMimocodeAuth(auth), req, opts)
}

func (e *MimocodeExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	return e.inner.HttpRequest(ctx, prepareMimocodeAuth(auth), req)
}

func (e *MimocodeExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing mimocode auth"}
	}
	return auth, nil
}

func prepareMimocodeAuth(auth *cliproxyauth.Auth) *cliproxyauth.Auth {
	if auth == nil {
		auth = &cliproxyauth.Auth{Provider: "mimocode"}
	} else {
		auth = auth.Clone()
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	if strings.TrimSpace(auth.Attributes["base_url"]) == "" {
		auth.Attributes["base_url"] = mimocodeDefaultBaseURL
	}
	if !hasMimocodeSourceHeader(auth.Attributes) {
		auth.Attributes["header:"+mimocodeSourceHeader] = mimocodeSourceValue
	}
	return auth
}

func relabelMimocodeError(err error) error {
	status, ok := err.(statusErr)
	if !ok || status.code != http.StatusBadRequest {
		return err
	}
	code := gjson.Get(status.msg, "error.code")
	switch strings.TrimSpace(code.String()) {
	case "421":
		status.msg = "Request blocked by content moderation"
	case "441":
		status.msg = "Request blocked by risk control"
	}
	return status
}

func hasMimocodeSourceHeader(attrs map[string]string) bool {
	for key := range attrs {
		if !strings.HasPrefix(strings.ToLower(key), "header:") {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(key[len("header:"):]), mimocodeSourceHeader) {
			return true
		}
	}
	return false
}
