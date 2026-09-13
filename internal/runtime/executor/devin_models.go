// Package executor provides provider executors. This file discovers the Devin
// model catalog at runtime.
package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	devinGetCliModelConfigsPath = "/exa.api_server_pb.ApiServerService/GetCliModelConfigs"

	devinModelsFetchTimeout = 15 * time.Second

	// Discovery announces the "chisel" dev-channel identity rather than the
	// released "devin-cli" one. The backend gates the native catalog on this
	// identity: with the released identity it answers 200 with a single CLI
	// default config instead of the full set.
	devinDiscoveryIDEName    = "chisel"
	devinDiscoveryIDEVersion = "0.0.0-dev"

	// Metadata field numbers.
	devinMetaIDENameField          = 1
	devinMetaExtensionVersionField = 2
	devinMetaIDEVersionField       = 7
	devinMetaExtensionNameField    = 12
	devinMetaDisplayOptionsField   = 30

	// ClientModelConfig field numbers.
	devinConfigLabelField     = 1
	devinConfigDisabledField  = 4
	devinConfigMaxTokensField = 18
	devinConfigModelUIDField  = 22
	devinConfigModelInfoField = 23
	devinConfigCostTierField  = 24

	// ModelInfo field numbers.
	devinModelInfoMaxTokensField       = 4
	devinModelInfoFeaturesField        = 6
	devinModelInfoMaxOutputTokensField = 13
	devinModelInfoDisplayOptionField   = 22
	devinModelInfoIsRouterField        = 25

	// ModelFeatures field numbers.
	devinFeaturesSupportsThinkingField = 15

	devinDefaultContextWindow = 200000
	devinDefaultMaxTokens     = 64000

	// devinCostTierFree is the ClientModelConfig.model_cost_tier value the
	// backend assigns to models it bills as free/unlimited (e.g. glm-5-2,
	// swe-2-high). Mirrors MODEL_COST_TIER_FREE in the published proto.
	devinCostTierFree = 4
)

// devinDiscoveryDisplayOptions are the display slots the native client
// advertises. Requesting the internal slots is what makes the server return its
// full catalog; the internal ones are filtered out again below, exactly as the
// native client does.
var devinDiscoveryDisplayOptions = []uint64{3, 4, 6, 7, 8}

// devinInternalDisplayOptions are slots the native client requests but never
// surfaces: quick-review and internal defaults.
var devinInternalDisplayOptions = map[uint64]bool{4: true, 6: true}

// FetchDevinModels discovers the Devin catalog through the GetCliModelConfigs
// unary Connect RPC. It returns nil when discovery fails so callers can fall
// back to the static seed.
func FetchDevinModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	if ctx == nil {
		ctx = context.Background()
	}
	token := devinAPIKey(auth)
	if strings.TrimSpace(token) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, devinModelsFetchTimeout)
	defer cancel()

	url := strings.TrimRight(devinServerURL(auth), "/") + devinGetCliModelConfigsPath
	body := devinBuildModelConfigsRequest(token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		log.WithError(err).Debug("devin: failed to build model discovery request")
		return nil
	}
	req.Header.Set("authorization", devinAuthHeader(token))
	req.Header.Set("content-type", "application/proto")
	req.Header.Set("connect-protocol-version", "1")
	req.Header.Set("accept", "*/*")

	httpClient := newProxyAwareHTTPClient(ctx, cfg, auth, 0)
	resp, err := httpClient.Do(req)
	if err != nil {
		log.WithError(err).Debug("devin: model discovery request failed")
		return nil
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("devin: failed to close model discovery response")
		}
	}()
	if resp.StatusCode != http.StatusOK {
		log.WithField("status", resp.StatusCode).Debug("devin: model discovery returned a non-200 status")
		return nil
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		log.WithError(err).Debug("devin: failed to read model discovery response")
		return nil
	}

	models := devinParseModelConfigs(raw)
	if len(models) == 0 {
		// An empty-but-200 response is the failure signature of a stale client
		// identity pin, not an empty account catalog. Report discovery failure
		// so the static seed survives.
		log.Warn("devin: model discovery returned an empty catalog; the pinned client identity may be stale")
		return nil
	}
	return models
}

// devinBuildModelConfigsRequest encodes a GetCliModelConfigsRequest carrying
// the discovery identity and the requested display slots.
func devinBuildModelConfigsRequest(token string) []byte {
	var meta []byte
	meta = append(meta, devinEncodeField(nil, devinMetaIDENameField, 2, devinEncodeString(devinDiscoveryIDEName))...)
	meta = append(meta, devinEncodeField(nil, devinMetaExtensionVersionField, 2, devinEncodeString(devinDiscoveryIDEVersion))...)
	meta = append(meta, devinEncodeField(nil, devinMetaAPIKeyField, 2, devinEncodeString(token))...)
	meta = append(meta, devinEncodeField(nil, devinMetaLocaleField, 2, devinEncodeString("en"))...)
	meta = append(meta, devinEncodeField(nil, devinMetaOSField, 2, devinEncodeString("linux"))...)
	meta = append(meta, devinEncodeField(nil, devinMetaIDEVersionField, 2, devinEncodeString(devinDiscoveryIDEVersion))...)
	meta = append(meta, devinEncodeField(nil, devinMetaExtensionNameField, 2, devinEncodeString(devinDiscoveryIDEName))...)
	for _, opt := range devinDiscoveryDisplayOptions {
		meta = append(meta, devinEncodeField(nil, devinMetaDisplayOptionsField, 0, devinEncodeVarint(nil, opt))...)
	}
	return devinEncodeSubMessage(devinReqMetadataField, meta)
}

// devinParseModelConfigs decodes the repeated ClientModelConfig entries and
// keeps the ones the native client would surface.
func devinParseModelConfigs(body []byte) []*registry.ModelInfo {
	var models []*registry.ModelInfo
	seen := make(map[string]bool)
	devinScanFields(body, func(num int, wire int, _ uint64, data []byte) bool {
		if num != 1 || wire != 2 {
			return true
		}
		model := devinParseModelConfig(data)
		if model == nil || seen[model.ID] {
			return true
		}
		seen[model.ID] = true
		models = append(models, model)
		return true
	})
	return models
}

// devinParseModelConfig turns one ClientModelConfig into a registry entry, or
// nil when the config is disabled, internal, or carries no usable uid.
func devinParseModelConfig(buf []byte) *registry.ModelInfo {
	var (
		label     string
		uid       string
		disabled  bool
		configMax uint64
		costTier  uint64
		infoBuf   []byte
	)
	devinScanFields(buf, func(num int, wire int, v uint64, data []byte) bool {
		switch {
		case num == devinConfigLabelField && wire == 2:
			label = string(data)
		case num == devinConfigModelUIDField && wire == 2:
			uid = string(data)
		case num == devinConfigDisabledField && wire == 0:
			disabled = v != 0
		case num == devinConfigMaxTokensField && wire == 0:
			configMax = v
		case num == devinConfigCostTierField && wire == 0:
			costTier = v
		case num == devinConfigModelInfoField && wire == 2:
			infoBuf = data
		}
		return true
	})
	uid = strings.TrimSpace(uid)
	if uid == "" || disabled {
		return nil
	}

	var (
		contextWindow uint64
		maxOutput     uint64
		displayOption uint64
		thinking      bool
	)
	if len(infoBuf) > 0 {
		devinScanFields(infoBuf, func(num int, wire int, v uint64, data []byte) bool {
			switch {
			case num == devinModelInfoMaxTokensField && wire == 0:
				contextWindow = v
			case num == devinModelInfoMaxOutputTokensField && wire == 0:
				maxOutput = v
			case num == devinModelInfoDisplayOptionField && wire == 0:
				displayOption = v
			case num == devinModelInfoFeaturesField && wire == 2:
				devinScanFields(data, func(fNum int, fWire int, fv uint64, _ []byte) bool {
					if fNum == devinFeaturesSupportsThinkingField && fWire == 0 {
						thinking = fv != 0
					}
					return true
				})
			}
			return true
		})
	}
	if devinInternalDisplayOptions[displayOption] {
		return nil
	}

	if contextWindow == 0 {
		contextWindow = devinDefaultContextWindow
	}
	if maxOutput == 0 {
		maxOutput = configMax
	}
	if maxOutput == 0 {
		maxOutput = devinDefaultMaxTokens
	}
	if label = strings.TrimSpace(label); label == "" {
		label = uid
	}

	model := &registry.ModelInfo{
		ID:                  uid,
		Object:              "model",
		OwnedBy:             "devin",
		Type:                "devin",
		DisplayName:         label,
		ContextLength:       int(contextWindow),
		MaxCompletionTokens: int(maxOutput),
		SupportedEndpoints:  []string{"/chat/completions"},
	}
	if costTier == devinCostTierFree {
		model.CostTier = "free"
		model.IsFree = true
	}
	if thinking {
		model.Thinking = &registry.ThinkingSupport{Max: 50000, DynamicAllowed: true}
	}
	return model
}
