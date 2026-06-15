package management

import (
	"context"
	"sort"
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// initAntigravityPrimaryInfo assigns a primary/standby role to a new antigravity
// credential. It is default-on (works even when AntigravityPrimaryHandoff is false)
// so that existing antigravity multi-account setups get deterministic primary
// selection without requiring an explicit config toggle.
func (h *Handler) initAntigravityPrimaryInfo(ctx context.Context, record *coreauth.Auth) {
	if h == nil || h.cfg == nil || record == nil {
		return
	}
	if !strings.EqualFold(strings.TrimSpace(record.Provider), "antigravity") {
		return
	}

	existingMaxOrder := 0
	hasExistingPrimary := false

	// Check both authManager and token store for existing antigravity credentials.
	// The token store may have credentials that aren't yet registered in authManager
	// (e.g., during sequential saveTokenRecord calls).
	if h.authManager != nil {
		for _, a := range h.authManager.List() {
			if a == nil || a.ID == record.ID {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(a.Provider), "antigravity") {
				continue
			}
			if a.PrimaryInfo != nil {
				if a.PrimaryInfo.IsPrimary {
					hasExistingPrimary = true
				}
				if a.PrimaryInfo.Order > existingMaxOrder {
					existingMaxOrder = a.PrimaryInfo.Order
				}
			} else if !a.Disabled && a.Status == coreauth.StatusActive {
				// Legacy active antigravity credential without explicit PrimaryInfo
				// is treated as the current primary.
				hasExistingPrimary = true
			}
		}
	}

	if tokenStore := h.tokenStoreWithBaseDir(); tokenStore != nil {
		if allAuths, err := tokenStore.List(ctx); err == nil {
			for _, a := range allAuths {
				if a == nil || a.ID == record.ID {
					continue
				}
				if !strings.EqualFold(strings.TrimSpace(a.Provider), "antigravity") {
					continue
				}
				if a.PrimaryInfo != nil {
					if a.PrimaryInfo.IsPrimary {
						hasExistingPrimary = true
					}
					if a.PrimaryInfo.Order > existingMaxOrder {
						existingMaxOrder = a.PrimaryInfo.Order
					}
				} else if !a.Disabled && a.Status == coreauth.StatusActive {
					hasExistingPrimary = true
				}
			}
		}
	}

	if hasExistingPrimary {
		record.PrimaryInfo = &coreauth.PrimaryInfo{
			IsPrimary: false,
			Order:     existingMaxOrder + 1,
		}
		record.Disabled = true
		record.Status = coreauth.StatusDisabled
	} else {
		record.PrimaryInfo = &coreauth.PrimaryInfo{
			IsPrimary: true,
			Order:     1,
		}
		record.Disabled = false
		record.Status = coreauth.StatusActive
	}
}

// ensureSoleAntigravityPrimary promotes the given antigravity credential to the
// sole primary and demotes all other antigravity credentials to standby. It also
// handles legacy active primaries that do not have a PrimaryInfo block.
func (h *Handler) ensureSoleAntigravityPrimary(ctx context.Context, auth *coreauth.Auth) {
	if h == nil || auth == nil || h.authManager == nil {
		return
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "antigravity") {
		return
	}

	// Promote the target credential to primary.
	auth.Disabled = false
	auth.Status = coreauth.StatusActive
	auth.PrimaryInfo = &coreauth.PrimaryInfo{
		IsPrimary: true,
		Order:     1,
	}

	// Demote all other antigravity credentials that look like primaries.
	for _, a := range h.authManager.List() {
		if a == nil || a.ID == auth.ID {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(a.Provider), "antigravity") {
			continue
		}
		if a.PrimaryInfo != nil && a.PrimaryInfo.IsPrimary {
			a.PrimaryInfo.IsPrimary = false
			a.Disabled = true
			a.Status = coreauth.StatusDisabled
			_, _ = h.authManager.Update(ctx, a)
		} else if a.PrimaryInfo == nil && !a.Disabled && a.Status == coreauth.StatusActive {
			// Legacy active primary without explicit PrimaryInfo.
			a.PrimaryInfo = &coreauth.PrimaryInfo{IsPrimary: false, Order: 0}
			a.Disabled = true
			a.Status = coreauth.StatusDisabled
			_, _ = h.authManager.Update(ctx, a)
		}
	}

	_, _ = h.authManager.Update(ctx, auth)
}

// antigravityPrimaryForList returns a deterministic primary antigravity credential
// from the current manager state, or nil if none exists. It treats legacy active
// antigravity credentials without PrimaryInfo as primary, and also promotes a sole
// enabled antigravity credential that has a stale IsPrimary:false PrimaryInfo.
func (h *Handler) antigravityPrimaryForList() *coreauth.Auth {
	if h == nil || h.authManager == nil {
		return nil
	}
	antigravityAuths := make([]*coreauth.Auth, 0)
	for _, a := range h.authManager.List() {
		if a == nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(a.Provider), "antigravity") {
			continue
		}
		antigravityAuths = append(antigravityAuths, a)
	}
	if len(antigravityAuths) == 0 {
		return nil
	}
	explicit := make([]*coreauth.Auth, 0)
	for _, a := range antigravityAuths {
		if a.PrimaryInfo != nil && a.PrimaryInfo.IsPrimary {
			explicit = append(explicit, a)
		}
	}
	if len(explicit) > 0 {
		sort.Slice(explicit, func(i, j int) bool {
			return explicit[i].ID < explicit[j].ID
		})
		return explicit[0]
	}
	legacy := make([]*coreauth.Auth, 0)
	for _, a := range antigravityAuths {
		if a.PrimaryInfo == nil && !a.Disabled && a.Status == coreauth.StatusActive {
			legacy = append(legacy, a)
		}
	}
	if len(legacy) > 0 {
		sort.Slice(legacy, func(i, j int) bool {
			return legacy[i].ID < legacy[j].ID
		})
		return legacy[0]
	}
	enabled := make([]*coreauth.Auth, 0)
	for _, a := range antigravityAuths {
		if !a.Disabled && a.Status == coreauth.StatusActive {
			enabled = append(enabled, a)
		}
	}
	if len(enabled) == 1 {
		return enabled[0]
	}
	return nil
}

// reconcileAntigravityPrimaryInfoForResponse returns the primary_info that should
// be displayed for an antigravity auth in list responses. It performs list-time
// reconciliation without mutating the stored state: a sole enabled antigravity
// credential is shown as primary even if its stored PrimaryInfo says otherwise.
func (h *Handler) reconcileAntigravityPrimaryInfoForResponse(auth *coreauth.Auth) *coreauth.PrimaryInfo {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "antigravity") {
		return nil
	}
	primary := h.antigravityPrimaryForList()
	if primary != nil && primary.ID == auth.ID {
		return &coreauth.PrimaryInfo{IsPrimary: true, Order: 1}
	}
	if auth.Disabled || auth.Status == coreauth.StatusDisabled {
		return &coreauth.PrimaryInfo{IsPrimary: false, Order: 0}
	}
	if auth.PrimaryInfo != nil {
		return auth.PrimaryInfo
	}
	return nil
}
