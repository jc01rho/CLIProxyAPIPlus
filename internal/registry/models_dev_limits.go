package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// ModelsDevLimitsURL is the model-only catalog (not the provider routing catalog).
const ModelsDevLimitsURL = "https://models.dev/models.json"
const maxModelsDevLimitsSize = 4 << 20

// ModelsDevLimit contains the provider-independent limits from models.dev.
type ModelsDevLimit struct {
	Context int `json:"context"`
	Input   int `json:"input"`
	Output  int `json:"output"`
}

type modelsDevLimitCatalog struct {
	exact     map[string]ModelsDevLimit
	byModelID map[string]ModelsDevLimit
}

var modelsDevLimits atomic.Pointer[modelsDevLimitCatalog]

// LookupModelsDevLimit returns a limit only when the model ID identifies one
// canonical model. A fully qualified models.dev ID is always unambiguous.
func LookupModelsDevLimit(id string) (ModelsDevLimit, bool) {
	catalog := modelsDevLimits.Load()
	if catalog == nil {
		return ModelsDevLimit{}, false
	}
	if limit, ok := catalog.exact[id]; ok {
		return limit, true
	}
	limit, ok := catalog.byModelID[id]
	return limit, ok
}

// RefreshModelsDevLimits fetches a fresh catalog on every server start. A
// failed fetch leaves the existing model metadata in place.
func RefreshModelsDevLimits(ctx context.Context, sourceURL string) error {
	return refreshModelsDevLimits(ctx, sourceURL, &http.Client{Timeout: 12 * time.Second})
}

func refreshModelsDevLimits(ctx context.Context, url string, client *http.Client) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("models.dev request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("models.dev fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("models.dev fetch: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxModelsDevLimitsSize+1))
	if err != nil {
		return fmt.Errorf("models.dev read: %w", err)
	}
	if len(data) > maxModelsDevLimitsSize {
		return fmt.Errorf("models.dev catalog exceeds %d bytes", maxModelsDevLimitsSize)
	}
	catalog, err := parseModelsDevLimits(data)
	if err != nil {
		return err
	}
	modelsDevLimits.Store(catalog)
	return nil
}

func parseModelsDevLimits(data []byte) (*modelsDevLimitCatalog, error) {
	var models map[string]struct {
		Limit ModelsDevLimit `json:"limit"`
	}
	if err := json.Unmarshal(data, &models); err != nil {
		return nil, fmt.Errorf("models.dev catalog: %w", err)
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("models.dev catalog is empty")
	}
	catalog := &modelsDevLimitCatalog{
		exact:     make(map[string]ModelsDevLimit),
		byModelID: make(map[string]ModelsDevLimit),
	}
	ambiguous := make(map[string]bool)
	for key, model := range models {
		_, id, ok := strings.Cut(key, "/")
		if !ok || id == "" || (model.Limit.Context <= 0 && model.Limit.Output <= 0) {
			continue
		}
		catalog.exact[key] = model.Limit
		if _, exists := catalog.byModelID[id]; exists {
			ambiguous[id] = true
		}
		catalog.byModelID[id] = model.Limit
	}
	for id := range ambiguous {
		delete(catalog.byModelID, id)
	}
	if len(catalog.exact) == 0 {
		return nil, fmt.Errorf("models.dev catalog contains no valid limits")
	}
	return catalog, nil
}

// ApplyModelsDevLimit overrides missing or differing model-list metadata.
// Callers must pass a clone, not a registered model used by routing.
func ApplyModelsDevLimit(info *ModelInfo) {
	if info == nil {
		return
	}
	metadataID := info.MetadataModelID
	if metadataID == "" {
		metadataID = info.ID
	}
	limit, ok := LookupModelsDevLimit(metadataID)
	if !ok {
		return
	}
	if limit.Context > 0 {
		info.ContextLength = limit.Context
		info.MaxContextLength = limit.Context
	}
	if limit.Input > 0 {
		info.InputTokenLimit = limit.Input
	} else if limit.Context > 0 {
		info.InputTokenLimit = limit.Context
	}
	if limit.Output > 0 {
		info.MaxCompletionTokens = limit.Output
	}
}
