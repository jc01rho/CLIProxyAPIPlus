package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/gin-gonic/gin"
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
