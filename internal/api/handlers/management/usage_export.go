package management

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/keeperexport"
)

type usageExportRuntime interface {
	ManagementStatus(context.Context) (keeperexport.StatusResponse, error)
}

func (h *Handler) SetUsageExportRuntime(runtime usageExportRuntime) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.usageExportRuntime = runtime
	h.mu.Unlock()
}

func (h *Handler) GetUsageExportSettings(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	h.mu.Lock()
	if h.cfg == nil {
		h.mu.Unlock()
		writeUsageExportError(c, keeperexport.StableError("internal_error"))
		return
	}
	settings := settingsFromConfig(h.cfg.UsageExport)
	h.mu.Unlock()
	settings.Keeper.TokenConfigured = h.cfg.UsageExport.Keeper.UsageExportTokenConfigured()
	writeUsageExportJSON(c, http.StatusOK, mustSettingsResponse(settings))
}

func (h *Handler) PutUsageExportSettings(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	body, perr := readUsageExportBody(c)
	if perr != nil {
		writeUsageExportError(c, perr)
		return
	}
	settings, perr := keeperexport.DecodeSettingsPutRequest(body)
	if perr != nil {
		writeUsageExportError(c, perr)
		return
	}

	h.mu.Lock()
	if h.cfg == nil {
		h.mu.Unlock()
		writeUsageExportError(c, keeperexport.StableError("internal_error"))
		return
	}
	previous := h.cfg.UsageExport
	h.cfg.UsageExport = configFromSettings(*settings, previous.Keeper.Token)
	if err := h.cfg.ValidateUsageExport(h.configFilePath); err != nil {
		h.cfg.UsageExport = previous
		h.mu.Unlock()
		writeUsageExportError(c, keeperexport.StableError("invalid_settings"))
		return
	}
	if err := config.SaveConfigPreserveComments(h.configFilePath, h.cfg); err != nil {
		h.cfg.UsageExport = previous
		h.mu.Unlock()
		writeUsageExportError(c, keeperexport.StableError("storage_error"))
		return
	}
	snapshot := h.reloadSnapshotConfigLocked()
	updated := h.cfg
	effective := settingsFromConfig(updated.UsageExport)
	h.mu.Unlock()

	h.applyRuntimeConfig(updated)
	var reqCtx context.Context
	if c.Request != nil {
		reqCtx = c.Request.Context()
	}
	h.reloadConfigAfterManagementSaveAsync(reqCtx, snapshot)
	effective.Keeper.TokenConfigured = updated.UsageExport.Keeper.UsageExportTokenConfigured()
	writeUsageExportJSON(c, http.StatusOK, mustSettingsResponse(effective))
}

func (h *Handler) TestUsageExportConnection(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	body, perr := readUsageExportBody(c)
	if perr != nil {
		writeUsageExportError(c, perr)
		return
	}
	settings, current, perr := keeperexport.DecodeConnectionTestRequest(body)
	if perr != nil {
		writeUsageExportError(c, perr)
		return
	}
	var cfg config.UsageExportConfig
	if current {
		h.mu.Lock()
		if h.cfg == nil {
			h.mu.Unlock()
			writeUsageExportError(c, keeperexport.StableError("internal_error"))
			return
		}
		cfg = h.cfg.UsageExport
		h.mu.Unlock()
	} else {
		cfg = configFromSettings(*settings, "")
	}
	validation := &config.Config{UsageStatisticsEnabled: true, UsageExport: cfg}
	if err := validation.ValidateUsageExport(h.configFilePath); err != nil {
		writeUsageExportError(c, keeperexport.StableError("invalid_settings"))
		return
	}
	result, perr := keeperexport.TestConnection(c.Request.Context(), cfg)
	if perr != nil {
		writeUsageExportError(c, perr)
		return
	}
	encoded, err := keeperexport.MarshalConnectionTestResponse(*result)
	if err != nil {
		writeUsageExportError(c, keeperexport.StableError("internal_error"))
		return
	}
	writeUsageExportJSON(c, http.StatusOK, encoded)
}

func (h *Handler) GetUsageExportStatus(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	h.mu.Lock()
	runtime := h.usageExportRuntime
	cfg := h.cfg
	h.mu.Unlock()
	if runtime == nil {
		status := keeperexport.StatusResponse{
			State: keeperexport.StateDisabled, MetadataRevisions: zeroMetadataRevisions(),
		}
		if cfg != nil {
			status.Enabled = cfg.UsageExport.Enabled
			status.TokenConfigured = cfg.UsageExport.Keeper.UsageExportTokenConfigured()
			if status.Enabled {
				status.State = keeperexport.StateStarting
			}
		}
		writeStatus(c, status)
		return
	}
	status, err := runtime.ManagementStatus(c.Request.Context())
	if err != nil {
		writeUsageExportError(c, keeperexport.StableError("internal_error"))
		return
	}
	writeStatus(c, status)
}

func readUsageExportBody(c *gin.Context) ([]byte, *keeperexport.Error) {
	if c.Request.Header.Get("Content-Encoding") != "" {
		return nil, keeperexport.StableError("invalid_field")
	}
	mediaType, params, err := mime.ParseMediaType(c.Request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || (len(params) > 0 && !strings.EqualFold(params["charset"], "utf-8")) {
		return nil, keeperexport.StableError("invalid_field")
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, keeperexport.MaxBodyBytes+1))
	if err != nil {
		return nil, keeperexport.StableError("invalid_json")
	}
	if len(body) > keeperexport.MaxBodyBytes {
		return nil, keeperexport.StableError("request_too_large")
	}
	return body, nil
}

func writeStatus(c *gin.Context, status keeperexport.StatusResponse) {
	if perr := keeperexport.ValidateStatusResponse(status); perr != nil {
		writeUsageExportError(c, keeperexport.StableError("internal_error"))
		return
	}
	encoded, err := keeperexport.MarshalStatusResponse(status)
	if err != nil {
		writeUsageExportError(c, keeperexport.StableError("internal_error"))
		return
	}
	writeUsageExportJSON(c, http.StatusOK, encoded)
}

func writeUsageExportError(c *gin.Context, perr *keeperexport.Error) {
	if perr == nil {
		perr = keeperexport.StableError("internal_error")
	}
	writeUsageExportJSON(c, perr.HTTPStatus, mustJSON(struct {
		ProtocolVersion string              `json:"protocolVersion"`
		Error           *keeperexport.Error `json:"error"`
	}{keeperexport.ProtocolVersion, perr}))
}

func writeUsageExportJSON(c *gin.Context, status int, data []byte) {
	c.Header("Content-Type", "application/json; charset=utf-8")
	c.Status(status)
	_, _ = c.Writer.Write(data)
}

func mustSettingsResponse(settings keeperexport.Settings) []byte {
	encoded, err := keeperexport.MarshalSettingsResponse(settings)
	if err != nil {
		return mustJSON(map[string]any{"protocolVersion": keeperexport.ProtocolVersion})
	}
	return encoded
}

func mustJSON(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}

func settingsFromConfig(cfg config.UsageExportConfig) keeperexport.Settings {
	return keeperexport.Settings{
		Enabled: cfg.Enabled, Mode: cfg.Mode,
		Keeper:   keeperexport.KeeperSettings{URL: cfg.Keeper.URL, TokenEnv: cfg.Keeper.TokenEnv, CAFile: cfg.Keeper.CAFile, ClientCertFile: cfg.Keeper.ClientCertFile, ClientKeyFile: cfg.Keeper.ClientKeyFile},
		Outbox:   keeperexport.OutboxSettings{Path: cfg.Outbox.Path, MaxBytes: cfg.Outbox.MaxBytes},
		Delivery: keeperexport.DeliverySettings{MaxBatchEvents: cfg.Delivery.MaxBatchEvents, MaxBatchBytes: cfg.Delivery.MaxBatchBytes, FlushIntervalMs: cfg.Delivery.FlushIntervalMs, RequestTimeoutMs: cfg.Delivery.RequestTimeoutMs, InitialBackoffMs: cfg.Delivery.InitialBackoffMs, MaxBackoffMs: cfg.Delivery.MaxBackoffMs},
		Metadata: keeperexport.MetadataSettings{Enabled: cfg.Metadata.Enabled, IntervalMs: cfg.Metadata.IntervalMs, Categories: append([]string(nil), cfg.Metadata.Categories...)},
		Privacy:  keeperexport.PrivacySettings{IncludeClientIP: cfg.Privacy.IncludeClientIP, IncludeForwardedFor: cfg.Privacy.IncludeForwardedFor, IncludeUserAgent: cfg.Privacy.IncludeUserAgent},
	}
}

func configFromSettings(settings keeperexport.Settings, directToken string) config.UsageExportConfig {
	return config.UsageExportConfig{
		Enabled: settings.Enabled, Mode: settings.Mode,
		Keeper:   config.UsageExportKeeperConfig{URL: settings.Keeper.URL, Token: directToken, TokenEnv: settings.Keeper.TokenEnv, CAFile: settings.Keeper.CAFile, ClientCertFile: settings.Keeper.ClientCertFile, ClientKeyFile: settings.Keeper.ClientKeyFile},
		Outbox:   config.UsageExportOutboxConfig{Path: settings.Outbox.Path, MaxBytes: settings.Outbox.MaxBytes},
		Delivery: config.UsageExportDeliveryConfig{MaxBatchEvents: settings.Delivery.MaxBatchEvents, MaxBatchBytes: settings.Delivery.MaxBatchBytes, FlushIntervalMs: settings.Delivery.FlushIntervalMs, RequestTimeoutMs: settings.Delivery.RequestTimeoutMs, InitialBackoffMs: settings.Delivery.InitialBackoffMs, MaxBackoffMs: settings.Delivery.MaxBackoffMs},
		Metadata: config.UsageExportMetadataConfig{Enabled: settings.Metadata.Enabled, IntervalMs: settings.Metadata.IntervalMs, Categories: append([]string(nil), settings.Metadata.Categories...)},
		Privacy:  config.UsageExportPrivacyConfig{IncludeClientIP: settings.Privacy.IncludeClientIP, IncludeForwardedFor: settings.Privacy.IncludeForwardedFor, IncludeUserAgent: settings.Privacy.IncludeUserAgent},
	}
}

func zeroMetadataRevisions() map[string]int64 {
	return map[string]int64{"auth_files": 0, "api_keys": 0, "provider_identities": 0}
}
