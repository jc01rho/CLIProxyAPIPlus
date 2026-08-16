package keeperexport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// StableError returns a copy of the frozen protocol error for code.
func StableError(code string) *Error { return protocolError(code) }

// DecodeConnectionTestRequest decodes the complete, strict connection-test
// body. current is true only when settings is explicitly JSON null.
func DecodeConnectionTestRequest(data []byte) (settings *Settings, current bool, perr *Error) {
	if perr = requestPrecheck(data); perr != nil {
		return nil, false, perr
	}
	var wire struct {
		ProtocolVersion string          `json:"protocolVersion"`
		Settings        json.RawMessage `json:"settings"`
	}
	if perr = decodeTyped(data, &wire); perr != nil {
		return nil, false, perr
	}
	if perr = requireKeys(data, "protocolVersion", "settings"); perr != nil {
		return nil, false, perr
	}
	if bytes.Equal(bytes.TrimSpace(wire.Settings), []byte("null")) {
		return nil, true, nil
	}
	nested, err := json.Marshal(struct {
		ProtocolVersion string          `json:"protocolVersion"`
		Settings        json.RawMessage `json:"settings"`
	}{ProtocolVersion: ProtocolVersion, Settings: wire.Settings})
	if err != nil {
		return nil, false, protocolError("invalid_field")
	}
	settings, perr = DecodeSettingsPutRequest(nested)
	return settings, false, perr
}

// TestConnection performs only the non-mutating identity operation. It never
// opens or binds an outbox and never starts an exporter worker.
func TestConnection(ctx context.Context, cfg config.UsageExportConfig) (*ConnectionTestResponse, *Error) {
	if ctx == nil {
		ctx = context.Background()
	}
	token := cfg.Keeper.UsageExportToken()
	if token == "" {
		return nil, protocolError("token_env_unset")
	}
	client, err := newHTTPClient(cfg.Keeper, time.Duration(cfg.Delivery.RequestTimeoutMs)*time.Millisecond)
	if err != nil {
		return nil, protocolError("keeper_tls_error")
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if transport, ok := client.Transport.(*http.Transport); ok {
		defer transport.CloseIdleConnections()
	}

	url := strings.TrimRight(cfg.Keeper.URL, "/") + "/api/v1/export/identity"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, protocolError("keeper_unreachable")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	started := time.Now()
	response, err := client.Do(req)
	token = ""
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
			return nil, protocolError("keeper_timeout")
		}
		if isTLSValidationError(err) {
			return nil, protocolError("keeper_tls_error")
		}
		return nil, protocolError("keeper_unreachable")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxBodyBytes+1))
	if err != nil {
		return nil, protocolError("keeper_unreachable")
	}
	if len(body) > MaxBodyBytes {
		return nil, protocolError("keeper_invalid_response")
	}
	if response.StatusCode != http.StatusOK {
		remoteErr := decodeRemoteFailure(response.StatusCode, body, http.Header{})
		var stable *Error
		if errors.As(remoteErr, &stable) {
			return nil, stable
		}
		return nil, protocolError("keeper_invalid_response")
	}
	if perr := validateKeeperResponseHeaders(response, MaxBodyBytes, false); perr != nil {
		return nil, perr
	}
	identity, decodeErr := DecodeIdentityResponse(body)
	if decodeErr != nil {
		return nil, protocolError("keeper_invalid_response")
	}
	if !contains(identity.Credential.Scopes, "identity:test") {
		return nil, protocolError("insufficient_scope")
	}
	latency := time.Since(started).Milliseconds()
	if latency < 0 {
		latency = 0
	}
	return &ConnectionTestResponse{
		OK:               true,
		Instance:         identity.Instance,
		CredentialScopes: append([]string(nil), identity.Credential.Scopes...),
		LatencyMs:        latency,
		TestedAt:         formatTimestamp(time.Now()),
	}, nil
}

func formatTimestamp(value time.Time) string {
	return value.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
}

func stringTime(value time.Time) *string {
	if value.IsZero() {
		return nil
	}
	formatted := formatTimestamp(value)
	return &formatted
}

// MarshalSettingsResponse encodes the frozen redacted settings response.
func MarshalSettingsResponse(settings Settings) ([]byte, error) {
	wire := settingsEnvelopeReadWire{ProtocolVersion: ProtocolVersion}
	wire.Settings.Enabled = settings.Enabled
	wire.Settings.Mode = settings.Mode
	wire.Settings.Keeper = keeperSettingsReadWire{
		URL: settings.Keeper.URL, TokenEnv: settings.Keeper.TokenEnv,
		TokenConfigured: settings.Keeper.TokenConfigured, CAFile: settings.Keeper.CAFile,
		ClientCertFile: settings.Keeper.ClientCertFile, ClientKeyFile: settings.Keeper.ClientKeyFile,
	}
	wire.Settings.Outbox = outboxSettingsWire{Path: settings.Outbox.Path, MaxBytes: settings.Outbox.MaxBytes}
	wire.Settings.Delivery = deliverySettingsWire{
		MaxBatchEvents: settings.Delivery.MaxBatchEvents, MaxBatchBytes: settings.Delivery.MaxBatchBytes,
		FlushIntervalMs: settings.Delivery.FlushIntervalMs, RequestTimeoutMs: settings.Delivery.RequestTimeoutMs,
		InitialBackoffMs: settings.Delivery.InitialBackoffMs, MaxBackoffMs: settings.Delivery.MaxBackoffMs,
	}
	wire.Settings.Metadata = metadataSettingsWire{Enabled: settings.Metadata.Enabled, IntervalMs: settings.Metadata.IntervalMs, Categories: settings.Metadata.Categories}
	wire.Settings.Privacy = privacySettingsWire{IncludeClientIP: settings.Privacy.IncludeClientIP, IncludeForwardedFor: settings.Privacy.IncludeForwardedFor, IncludeUserAgent: settings.Privacy.IncludeUserAgent}
	return json.Marshal(wire)
}

// MarshalConnectionTestResponse encodes the frozen connection-test response.
func MarshalConnectionTestResponse(response ConnectionTestResponse) ([]byte, error) {
	return json.Marshal(struct {
		ProtocolVersion  string      `json:"protocolVersion"`
		OK               bool        `json:"ok"`
		Instance         InstanceRef `json:"instance"`
		CredentialScopes []string    `json:"credentialScopes"`
		LatencyMs        int64       `json:"latencyMs"`
		TestedAt         string      `json:"testedAt"`
	}{ProtocolVersion, response.OK, response.Instance, response.CredentialScopes, response.LatencyMs, response.TestedAt})
}

// ValidateStatusResponse validates the exact frozen runtime status shape and
// state/field invariants using the same strict decoder used by consumers.
func ValidateStatusResponse(response StatusResponse) *Error {
	encoded, err := MarshalStatusResponse(response)
	if err != nil {
		return protocolError("invalid_field")
	}
	_, perr := DecodeStatusResponse(encoded)
	return perr
}

// MarshalStatusResponse encodes the frozen redacted runtime status response.
func MarshalStatusResponse(response StatusResponse) ([]byte, error) {
	type instanceWire struct {
		InstanceID  string `json:"instanceId"`
		DisplayName string `json:"displayName"`
	}
	type errorWire struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Retryable bool   `json:"retryable"`
		At        string `json:"at"`
	}
	var instance *instanceWire
	if response.Instance != nil {
		instance = &instanceWire{response.Instance.InstanceID, response.Instance.DisplayName}
	}
	var lastError *errorWire
	if response.LastError != nil {
		lastError = &errorWire{response.LastError.Code, response.LastError.Message, response.LastError.Retryable, response.LastError.At}
	}
	return json.Marshal(struct {
		ProtocolVersion      string           `json:"protocolVersion"`
		State                ExporterState    `json:"state"`
		Enabled              bool             `json:"enabled"`
		TokenConfigured      bool             `json:"tokenConfigured"`
		Instance             *instanceWire    `json:"instance"`
		StreamID             *string          `json:"streamId"`
		NextSequence         *int64           `json:"nextSequence"`
		AcknowledgedThrough  *int64           `json:"acknowledgedThrough"`
		NextExpectedSequence *int64           `json:"nextExpectedSequence"`
		BacklogEvents        int64            `json:"backlogEvents"`
		BacklogBytes         int64            `json:"backlogBytes"`
		OldestBacklogAt      *string          `json:"oldestBacklogAt"`
		LastAttemptAt        *string          `json:"lastAttemptAt"`
		LastSuccessAt        *string          `json:"lastSuccessAt"`
		NextRetryAt          *string          `json:"nextRetryAt"`
		MetadataRevisions    map[string]int64 `json:"metadataRevisions"`
		LastError            *errorWire       `json:"lastError"`
	}{ProtocolVersion, response.State, response.Enabled, response.TokenConfigured, instance, response.StreamID,
		response.NextSequence, response.AcknowledgedThrough, response.NextExpectedSequence, response.BacklogEvents,
		response.BacklogBytes, response.OldestBacklogAt, response.LastAttemptAt, response.LastSuccessAt,
		response.NextRetryAt, response.MetadataRevisions, lastError})
}
