package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestSaveTokenRecord_AntigravityPrimaryHandoff_FirstCredential(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	cfg := &config.Config{
		AuthDir:                   tmpDir,
		AntigravityPrimaryHandoff: true,
	}
	store := &memoryAuthStore{items: make(map[string]*coreauth.Auth)}
	manager := coreauth.NewManager(store, nil, nil)
	h := NewHandlerWithoutConfigFilePath(cfg, manager)

	record := &coreauth.Auth{
		ID:       "antigravity-test-1",
		Provider: "antigravity",
		FileName: "antigravity-test-1.json",
		Label:    "test-1",
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "test-token-1",
		},
	}

	_, err := h.saveTokenRecord(ctx, record)
	if err != nil {
		t.Fatalf("saveTokenRecord failed: %v", err)
	}

	if record.PrimaryInfo == nil {
		t.Fatal("expected PrimaryInfo to be set, got nil")
	}
	if !record.PrimaryInfo.IsPrimary {
		t.Error("expected IsPrimary=true for first credential, got false")
	}
	if record.PrimaryInfo.Order != 1 {
		t.Errorf("expected Order=1, got %d", record.PrimaryInfo.Order)
	}
	if record.Disabled {
		t.Error("expected Disabled=false for first credential, got true")
	}
	if record.Status != coreauth.StatusActive {
		t.Errorf("expected Status=StatusActive, got %v", record.Status)
	}
}

func TestSaveTokenRecord_AntigravityPrimaryHandoff_SecondCredential(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	cfg := &config.Config{
		AuthDir:                   tmpDir,
		AntigravityPrimaryHandoff: true,
	}
	store := &memoryAuthStore{items: make(map[string]*coreauth.Auth)}
	manager := coreauth.NewManager(store, nil, nil)
	h := NewHandlerWithoutConfigFilePath(cfg, manager)

	first := &coreauth.Auth{
		ID:       "antigravity-test-1",
		Provider: "antigravity",
		FileName: "antigravity-test-1.json",
		Label:    "test-1",
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "test-token-1",
		},
	}
	_, _ = h.saveTokenRecord(ctx, first)

	second := &coreauth.Auth{
		ID:       "antigravity-test-2",
		Provider: "antigravity",
		FileName: "antigravity-test-2.json",
		Label:    "test-2",
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "test-token-2",
		},
	}

	_, err := h.saveTokenRecord(ctx, second)
	if err != nil {
		t.Fatalf("saveTokenRecord failed: %v", err)
	}

	if second.PrimaryInfo == nil {
		t.Fatal("expected PrimaryInfo to be set, got nil")
	}
	if second.PrimaryInfo.IsPrimary {
		t.Error("expected IsPrimary=false for second credential, got true")
	}
	if second.PrimaryInfo.Order != 2 {
		t.Errorf("expected Order=2, got %d", second.PrimaryInfo.Order)
	}
	if !second.Disabled {
		t.Error("expected Disabled=true for second credential, got false")
	}
	if second.Status != coreauth.StatusDisabled {
		t.Errorf("expected Status=StatusDisabled, got %v", second.Status)
	}
	secondPath := filepath.Join(tmpDir, "antigravity-test-2.json")
	if _, err := os.Stat(secondPath); err != nil {
		t.Fatalf("expected standby credential file to be persisted, got stat error: %v", err)
	}
	raw, err := os.ReadFile(secondPath)
	if err != nil {
		t.Fatalf("read standby credential file failed: %v", err)
	}
	var stored map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("unmarshal standby credential file failed: %v", err)
	}
	if disabled, ok := stored["disabled"].(bool); !ok || !disabled {
		t.Fatalf("expected persisted standby credential to be disabled, got %#v", stored["disabled"])
	}
	primaryInfo, ok := stored["primary_info"].(map[string]any)
	if !ok {
		t.Fatal("expected persisted standby credential to include primary_info")
	}
	if isPrimary, ok := primaryInfo["is_primary"].(bool); !ok || isPrimary {
		t.Fatalf("expected persisted standby credential to be non-primary, got %#v", primaryInfo["is_primary"])
	}
}

func TestSaveTokenRecord_NilHandler(t *testing.T) {
	ctx := context.Background()
	var h *Handler = nil

	record := &coreauth.Auth{
		ID:       "antigravity-test",
		Provider: "antigravity",
	}

	_, err := h.saveTokenRecord(ctx, record)
	if err == nil {
		t.Error("expected error for nil handler, got nil")
	}
}

func TestSaveTokenRecord_NilConfig(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	h := &Handler{
		cfg:         nil,
		authManager: coreauth.NewManager(&memoryAuthStore{items: make(map[string]*coreauth.Auth)}, nil, nil),
		tokenStore:  &memoryAuthStore{items: make(map[string]*coreauth.Auth)},
	}

	record := &coreauth.Auth{
		ID:       "antigravity-test",
		Provider: "antigravity",
		FileName: filepath.Join(tmpDir, "test.json"),
	}

	_, err := h.saveTokenRecord(ctx, record)
	if err != nil {
		t.Fatalf("saveTokenRecord should not panic with nil cfg, got: %v", err)
	}

	if record.PrimaryInfo != nil {
		t.Error("expected PrimaryInfo to remain nil when cfg is nil")
	}
}

func TestSaveTokenRecord_NonAntigravityProvider(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	cfg := &config.Config{
		AuthDir:                   tmpDir,
		AntigravityPrimaryHandoff: true,
	}
	store := &memoryAuthStore{items: make(map[string]*coreauth.Auth)}
	manager := coreauth.NewManager(store, nil, nil)
	h := NewHandlerWithoutConfigFilePath(cfg, manager)

	record := &coreauth.Auth{
		ID:       "claude-test-1",
		Provider: "claude",
		FileName: "claude-test-1.json",
		Label:    "test-claude",
		Metadata: map[string]any{
			"type":    "claude",
			"api_key": "test-key",
		},
	}

	_, err := h.saveTokenRecord(ctx, record)
	if err != nil {
		t.Fatalf("saveTokenRecord failed: %v", err)
	}

	if record.PrimaryInfo != nil {
		t.Error("expected PrimaryInfo to remain nil for non-antigravity provider")
	}
}

func TestInitAntigravityPrimaryInfo_NilSafety(t *testing.T) {
	ctx := context.Background()

	var h *Handler = nil
	record := &coreauth.Auth{Provider: "antigravity"}
	h.initAntigravityPrimaryInfo(ctx, record)
	if record.PrimaryInfo != nil {
		t.Error("nil handler should not modify record")
	}

	h = &Handler{cfg: nil}
	record2 := &coreauth.Auth{Provider: "antigravity"}
	h.initAntigravityPrimaryInfo(ctx, record2)
	if record2.PrimaryInfo != nil {
		t.Error("nil cfg should not modify record")
	}

	h = &Handler{cfg: &config.Config{AntigravityPrimaryHandoff: true}}
	var nilRecord *coreauth.Auth = nil
	h.initAntigravityPrimaryInfo(ctx, nilRecord)

	h.initAntigravityPrimaryInfo(ctx, &coreauth.Auth{Provider: "claude"})
}

func TestInitAntigravityPrimaryInfo_DefaultOnBehavior_NoConfig(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Config without AntigravityPrimaryHandoff (defaults to false)
	cfg := &config.Config{
		AuthDir: tmpDir,
		// AntigravityPrimaryHandoff: NOT SET → defaults to false
	}
	store := &memoryAuthStore{items: make(map[string]*coreauth.Auth)}
	manager := coreauth.NewManager(store, nil, nil)
	h := NewHandlerWithoutConfigFilePath(cfg, manager)

	first := &coreauth.Auth{
		ID:       "antigravity-default-1",
		Provider: "antigravity",
		FileName: "antigravity-default-1.json",
		Label:    "test-1",
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "test-token-1",
		},
	}

	h.initAntigravityPrimaryInfo(ctx, first)

	// Should be primary EVEN WITHOUT config being set explicitly
	if first.PrimaryInfo == nil {
		t.Fatal("expected PrimaryInfo to be set even without explicit config, got nil")
	}
	if !first.PrimaryInfo.IsPrimary {
		t.Error("expected IsPrimary=true for first credential even without config, got false")
	}
	if first.Disabled {
		t.Error("expected Disabled=false for first credential, got true")
	}
	if first.Status != coreauth.StatusActive {
		t.Errorf("expected Status=StatusActive, got %v", first.Status)
	}
}

func TestInitAntigravityPrimaryInfo_DefaultOnBehavior_MultipleCredentials(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Config without AntigravityPrimaryHandoff (defaults to false)
	cfg := &config.Config{
		AuthDir: tmpDir,
		// AntigravityPrimaryHandoff: NOT SET → defaults to false
	}
	store := &memoryAuthStore{items: make(map[string]*coreauth.Auth)}
	manager := coreauth.NewManager(store, nil, nil)
	h := NewHandlerWithoutConfigFilePath(cfg, manager)

	first := &coreauth.Auth{
		ID:       "antigravity-default-1",
		Provider: "antigravity",
		FileName: "antigravity-default-1.json",
		Label:    "test-1",
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "test-token-1",
		},
	}
	_, _ = h.saveTokenRecord(ctx, first)

	second := &coreauth.Auth{
		ID:       "antigravity-default-2",
		Provider: "antigravity",
		FileName: "antigravity-default-2.json",
		Label:    "test-2",
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "test-token-2",
		},
	}

	_, _ = h.saveTokenRecord(ctx, second)

	// Second should be non-primary EVEN WITHOUT explicit config
	if second.PrimaryInfo == nil {
		t.Fatal("expected PrimaryInfo to be set even without explicit config, got nil")
	}
	if second.PrimaryInfo.IsPrimary {
		t.Error("expected IsPrimary=false for second credential, got true")
	}
	if !second.Disabled {
		t.Error("expected Disabled=true for second credential, got false")
	}
	if second.Status != coreauth.StatusDisabled {
		t.Errorf("expected Status=StatusDisabled, got %v", second.Status)
	}
}

func TestEnsureSoleAntigravityPrimary_DemotesPreviousPrimary(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	cfg := &config.Config{
		AuthDir:                   tmpDir,
		AntigravityPrimaryHandoff: true,
	}
	store := &memoryAuthStore{items: make(map[string]*coreauth.Auth)}
	manager := coreauth.NewManager(store, nil, nil)
	h := NewHandlerWithoutConfigFilePath(cfg, manager)

	primary := &coreauth.Auth{
		ID:       "antigravity-primary",
		Provider: "antigravity",
		FileName: "antigravity-primary.json",
		Label:    "primary",
		Status:   coreauth.StatusActive,
		Disabled: false,
		PrimaryInfo: &coreauth.PrimaryInfo{
			IsPrimary: true,
			Order:     1,
		},
	}
	secondary := &coreauth.Auth{
		ID:       "antigravity-secondary",
		Provider: "antigravity",
		FileName: "antigravity-secondary.json",
		Label:    "secondary",
		Status:   coreauth.StatusDisabled,
		Disabled: true,
		PrimaryInfo: &coreauth.PrimaryInfo{
			IsPrimary: false,
			Order:     2,
		},
	}

	manager.Register(ctx, primary)
	manager.Register(ctx, secondary)

	h.ensureSoleAntigravityPrimary(ctx, secondary)

	updatedPrimary, _ := manager.GetByID("antigravity-primary")
	updatedSecondary, _ := manager.GetByID("antigravity-secondary")

	if updatedSecondary.Disabled {
		t.Error("expected secondary to be enabled after ensureSoleAntigravityPrimary")
	}
	if !updatedSecondary.PrimaryInfo.IsPrimary {
		t.Error("expected secondary to be promoted to primary")
	}
	if !updatedPrimary.Disabled {
		t.Error("expected primary to be demoted after secondary promotion")
	}
	if updatedPrimary.PrimaryInfo.IsPrimary {
		t.Error("expected primary to no longer be primary after demotion")
	}
}

func TestEnsureSoleAntigravityPrimary_DemotesLegacyActivePrimary(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	cfg := &config.Config{
		AuthDir:                   tmpDir,
		AntigravityPrimaryHandoff: true,
	}
	store := &memoryAuthStore{items: make(map[string]*coreauth.Auth)}
	manager := coreauth.NewManager(store, nil, nil)
	h := NewHandlerWithoutConfigFilePath(cfg, manager)

	legacyPrimary := &coreauth.Auth{
		ID:       "antigravity-legacy-primary",
		Provider: "antigravity",
		FileName: "antigravity-legacy-primary.json",
		Label:    "legacy-primary",
		Status:   coreauth.StatusActive,
		Disabled: false,
	}
	standby := &coreauth.Auth{
		ID:       "antigravity-secondary",
		Provider: "antigravity",
		FileName: "antigravity-secondary.json",
		Label:    "secondary",
		Status:   coreauth.StatusDisabled,
		Disabled: true,
		PrimaryInfo: &coreauth.PrimaryInfo{
			IsPrimary: false,
			Order:     2,
		},
	}

	if _, err := manager.Register(ctx, legacyPrimary); err != nil {
		t.Fatalf("register legacy primary failed: %v", err)
	}
	if _, err := manager.Register(ctx, standby); err != nil {
		t.Fatalf("register standby failed: %v", err)
	}

	h.ensureSoleAntigravityPrimary(ctx, standby)

	updatedLegacyPrimary, _ := manager.GetByID("antigravity-legacy-primary")
	updatedStandby, _ := manager.GetByID("antigravity-secondary")

	if !updatedLegacyPrimary.Disabled {
		t.Error("expected legacy active primary to be demoted and disabled")
	}
	if updatedLegacyPrimary.PrimaryInfo == nil {
		t.Fatal("expected demoted legacy primary to receive PrimaryInfo")
	}
	if updatedLegacyPrimary.PrimaryInfo.IsPrimary {
		t.Error("expected demoted legacy primary to no longer be primary")
	}
	if updatedStandby.Disabled {
		t.Error("expected standby to be enabled after promotion")
	}
	if updatedStandby.PrimaryInfo == nil || !updatedStandby.PrimaryInfo.IsPrimary {
		t.Error("expected standby to be promoted to primary")
	}
}

func TestSaveTokenRecord_AntigravityPrimaryHandoff_LegacyActivePrimaryStaysUnique(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	cfg := &config.Config{
		AuthDir:                   tmpDir,
		AntigravityPrimaryHandoff: true,
	}
	store := &memoryAuthStore{items: make(map[string]*coreauth.Auth)}
	manager := coreauth.NewManager(store, nil, nil)
	h := NewHandlerWithoutConfigFilePath(cfg, manager)

	legacyPrimary := &coreauth.Auth{
		ID:       "antigravity-legacy-primary",
		Provider: "antigravity",
		FileName: "antigravity-legacy-primary.json",
		Label:    "legacy-primary",
		Status:   coreauth.StatusActive,
		Disabled: false,
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "legacy-token",
		},
	}
	standby := &coreauth.Auth{
		ID:       "antigravity-standby",
		Provider: "antigravity",
		FileName: "antigravity-standby.json",
		Label:    "standby",
		Status:   coreauth.StatusDisabled,
		Disabled: true,
		PrimaryInfo: &coreauth.PrimaryInfo{
			IsPrimary: false,
			Order:     2,
		},
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "standby-token",
		},
	}

	if _, err := manager.Register(ctx, legacyPrimary); err != nil {
		t.Fatalf("register legacy primary failed: %v", err)
	}
	if _, err := manager.Register(ctx, standby); err != nil {
		t.Fatalf("register standby failed: %v", err)
	}

	newOAuth := &coreauth.Auth{
		ID:       "antigravity-new-oauth",
		Provider: "antigravity",
		FileName: "antigravity-new-oauth.json",
		Label:    "new-oauth",
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "new-token",
		},
	}

	_, err := h.saveTokenRecord(ctx, newOAuth)
	if err != nil {
		t.Fatalf("saveTokenRecord failed: %v", err)
	}

	if newOAuth.PrimaryInfo == nil {
		t.Fatal("expected new oauth credential to get PrimaryInfo")
	}
	if newOAuth.PrimaryInfo.IsPrimary {
		t.Fatal("expected new oauth credential to remain standby when a legacy active primary exists")
	}
	if !newOAuth.Disabled {
		t.Fatal("expected new oauth credential to be disabled as standby")
	}

	updatedLegacyPrimary, ok := manager.GetByID("antigravity-legacy-primary")
	if !ok {
		t.Fatal("expected legacy primary to remain registered")
	}
	if updatedLegacyPrimary.Disabled {
		t.Fatal("expected legacy primary to remain active")
	}
	if updatedLegacyPrimary.PrimaryInfo != nil && !updatedLegacyPrimary.PrimaryInfo.IsPrimary {
		t.Fatal("expected legacy primary not to be demoted")
	}
}

func TestListAuthFiles_BackfillsAntigravityPrimaryInfoForLegacyRecords(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	tmpDir := t.TempDir()
	cfg := &config.Config{AuthDir: tmpDir}
	store := &memoryAuthStore{items: make(map[string]*coreauth.Auth)}
	manager := coreauth.NewManager(store, nil, nil)
	h := NewHandlerWithoutConfigFilePath(cfg, manager)

	primary := &coreauth.Auth{
		ID:       "legacy-antigravity-primary",
		Provider: "antigravity",
		FileName: "legacy-antigravity-primary.json",
		Label:    "legacy-primary",
		Status:   coreauth.StatusActive,
		Disabled: false,
		Attributes: map[string]string{
			"path": filepath.Join(tmpDir, "legacy-antigravity-primary.json"),
		},
	}
	secondary := &coreauth.Auth{
		ID:       "legacy-antigravity-standby",
		Provider: "antigravity",
		FileName: "legacy-antigravity-standby.json",
		Label:    "legacy-standby",
		Status:   coreauth.StatusDisabled,
		Disabled: true,
		Attributes: map[string]string{
			"path": filepath.Join(tmpDir, "legacy-antigravity-standby.json"),
		},
	}

	if _, err := manager.Register(ctx, primary); err != nil {
		t.Fatalf("register primary failed: %v", err)
	}
	if _, err := manager.Register(ctx, secondary); err != nil {
		t.Fatalf("register secondary failed: %v", err)
	}
	if err := os.WriteFile(primary.Attributes["path"], []byte(`{"type":"antigravity"}`), 0o644); err != nil {
		t.Fatalf("write primary file failed: %v", err)
	}
	if err := os.WriteFile(secondary.Attributes["path"], []byte(`{"type":"antigravity","disabled":true}`), 0o644); err != nil {
		t.Fatalf("write secondary file failed: %v", err)
	}

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)

	h.ListAuthFiles(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Files []struct {
			Name        string `json:"name"`
			Disabled    bool   `json:"disabled"`
			PrimaryInfo *struct {
				IsPrimary bool `json:"is_primary"`
				Order     int  `json:"order"`
			} `json:"primary_info"`
		} `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}
	if len(payload.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(payload.Files))
	}

	var primaryInfoFound bool
	var primaryIsPrimary bool
	var secondaryInfoFound bool
	var secondaryIsPrimary bool
	for _, file := range payload.Files {
		switch file.Name {
		case "legacy-antigravity-primary.json":
			primaryInfoFound = file.PrimaryInfo != nil
			if file.PrimaryInfo != nil {
				primaryIsPrimary = file.PrimaryInfo.IsPrimary
			}
		case "legacy-antigravity-standby.json":
			secondaryInfoFound = file.PrimaryInfo != nil
			if file.PrimaryInfo != nil {
				secondaryIsPrimary = file.PrimaryInfo.IsPrimary
			}
		}
	}

	if !primaryInfoFound {
		t.Fatal("expected primary_info for legacy primary entry")
	}
	if !primaryIsPrimary {
		t.Fatal("expected legacy active antigravity entry to be backfilled as primary")
	}

	if !secondaryInfoFound {
		t.Fatal("expected primary_info for legacy standby entry")
	}
	if secondaryIsPrimary {
		t.Fatal("expected disabled legacy antigravity entry to remain standby")
	}
}

func TestListAuthFiles_ExplicitPrimaryPreventsDuplicateFallbackPrimary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	tmpDir := t.TempDir()
	cfg := &config.Config{AuthDir: tmpDir}
	store := &memoryAuthStore{items: make(map[string]*coreauth.Auth)}
	manager := coreauth.NewManager(store, nil, nil)
	h := NewHandlerWithoutConfigFilePath(cfg, manager)

	explicitPrimary := &coreauth.Auth{
		ID:       "antigravity-explicit-primary",
		Provider: "antigravity",
		FileName: "antigravity-explicit-primary.json",
		Label:    "explicit-primary",
		Status:   coreauth.StatusActive,
		Disabled: false,
		PrimaryInfo: &coreauth.PrimaryInfo{
			IsPrimary: true,
			Order:     1,
		},
		Attributes: map[string]string{
			"path": filepath.Join(tmpDir, "antigravity-explicit-primary.json"),
		},
	}
	legacyActive := &coreauth.Auth{
		ID:       "antigravity-legacy-active",
		Provider: "antigravity",
		FileName: "antigravity-legacy-active.json",
		Label:    "legacy-active",
		Status:   coreauth.StatusActive,
		Disabled: false,
		Attributes: map[string]string{
			"path": filepath.Join(tmpDir, "antigravity-legacy-active.json"),
		},
	}

	if _, err := manager.Register(ctx, explicitPrimary); err != nil {
		t.Fatalf("register explicit primary failed: %v", err)
	}
	if _, err := manager.Register(ctx, legacyActive); err != nil {
		t.Fatalf("register legacy active failed: %v", err)
	}
	if err := os.WriteFile(explicitPrimary.Attributes["path"], []byte(`{"type":"antigravity","primary_info":{"is_primary":true,"order":1}}`), 0o644); err != nil {
		t.Fatalf("write explicit primary file failed: %v", err)
	}
	if err := os.WriteFile(legacyActive.Attributes["path"], []byte(`{"type":"antigravity"}`), 0o644); err != nil {
		t.Fatalf("write legacy active file failed: %v", err)
	}

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)

	h.ListAuthFiles(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Files []struct {
			Name        string `json:"name"`
			PrimaryInfo *struct {
				IsPrimary bool `json:"is_primary"`
				Order     int  `json:"order"`
			} `json:"primary_info"`
		} `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}

	primaryCount := 0
	legacyWasFallbackPrimary := false
	for _, file := range payload.Files {
		if file.PrimaryInfo != nil && file.PrimaryInfo.IsPrimary {
			primaryCount++
			if file.Name == "antigravity-legacy-active.json" {
				legacyWasFallbackPrimary = true
			}
		}
	}

	if primaryCount != 1 {
		t.Fatalf("expected exactly one primary entry, got %d", primaryCount)
	}
	if legacyWasFallbackPrimary {
		t.Fatal("expected legacy active entry not to become fallback primary when explicit primary exists")
	}
}

func TestListAuthFiles_ReconcilesStaleStandbyAsSoleEnabledPrimary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	tmpDir := t.TempDir()
	cfg := &config.Config{AuthDir: tmpDir}
	store := &memoryAuthStore{items: make(map[string]*coreauth.Auth)}
	manager := coreauth.NewManager(store, nil, nil)
	h := NewHandlerWithoutConfigFilePath(cfg, manager)

	soleEnabled := &coreauth.Auth{
		ID:       "antigravity-sole-enabled",
		Provider: "antigravity",
		FileName: "antigravity-sole-enabled.json",
		Label:    "sole-enabled",
		Status:   coreauth.StatusActive,
		Disabled: false,
		PrimaryInfo: &coreauth.PrimaryInfo{
			IsPrimary: false,
			Order:     2,
		},
		Attributes: map[string]string{
			"path": filepath.Join(tmpDir, "antigravity-sole-enabled.json"),
		},
	}
	standbyDisabled := &coreauth.Auth{
		ID:       "antigravity-disabled-standby",
		Provider: "antigravity",
		FileName: "antigravity-disabled-standby.json",
		Label:    "disabled-standby",
		Status:   coreauth.StatusDisabled,
		Disabled: true,
		PrimaryInfo: &coreauth.PrimaryInfo{
			IsPrimary: false,
			Order:     3,
		},
		Attributes: map[string]string{
			"path": filepath.Join(tmpDir, "antigravity-disabled-standby.json"),
		},
	}

	if _, err := manager.Register(ctx, soleEnabled); err != nil {
		t.Fatalf("register sole enabled failed: %v", err)
	}
	if _, err := manager.Register(ctx, standbyDisabled); err != nil {
		t.Fatalf("register disabled standby failed: %v", err)
	}
	if err := os.WriteFile(soleEnabled.Attributes["path"], []byte(`{"type":"antigravity","primary_info":{"is_primary":false,"order":2}}`), 0o644); err != nil {
		t.Fatalf("write sole enabled file failed: %v", err)
	}
	if err := os.WriteFile(standbyDisabled.Attributes["path"], []byte(`{"type":"antigravity","disabled":true,"primary_info":{"is_primary":false,"order":3}}`), 0o644); err != nil {
		t.Fatalf("write disabled standby file failed: %v", err)
	}

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)

	h.ListAuthFiles(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Files []struct {
			Name        string `json:"name"`
			Disabled    bool   `json:"disabled"`
			PrimaryInfo *struct {
				IsPrimary bool `json:"is_primary"`
				Order     int  `json:"order"`
			} `json:"primary_info"`
		} `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}

	primaryCount := 0
	soleEnabledIsPrimary := false
	disabledStandbyIsPrimary := false
	for _, file := range payload.Files {
		if file.PrimaryInfo != nil && file.PrimaryInfo.IsPrimary {
			primaryCount++
		}
		switch file.Name {
		case "antigravity-sole-enabled.json":
			soleEnabledIsPrimary = file.PrimaryInfo != nil && file.PrimaryInfo.IsPrimary
		case "antigravity-disabled-standby.json":
			disabledStandbyIsPrimary = file.PrimaryInfo != nil && file.PrimaryInfo.IsPrimary
		}
	}

	if primaryCount != 1 {
		t.Fatalf("expected exactly one reconciled primary entry, got %d", primaryCount)
	}
	if !soleEnabledIsPrimary {
		t.Fatal("expected sole enabled antigravity credential to be reconciled as primary")
	}
	if disabledStandbyIsPrimary {
		t.Fatal("expected disabled standby credential to remain non-primary")
	}
	if soleEnabled.PrimaryInfo == nil || soleEnabled.PrimaryInfo.IsPrimary {
		t.Fatal("expected in-memory auth state to remain unchanged by list-time reconciliation")
	}
}

func TestListAuthFilesFromDisk_ReconcilesStaleStandbyAsSoleEnabledPrimary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmpDir := t.TempDir()
	h := &Handler{cfg: &config.Config{AuthDir: tmpDir}}

	soleEnabledPath := filepath.Join(tmpDir, "antigravity-disk-sole-enabled.json")
	disabledStandbyPath := filepath.Join(tmpDir, "antigravity-disk-disabled-standby.json")
	if err := os.WriteFile(soleEnabledPath, []byte(`{"type":"antigravity","email":"sole@example.com","primary_info":{"is_primary":false,"order":2}}`), 0o644); err != nil {
		t.Fatalf("write sole enabled disk file failed: %v", err)
	}
	if err := os.WriteFile(disabledStandbyPath, []byte(`{"type":"antigravity","disabled":true,"primary_info":{"is_primary":false,"order":3}}`), 0o644); err != nil {
		t.Fatalf("write disabled standby disk file failed: %v", err)
	}

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)

	h.listAuthFilesFromDisk(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Files []struct {
			Name        string `json:"name"`
			PrimaryInfo *struct {
				IsPrimary bool `json:"is_primary"`
				Order     int  `json:"order"`
			} `json:"primary_info"`
		} `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal disk response failed: %v", err)
	}

	primaryCount := 0
	soleEnabledIsPrimary := false
	for _, file := range payload.Files {
		if file.PrimaryInfo != nil && file.PrimaryInfo.IsPrimary {
			primaryCount++
		}
		if file.Name == filepath.Base(soleEnabledPath) {
			soleEnabledIsPrimary = file.PrimaryInfo != nil && file.PrimaryInfo.IsPrimary
		}
	}

	if primaryCount != 1 {
		t.Fatalf("expected exactly one reconciled primary entry from disk fallback, got %d", primaryCount)
	}
	if !soleEnabledIsPrimary {
		t.Fatal("expected disk fallback sole enabled credential to be reconciled as primary")
	}
}

func TestWriteAuthFile_PersistsCanonicalPrimaryInfoForSoleEnabledAntigravity(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	cfg := &config.Config{AuthDir: tmpDir}
	store := &memoryAuthStore{items: make(map[string]*coreauth.Auth)}
	manager := coreauth.NewManager(store, nil, nil)
	h := NewHandlerWithoutConfigFilePath(cfg, manager)

	standbyPath := filepath.Join(tmpDir, "existing-standby.json")
	standby := &coreauth.Auth{
		ID:       "existing-standby.json",
		Provider: "antigravity",
		FileName: "existing-standby.json",
		Label:    "existing-standby",
		Status:   coreauth.StatusDisabled,
		Disabled: true,
		PrimaryInfo: &coreauth.PrimaryInfo{
			IsPrimary: false,
			Order:     2,
		},
		Attributes: map[string]string{"path": standbyPath},
		Metadata: map[string]any{
			"type":         "antigravity",
			"disabled":     true,
			"access_token": "standby-token",
			"primary_info": map[string]any{"is_primary": false, "order": 2},
		},
	}
	if _, err := manager.Register(ctx, standby); err != nil {
		t.Fatalf("register standby failed: %v", err)
	}
	if err := os.WriteFile(standbyPath, []byte(`{"type":"antigravity","disabled":true,"access_token":"standby-token","primary_info":{"is_primary":false,"order":2}}`), 0o644); err != nil {
		t.Fatalf("write standby file failed: %v", err)
	}

	if err := h.writeAuthFile(ctx, "uploaded-antigravity.json", []byte(`{"type":"antigravity","access_token":"uploaded-token","primary_info":{"is_primary":false,"order":9}}`)); err != nil {
		t.Fatalf("writeAuthFile failed: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(tmpDir, "uploaded-antigravity.json"))
	if err != nil {
		t.Fatalf("read uploaded file failed: %v", err)
	}
	var stored map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("unmarshal uploaded file failed: %v", err)
	}
	primaryInfo, ok := stored["primary_info"].(map[string]any)
	if !ok {
		t.Fatal("expected uploaded file to persist primary_info")
	}
	if isPrimary, _ := primaryInfo["is_primary"].(bool); !isPrimary {
		t.Fatal("expected uploaded sole enabled antigravity file to be persisted as primary")
	}
	if disabled, _ := stored["disabled"].(bool); disabled {
		t.Fatal("expected uploaded sole enabled antigravity file to be enabled")
	}

	updatedStandby, ok := manager.GetByID("existing-standby.json")
	if !ok || updatedStandby == nil {
		t.Fatal("expected existing standby auth to remain registered")
	}
	if updatedStandby.PrimaryInfo == nil || updatedStandby.PrimaryInfo.IsPrimary {
		t.Fatal("expected existing standby auth to remain non-primary")
	}
}

func TestSaveTokenRecord_PersistsCanonicalPrimaryInfoMetadata(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		AuthDir:                   tmpDir,
		AntigravityPrimaryHandoff: true,
	}
	store := &memoryAuthStore{items: make(map[string]*coreauth.Auth)}
	manager := coreauth.NewManager(store, nil, nil)
	h := NewHandlerWithoutConfigFilePath(cfg, manager)

	record := &coreauth.Auth{
		ID:       "persist-primary.json",
		Provider: "antigravity",
		FileName: "persist-primary.json",
		Label:    "persist-primary",
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "persist-token",
			"primary_info": map[string]any{"is_primary": false, "order": 99},
		},
	}

	if _, err := h.saveTokenRecord(ctx, record); err != nil {
		t.Fatalf("saveTokenRecord failed: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(tmpDir, "persist-primary.json"))
	if err != nil {
		t.Fatalf("read persisted file failed: %v", err)
	}
	var stored map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("unmarshal persisted file failed: %v", err)
	}
	primaryInfo, ok := stored["primary_info"].(map[string]any)
	if !ok {
		t.Fatal("expected persisted file to include primary_info")
	}
	if isPrimary, _ := primaryInfo["is_primary"].(bool); !isPrimary {
		t.Fatal("expected persisted primary_info.is_primary=true after saveTokenRecord")
	}
}
