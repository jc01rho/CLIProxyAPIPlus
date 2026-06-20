package auth

import (
	"context"
	"sort"
	"strings"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func quotaCooldownDisabledForAuthWithConfig(auth *Auth, cfg *internalconfig.Config) bool {
	if auth != nil {
		if override, ok := auth.DisableCoolingOverride(); ok {
			return override
		}
		if providerCoolingDisabledForAuth(auth, cfg) {
			return true
		}
	}
	if cfg != nil && cfg.DisableCooling {
		return true
	}
	return quotaCooldownDisabled.Load()
}

func providerCoolingDisabledForAuth(auth *Auth, cfg *internalconfig.Config) bool {
	if auth == nil || cfg == nil {
		return false
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	if provider == "" {
		return false
	}
	providerKey := ""
	compatName := ""
	if auth.Attributes != nil {
		providerKey = strings.TrimSpace(auth.Attributes["provider_key"])
		compatName = strings.TrimSpace(auth.Attributes["compat_name"])
	}
	if providerKey == "" && compatName == "" && provider != "openai-compatibility" {
		return false
	}
	if providerKey == "" {
		providerKey = provider
	}
	entry := resolveOpenAICompatConfig(cfg, providerKey, compatName, provider)
	return entry != nil && entry.DisableCooling
}

func (m *Manager) cooldownDisabledForAuth(auth *Auth) bool {
	if m == nil {
		return quotaCooldownDisabledForAuth(auth)
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	return quotaCooldownDisabledForAuthWithConfig(auth, cfg)
}

func (m *Manager) clearDisabledCooldownStates(cfg *internalconfig.Config) bool {
	if m == nil {
		return false
	}
	now := time.Now()
	snapshots := make([]*Auth, 0)
	m.mu.Lock()
	for _, auth := range m.auths {
		if auth == nil {
			continue
		}
		if !quotaCooldownDisabledForAuthWithConfig(auth, cfg) && !auth.Disabled && auth.Status != StatusDisabled {
			continue
		}
		if clearCooldownStateForAuth(auth, now) {
			snapshots = append(snapshots, auth.Clone())
		}
	}
	m.mu.Unlock()

	if m.scheduler != nil {
		for _, snapshot := range snapshots {
			m.scheduler.upsertAuth(snapshot)
		}
	}
	return len(snapshots) > 0
}

// RestoreCooldownStates restores unexpired persisted cooldown records into registered auths.
func (m *Manager) RestoreCooldownStates(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.RLock()
	store := m.cooldownStore
	m.mu.RUnlock()
	if store == nil {
		return nil
	}
	records, errLoad := store.Load(ctx)
	if errLoad != nil {
		return errLoad
	}
	if len(records) == 0 {
		return nil
	}

	now := time.Now()
	authLevelRecords := make([]CooldownStateRecord, 0)
	snapshotsByID := make(map[string]*Auth)

	m.mu.Lock()
	for _, record := range records {
		if strings.TrimSpace(record.Model) == "" {
			authLevelRecords = append(authLevelRecords, record)
			continue
		}
		if m.restoreCooldownRecordLocked(record, now) {
			if auth := m.auths[strings.TrimSpace(record.AuthID)]; auth != nil {
				snapshotsByID[auth.ID] = auth.Clone()
			}
		}
	}
	for _, record := range authLevelRecords {
		if m.restoreCooldownRecordLocked(record, now) {
			if auth := m.auths[strings.TrimSpace(record.AuthID)]; auth != nil {
				snapshotsByID[auth.ID] = auth.Clone()
			}
		}
	}
	m.mu.Unlock()

	if m.scheduler != nil {
		for _, snapshot := range snapshotsByID {
			m.scheduler.upsertAuth(snapshot)
		}
	}
	m.persistCooldownStates(ctx)
	return nil
}

func (m *Manager) restoreCooldownRecordLocked(record CooldownStateRecord, now time.Time) bool {
	authID := strings.TrimSpace(record.AuthID)
	if authID == "" || record.NextRetryAfter.IsZero() || !record.NextRetryAfter.After(now) {
		return false
	}
	auth := m.auths[authID]
	if auth == nil || auth.Disabled || auth.Status == StatusDisabled || m.cooldownDisabledForAuth(auth) {
		return false
	}
	updatedAt := record.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = now
	}
	reason := strings.TrimSpace(record.Reason)
	model := strings.TrimSpace(record.Model)
	quota := record.Quota
	if quota.Exceeded && quota.NextRecoverAt.IsZero() {
		quota.NextRecoverAt = record.NextRetryAfter
	}

	if model == "" {
		auth.Unavailable = true
		auth.Status = StatusError
		auth.NextRetryAfter = record.NextRetryAfter
		auth.Quota = quota
		auth.UpdatedAt = updatedAt
		if reason != "" {
			auth.StatusMessage = reason
		}
		auth.LastError = cloneError(record.LastError)
		return true
	}

	state := ensureModelState(auth, model)
	state.Unavailable = true
	state.Status = StatusError
	state.NextRetryAfter = record.NextRetryAfter
	state.Quota = quota
	state.UpdatedAt = updatedAt
	if reason != "" {
		state.StatusMessage = reason
	}
	state.LastError = cloneError(record.LastError)
	updateAggregatedAvailability(auth, now)
	return true
}

func clearCooldownStateForAuth(auth *Auth, now time.Time) bool {
	if auth == nil {
		return false
	}
	changed := false
	if auth.Unavailable || !auth.NextRetryAfter.IsZero() || auth.Quota.Exceeded || !auth.Quota.NextRecoverAt.IsZero() {
		auth.Unavailable = false
		auth.NextRetryAfter = time.Time{}
		auth.Quota = QuotaState{}
		auth.UpdatedAt = now
		changed = true
	}
	for _, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		if state.Unavailable || !state.NextRetryAfter.IsZero() || state.Quota.Exceeded || !state.Quota.NextRecoverAt.IsZero() {
			state.Unavailable = false
			state.NextRetryAfter = time.Time{}
			state.Quota = QuotaState{}
			state.UpdatedAt = now
			changed = true
		}
	}
	if len(auth.ModelStates) > 0 {
		updateAggregatedAvailability(auth, now)
	}
	return changed
}

func (m *Manager) persistCooldownStates(ctx context.Context) {
	if m == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	records, store := m.cooldownStateSnapshot()
	if store == nil {
		return
	}
	if errSave := store.Save(ctx, records); errSave != nil {
		logEntryWithRequestID(ctx).Warnf("failed to persist cooldown state: %v", errSave)
	}
}

func (m *Manager) cooldownStateSnapshot() ([]CooldownStateRecord, CooldownStateStore) {
	now := time.Now()
	records := make([]CooldownStateRecord, 0)

	m.mu.RLock()
	store := m.cooldownStore
	if store == nil {
		m.mu.RUnlock()
		return nil, nil
	}
	for _, auth := range m.auths {
		records = append(records, m.cooldownStateRecordsForAuthLocked(auth, now)...)
	}
	m.mu.RUnlock()

	sort.Slice(records, func(i, j int) bool {
		if records[i].Provider != records[j].Provider {
			return records[i].Provider < records[j].Provider
		}
		if records[i].AuthID != records[j].AuthID {
			return records[i].AuthID < records[j].AuthID
		}
		return records[i].Model < records[j].Model
	})
	return records, store
}

func (m *Manager) cooldownStateRecordsForAuthLocked(auth *Auth, now time.Time) []CooldownStateRecord {
	if auth == nil || auth.ID == "" || auth.Disabled || auth.Status == StatusDisabled || m.cooldownDisabledForAuth(auth) {
		return nil
	}
	records := make([]CooldownStateRecord, 0, 1+len(auth.ModelStates))
	if record, ok := authCooldownStateRecord(auth, now); ok {
		records = append(records, record)
	}
	for model, state := range auth.ModelStates {
		if record, ok := modelCooldownStateRecord(auth, model, state, now); ok {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].Model < records[j].Model
	})
	return records
}

func cooldownStateRecordsEqual(a, b []CooldownStateRecord) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !cooldownStateRecordEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

func cooldownStateRecordEqual(a, b CooldownStateRecord) bool {
	if a.Provider != b.Provider ||
		a.AuthID != b.AuthID ||
		a.AuthFile != b.AuthFile ||
		a.Model != b.Model ||
		a.Status != b.Status ||
		a.Reason != b.Reason ||
		!a.NextRetryAfter.Equal(b.NextRetryAfter) ||
		!a.UpdatedAt.Equal(b.UpdatedAt) ||
		!cooldownQuotaEqual(a.Quota, b.Quota) {
		return false
	}
	return cooldownErrorEqual(a.LastError, b.LastError)
}

func cooldownQuotaEqual(a, b QuotaState) bool {
	return a.Exceeded == b.Exceeded &&
		a.Reason == b.Reason &&
		a.BackoffLevel == b.BackoffLevel &&
		a.NextRecoverAt.Equal(b.NextRecoverAt)
}

func cooldownErrorEqual(a, b *Error) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Code == b.Code &&
		a.Message == b.Message &&
		a.Retryable == b.Retryable &&
		a.HTTPStatus == b.HTTPStatus
}

func authCooldownStateRecord(auth *Auth, now time.Time) (CooldownStateRecord, bool) {
	if auth == nil || !auth.Unavailable || auth.NextRetryAfter.IsZero() || !auth.NextRetryAfter.After(now) {
		return CooldownStateRecord{}, false
	}
	return CooldownStateRecord{
		Provider:       strings.TrimSpace(auth.Provider),
		AuthID:         auth.ID,
		AuthFile:       cooldownAuthFile(auth),
		Status:         "cooling",
		NextRetryAfter: auth.NextRetryAfter,
		Reason:         cooldownReason(auth.StatusMessage, auth.Quota, auth.LastError),
		Quota:          auth.Quota,
		LastError:      cloneError(auth.LastError),
		UpdatedAt:      auth.UpdatedAt,
	}, true
}

func modelCooldownStateRecord(auth *Auth, model string, state *ModelState, now time.Time) (CooldownStateRecord, bool) {
	model = strings.TrimSpace(model)
	if auth == nil || state == nil || model == "" || !state.Unavailable || state.NextRetryAfter.IsZero() || !state.NextRetryAfter.After(now) {
		return CooldownStateRecord{}, false
	}
	return CooldownStateRecord{
		Provider:       strings.TrimSpace(auth.Provider),
		AuthID:         auth.ID,
		AuthFile:       cooldownAuthFile(auth),
		Model:          model,
		Status:         "cooling",
		NextRetryAfter: state.NextRetryAfter,
		Reason:         cooldownReason(state.StatusMessage, state.Quota, state.LastError),
		Quota:          state.Quota,
		LastError:      cloneError(state.LastError),
		UpdatedAt:      state.UpdatedAt,
	}, true
}

func cooldownReason(statusMessage string, quota QuotaState, lastErr *Error) string {
	if reason := strings.TrimSpace(quota.Reason); reason != "" {
		return reason
	}
	if statusMessage = strings.TrimSpace(statusMessage); statusMessage != "" {
		return statusMessage
	}
	if lastErr != nil {
		if code := strings.TrimSpace(lastErr.Code); code != "" {
			return code
		}
		if message := strings.TrimSpace(lastErr.Message); message != "" {
			return message
		}
	}
	return ""
}

