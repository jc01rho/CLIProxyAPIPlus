package keeperexport

import (
	"encoding/json"
	"path"
)

// Settings is the typed usage-export settings object (contract section 8.1).
type Settings struct {
	Enabled  bool
	Mode     string
	Keeper   KeeperSettings
	Outbox   OutboxSettings
	Delivery DeliverySettings
	Metadata MetadataSettings
	Privacy  PrivacySettings
}

// KeeperSettings holds the Keeper connection settings. TokenConfigured is
// response-only; it is never accepted in a PUT body.
type KeeperSettings struct {
	URL             string
	TokenEnv        string
	TokenConfigured bool
	CAFile          *string
	ClientCertFile  *string
	ClientKeyFile   *string
}

type OutboxSettings struct {
	Path     string
	MaxBytes int64
}

type DeliverySettings struct {
	MaxBatchEvents   int64
	MaxBatchBytes    int64
	FlushIntervalMs  int64
	RequestTimeoutMs int64
	InitialBackoffMs int64
	MaxBackoffMs     int64
}

type MetadataSettings struct {
	Enabled    bool
	IntervalMs int64
	Categories []string
}

type PrivacySettings struct {
	IncludeClientIP     bool
	IncludeForwardedFor bool
	IncludeUserAgent    bool
}

// SettingsResponse is the typed GET settings response (contract section 8.1).
type SettingsResponse struct {
	Settings Settings
}

type keeperSettingsWriteWire struct {
	URL            string  `json:"url"`
	TokenEnv       string  `json:"tokenEnv"`
	CAFile         *string `json:"caFile"`
	ClientCertFile *string `json:"clientCertFile"`
	ClientKeyFile  *string `json:"clientKeyFile"`
}

type keeperSettingsReadWire struct {
	URL             string  `json:"url"`
	TokenEnv        string  `json:"tokenEnv"`
	TokenConfigured bool    `json:"tokenConfigured"`
	CAFile          *string `json:"caFile"`
	ClientCertFile  *string `json:"clientCertFile"`
	ClientKeyFile   *string `json:"clientKeyFile"`
}

type outboxSettingsWire struct {
	Path     string `json:"path"`
	MaxBytes int64  `json:"maxBytes"`
}

type deliverySettingsWire struct {
	MaxBatchEvents   int64 `json:"maxBatchEvents"`
	MaxBatchBytes    int64 `json:"maxBatchBytes"`
	FlushIntervalMs  int64 `json:"flushIntervalMs"`
	RequestTimeoutMs int64 `json:"requestTimeoutMs"`
	InitialBackoffMs int64 `json:"initialBackoffMs"`
	MaxBackoffMs     int64 `json:"maxBackoffMs"`
}

type metadataSettingsWire struct {
	Enabled    bool     `json:"enabled"`
	IntervalMs int64    `json:"intervalMs"`
	Categories []string `json:"categories"`
}

type privacySettingsWire struct {
	IncludeClientIP     bool `json:"includeClientIp"`
	IncludeForwardedFor bool `json:"includeForwardedFor"`
	IncludeUserAgent    bool `json:"includeUserAgent"`
}

// settingsEnvelopeWriteWire is the PUT body. The write-side keeper object has
// no tokenConfigured and no token field, so both are rejected as unknown
// fields per contract section 8.1.
type settingsEnvelopeWriteWire struct {
	ProtocolVersion string `json:"protocolVersion"`
	Settings        struct {
		Enabled  bool                    `json:"enabled"`
		Mode     string                  `json:"mode"`
		Keeper   keeperSettingsWriteWire `json:"keeper"`
		Outbox   outboxSettingsWire      `json:"outbox"`
		Delivery deliverySettingsWire    `json:"delivery"`
		Metadata metadataSettingsWire    `json:"metadata"`
		Privacy  privacySettingsWire     `json:"privacy"`
	} `json:"settings"`
}

type settingsEnvelopeReadWire struct {
	ProtocolVersion string `json:"protocolVersion"`
	Settings        struct {
		Enabled  bool                   `json:"enabled"`
		Mode     string                 `json:"mode"`
		Keeper   keeperSettingsReadWire `json:"keeper"`
		Outbox   outboxSettingsWire     `json:"outbox"`
		Delivery deliverySettingsWire   `json:"delivery"`
		Metadata metadataSettingsWire   `json:"metadata"`
		Privacy  privacySettingsWire    `json:"privacy"`
	} `json:"settings"`
}

// DecodeSettingsPutRequest strictly decodes and validates a complete settings
// replacement body. Token material and the response-only tokenConfigured
// field are unknown fields here.
func DecodeSettingsPutRequest(data []byte) (*Settings, *Error) {
	if perr := requestPrecheck(data); perr != nil {
		return nil, perr
	}
	if perr := requireSettingsPutKeys(data); perr != nil {
		return nil, perr
	}
	var wire settingsEnvelopeWriteWire
	if perr := decodeTyped(data, &wire); perr != nil {
		return nil, perr
	}
	settings := &Settings{
		Enabled: wire.Settings.Enabled,
		Mode:    wire.Settings.Mode,
		Keeper: KeeperSettings{
			URL:            wire.Settings.Keeper.URL,
			TokenEnv:       wire.Settings.Keeper.TokenEnv,
			CAFile:         wire.Settings.Keeper.CAFile,
			ClientCertFile: wire.Settings.Keeper.ClientCertFile,
			ClientKeyFile:  wire.Settings.Keeper.ClientKeyFile,
		},
		Outbox: OutboxSettings{
			Path:     wire.Settings.Outbox.Path,
			MaxBytes: wire.Settings.Outbox.MaxBytes,
		},
		Delivery: DeliverySettings{
			MaxBatchEvents:   wire.Settings.Delivery.MaxBatchEvents,
			MaxBatchBytes:    wire.Settings.Delivery.MaxBatchBytes,
			FlushIntervalMs:  wire.Settings.Delivery.FlushIntervalMs,
			RequestTimeoutMs: wire.Settings.Delivery.RequestTimeoutMs,
			InitialBackoffMs: wire.Settings.Delivery.InitialBackoffMs,
			MaxBackoffMs:     wire.Settings.Delivery.MaxBackoffMs,
		},
		Metadata: MetadataSettings{
			Enabled:    wire.Settings.Metadata.Enabled,
			IntervalMs: wire.Settings.Metadata.IntervalMs,
			Categories: wire.Settings.Metadata.Categories,
		},
		Privacy: PrivacySettings{
			IncludeClientIP:     wire.Settings.Privacy.IncludeClientIP,
			IncludeForwardedFor: wire.Settings.Privacy.IncludeForwardedFor,
			IncludeUserAgent:    wire.Settings.Privacy.IncludeUserAgent,
		},
	}
	if perr := ValidateSettings(settings); perr != nil {
		return nil, perr
	}
	return settings, nil
}

// DecodeSettingsResponse strictly decodes and validates a redacted effective
// settings response.
func DecodeSettingsResponse(data []byte) (*SettingsResponse, *Error) {
	if perr := responsePrecheck(data); perr != nil {
		return nil, perr
	}
	var wire settingsEnvelopeReadWire
	if perr := decodeTyped(data, &wire); perr != nil {
		return nil, perr
	}
	settings := &Settings{
		Enabled: wire.Settings.Enabled,
		Mode:    wire.Settings.Mode,
		Keeper: KeeperSettings{
			URL:             wire.Settings.Keeper.URL,
			TokenEnv:        wire.Settings.Keeper.TokenEnv,
			TokenConfigured: wire.Settings.Keeper.TokenConfigured,
			CAFile:          wire.Settings.Keeper.CAFile,
			ClientCertFile:  wire.Settings.Keeper.ClientCertFile,
			ClientKeyFile:   wire.Settings.Keeper.ClientKeyFile,
		},
		Outbox: OutboxSettings{
			Path:     wire.Settings.Outbox.Path,
			MaxBytes: wire.Settings.Outbox.MaxBytes,
		},
		Delivery: DeliverySettings{
			MaxBatchEvents:   wire.Settings.Delivery.MaxBatchEvents,
			MaxBatchBytes:    wire.Settings.Delivery.MaxBatchBytes,
			FlushIntervalMs:  wire.Settings.Delivery.FlushIntervalMs,
			RequestTimeoutMs: wire.Settings.Delivery.RequestTimeoutMs,
			InitialBackoffMs: wire.Settings.Delivery.InitialBackoffMs,
			MaxBackoffMs:     wire.Settings.Delivery.MaxBackoffMs,
		},
		Metadata: MetadataSettings{
			Enabled:    wire.Settings.Metadata.Enabled,
			IntervalMs: wire.Settings.Metadata.IntervalMs,
			Categories: wire.Settings.Metadata.Categories,
		},
		Privacy: PrivacySettings{
			IncludeClientIP:     wire.Settings.Privacy.IncludeClientIP,
			IncludeForwardedFor: wire.Settings.Privacy.IncludeForwardedFor,
			IncludeUserAgent:    wire.Settings.Privacy.IncludeUserAgent,
		},
	}
	if perr := ValidateSettings(settings); perr != nil {
		return nil, perr
	}
	return &SettingsResponse{Settings: *settings}, nil
}

// ValidateSettings enforces the normative settings rules of contract section
// 8.1. Every violation maps to invalid_settings.
func ValidateSettings(s *Settings) *Error {
	invalid := func() *Error { return protocolError("invalid_settings") }
	push := s.Enabled && s.Mode == "push"
	disabled := !s.Enabled && s.Mode == "disabled"
	if !push && !disabled {
		return invalid()
	}
	if s.Keeper.URL == "" {
		if push {
			return invalid()
		}
	} else if !isHTTPOrigin(s.Keeper.URL) {
		return invalid()
	}
	if s.Keeper.TokenEnv != "" && !envVarNameRE.MatchString(s.Keeper.TokenEnv) {
		return invalid()
	}
	for _, file := range []*string{s.Keeper.CAFile, s.Keeper.ClientCertFile, s.Keeper.ClientKeyFile} {
		if file != nil && (!path.IsAbs(*file) || len(*file) > 4096) {
			return invalid()
		}
	}
	if (s.Keeper.ClientCertFile == nil) != (s.Keeper.ClientKeyFile == nil) {
		return invalid()
	}
	if !path.IsAbs(s.Outbox.Path) || len(s.Outbox.Path) > 4096 {
		return invalid()
	}
	if s.Outbox.MaxBytes < 16<<20 || s.Outbox.MaxBytes > 1<<40 {
		return invalid()
	}
	d := s.Delivery
	if d.MaxBatchEvents < 1 || d.MaxBatchEvents > 500 ||
		d.MaxBatchBytes < 65536 || d.MaxBatchBytes > MaxBodyBytes ||
		d.FlushIntervalMs < 100 || d.FlushIntervalMs > 60000 ||
		d.RequestTimeoutMs < 1000 || d.RequestTimeoutMs > 120000 ||
		d.InitialBackoffMs < 100 || d.InitialBackoffMs > 60000 ||
		d.MaxBackoffMs < d.InitialBackoffMs || d.MaxBackoffMs > 900000 {
		return invalid()
	}
	if s.Metadata.IntervalMs < 60000 || s.Metadata.IntervalMs > 86400000 {
		return invalid()
	}
	canonical := []MetadataCategory{CategoryAuthFiles, CategoryAPIKeys, CategoryProviderIdentities}
	if s.Metadata.Enabled {
		if len(s.Metadata.Categories) == 0 || len(s.Metadata.Categories) > len(canonical) {
			return invalid()
		}
		for i, category := range s.Metadata.Categories {
			if MetadataCategory(category) != canonical[i] {
				return invalid()
			}
		}
	}
	return nil
}

// requireSettingsPutKeys verifies that every required field at every nesting
// level of the complete PUT body is present, catching omitted struct fields
// that Go zero-value unmarshaling would silently accept.
func requireSettingsPutKeys(data []byte) *Error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return protocolError("invalid_json")
	}
	if perr := requireObjKeys(root, "protocolVersion", "settings"); perr != nil {
		return perr
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(root["settings"], &settings); err != nil {
		return protocolError("invalid_field")
	}
	if perr := requireObjKeys(settings, "enabled", "mode", "keeper", "outbox", "delivery", "metadata", "privacy"); perr != nil {
		return perr
	}
	var keeper map[string]json.RawMessage
	if err := json.Unmarshal(settings["keeper"], &keeper); err != nil {
		return protocolError("invalid_field")
	}
	if perr := requireObjKeys(keeper, "url", "tokenEnv", "caFile", "clientCertFile", "clientKeyFile"); perr != nil {
		return perr
	}
	var outbox map[string]json.RawMessage
	if err := json.Unmarshal(settings["outbox"], &outbox); err != nil {
		return protocolError("invalid_field")
	}
	if perr := requireObjKeys(outbox, "path", "maxBytes"); perr != nil {
		return perr
	}
	var delivery map[string]json.RawMessage
	if err := json.Unmarshal(settings["delivery"], &delivery); err != nil {
		return protocolError("invalid_field")
	}
	if perr := requireObjKeys(delivery, "maxBatchEvents", "maxBatchBytes", "flushIntervalMs", "requestTimeoutMs", "initialBackoffMs", "maxBackoffMs"); perr != nil {
		return perr
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(settings["metadata"], &metadata); err != nil {
		return protocolError("invalid_field")
	}
	if perr := requireObjKeys(metadata, "enabled", "intervalMs", "categories"); perr != nil {
		return perr
	}
	var privacy map[string]json.RawMessage
	if err := json.Unmarshal(settings["privacy"], &privacy); err != nil {
		return protocolError("invalid_field")
	}
	return requireObjKeys(privacy, "includeClientIp", "includeForwardedFor", "includeUserAgent")
}

func requireObjKeys(obj map[string]json.RawMessage, keys ...string) *Error {
	for _, key := range keys {
		raw, ok := obj[key]
		if !ok {
			return protocolError("invalid_field")
		}
		if string(raw) == "null" && key != "caFile" && key != "clientCertFile" && key != "clientKeyFile" {
			return protocolError("invalid_field")
		}
	}
	return nil
}
