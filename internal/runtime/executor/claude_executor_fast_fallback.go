package executor

import (
	"bytes"
	"fmt"
	"io"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/sjson"
)

type claudeFastFallbackOptions struct {
	auth                     *cliproxyauth.Auth
	apiKey                   string
	stream                   bool
	extraBetas               []string
	body                     []byte
	fallbackBilling          string
	cchSigning               bool
	incomingHeaders          http.Header
	confirmedNative          bool
	sessionID                string
	allowEntitlementFallback bool
}

func (e *ClaudeExecutor) retryClaudeFastModeRefusal(
	ctxReq *http.Request,
	client *http.Client,
	initialResp *http.Response,
	options claudeFastFallbackOptions,
) (*http.Response, []byte, bool, error) {
	if initialResp == nil || ctxReq == nil || client == nil || !options.allowEntitlementFallback || initialResp.StatusCode != http.StatusTooManyRequests {
		return initialResp, options.body, false, nil
	}

	errorBody, errDecode := decodeResponseBody(initialResp.Body, initialResp.Header.Get("Content-Encoding"))
	if errDecode != nil {
		return nil, options.body, false, fmt.Errorf("decode Claude Fast refusal: %w", errDecode)
	}
	body, errRead := io.ReadAll(errorBody)
	if errClose := errorBody.Close(); errClose != nil {
		log.Errorf("response body close error: %v", errClose)
	}
	if errRead != nil {
		return nil, options.body, false, fmt.Errorf("read Claude Fast refusal: %w", errRead)
	}
	if !claudeBodyIndicatesFastModeCredits(body) {
		initialResp.Body = io.NopCloser(bytes.NewReader(body))
		initialResp.ContentLength = int64(len(body))
		initialResp.Header.Del("Content-Encoding")
		initialResp.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
		return initialResp, options.body, false, nil
	}

	helps.AppendAPIResponseChunk(ctxReq.Context(), e.cfg, body)
	fallbackBody, errDelete := sjson.DeleteBytes(options.body, "speed")
	if errDelete != nil {
		return nil, options.body, false, fmt.Errorf("remove Claude Fast speed: %w", errDelete)
	}
	if options.cchSigning {
		var errCCH error
		fallbackBody, errCCH = finalizeAnthropicMessagesBodyCCH(fallbackBody, options.fallbackBilling)
		if errCCH != nil {
			return nil, options.body, false, fmt.Errorf("re-finalize Claude CCH for Fast fallback: %w", errCCH)
		}
	}

	fallbackReq, errRequest := http.NewRequestWithContext(ctxReq.Context(), http.MethodPost, ctxReq.URL.String(), bytes.NewReader(fallbackBody))
	if errRequest != nil {
		return nil, options.body, false, fmt.Errorf("create Claude Fast fallback request: %w", errRequest)
	}
	fallbackBetas := append([]string(nil), options.extraBetas...)
	fallbackBetas = append(fallbackBetas, claudeFastModeBeta)
	if errHeaders := applyClaudeHeaders(
		fallbackReq,
		options.auth,
		options.apiKey,
		options.stream,
		fallbackBetas,
		fallbackBody,
		e.cfg,
		options.incomingHeaders,
		options.confirmedNative,
		options.sessionID,
	); errHeaders != nil {
		return nil, options.body, false, errHeaders
	}

	authID, authLabel, authType, authValue := claudeAuthLogIdentity(options.auth)
	helps.RecordAPIRequest(ctxReq.Context(), e.cfg, helps.UpstreamRequestLog{
		URL:       fallbackReq.URL.String(),
		Method:    http.MethodPost,
		Headers:   fallbackReq.Header.Clone(),
		Body:      fallbackBody,
		Provider:  e.upstreamRequestLogProvider(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	fallbackResp, errDo := doClaudeUpstreamRequest(client, fallbackReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctxReq.Context(), e.cfg, errDo)
		return nil, fallbackBody, true, errDo
	}
	helps.RecordAPIResponseMetadata(ctxReq.Context(), e.cfg, fallbackResp.StatusCode, fallbackResp.Header.Clone())
	return fallbackResp, fallbackBody, true, nil
}

func claudeAuthLogIdentity(auth *cliproxyauth.Auth) (id, label, authType, authValue string) {
	if auth == nil {
		return "", "", "", ""
	}
	authType, authValue = auth.AccountInfo()
	return auth.ID, auth.Label, authType, authValue
}
