package keeperexport

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"net/url"
)

// MetadataCategory is one of the three fixed snapshot categories.
type MetadataCategory string

const (
	CategoryAuthFiles          MetadataCategory = "auth_files"
	CategoryAPIKeys            MetadataCategory = "api_keys"
	CategoryProviderIdentities MetadataCategory = "provider_identities"
)

// AuthFileItem is the secret-free auth-file snapshot item (contract 7.2).
type AuthFileItem struct {
	AuthIndex   string
	Name        string
	DisplayName string
	Type        string
	Provider    string
	Prefix      string
	Priority    *int64
	Disabled    *bool
	Note        *string
	AccountID   *string
	ProjectID   *string
	XAIUserID   *string
	ActiveStart *string
	ActiveUntil *string
	PlanType    *string
}

// APIKeyItem is the masked CPA client API-key snapshot item (contract 7.3).
type APIKeyItem struct {
	Fingerprint string
	DisplayKey  string
	Alias       string
}

// ProviderIdentityItem is the provider credential identity item (contract 7.4).
type ProviderIdentityItem struct {
	AuthIndex         string
	ProviderType      string
	DisplayName       string
	Prefix            string
	BaseURL           *string
	Priority          *int64
	Disabled          *bool
	Note              *string
	APIKeyFingerprint *string
}

// MetadataSnapshot is a typed complete metadata snapshot (contract 7.1).
// Exactly one item slice is populated, matching the decoded category.
type MetadataSnapshot struct {
	Revision           int64
	GeneratedAt        string
	AuthFiles          []AuthFileItem
	APIKeys            []APIKeyItem
	ProviderIdentities []ProviderIdentityItem
}

// MetadataApplyResponse is the typed snapshot success response (contract 7.1).
type MetadataApplyResponse struct {
	Category   MetadataCategory
	Revision   int64
	Applied    bool
	ItemCount  int64
	ServerTime string
}

type metadataEnvelopeWire struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Revision        int64           `json:"revision"`
	Complete        bool            `json:"complete"`
	GeneratedAt     string          `json:"generatedAt"`
	Items           json.RawMessage `json:"items"`
}

type authFileItemWire struct {
	AuthIndex   string  `json:"authIndex"`
	Name        string  `json:"name"`
	DisplayName string  `json:"displayName"`
	Type        string  `json:"type"`
	Provider    string  `json:"provider"`
	Prefix      string  `json:"prefix"`
	Priority    *int64  `json:"priority"`
	Disabled    *bool   `json:"disabled"`
	Note        *string `json:"note"`
	AccountID   *string `json:"accountId"`
	ProjectID   *string `json:"projectId"`
	XAIUserID   *string `json:"xaiUserId"`
	ActiveStart *string `json:"activeStart"`
	ActiveUntil *string `json:"activeUntil"`
	PlanType    *string `json:"planType"`
}

type apiKeyItemWire struct {
	Fingerprint string `json:"fingerprint"`
	DisplayKey  string `json:"displayKey"`
	Alias       string `json:"alias"`
}

type providerIdentityItemWire struct {
	AuthIndex         string  `json:"authIndex"`
	ProviderType      string  `json:"providerType"`
	DisplayName       string  `json:"displayName"`
	Prefix            string  `json:"prefix"`
	BaseURL           *string `json:"baseUrl"`
	Priority          *int64  `json:"priority"`
	Disabled          *bool   `json:"disabled"`
	Note              *string `json:"note"`
	APIKeyFingerprint *string `json:"apiKeyFingerprint"`
}

var (
	authFileItemRequiredKeys = []string{
		"authIndex", "name", "displayName", "type", "provider", "prefix",
		"priority", "disabled", "note", "accountId", "projectId", "xaiUserId",
		"activeStart", "activeUntil", "planType",
	}
	apiKeyItemRequiredKeys       = []string{"fingerprint", "displayKey", "alias"}
	providerIdentityRequiredKeys = []string{
		"authIndex", "providerType", "displayName", "prefix", "baseUrl",
		"priority", "disabled", "note", "apiKeyFingerprint",
	}
)

// DecodeMetadataSnapshot strictly decodes and validates a complete metadata
// snapshot request for the URL-derived category, per contract section 7.
func DecodeMetadataSnapshot(data []byte, category MetadataCategory) (*MetadataSnapshot, *Error) {
	switch category {
	case CategoryAuthFiles, CategoryAPIKeys, CategoryProviderIdentities:
	default:
		return nil, protocolError("invalid_field")
	}
	if perr := requestPrecheck(data); perr != nil {
		return nil, perr
	}
	var wire metadataEnvelopeWire
	if perr := decodeTyped(data, &wire); perr != nil {
		return nil, perr
	}
	if !wire.Complete {
		return nil, protocolError("incomplete_snapshot")
	}
	if wire.Revision < 1 || wire.Revision > MaxSafeInteger {
		return nil, protocolError("invalid_field")
	}
	if !isTimestamp(wire.GeneratedAt) {
		return nil, protocolError("invalid_field")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(wire.Items, &items); err != nil {
		return nil, protocolError("invalid_field")
	}
	if len(items) > MaxMetadataItems {
		return nil, protocolError("batch_limit_exceeded")
	}
	snapshot := &MetadataSnapshot{Revision: wire.Revision, GeneratedAt: wire.GeneratedAt}
	seen := make(map[string]struct{}, len(items))
	register := func(identity string) *Error {
		if _, dup := seen[identity]; dup {
			return protocolError("duplicate_metadata_identity")
		}
		seen[identity] = struct{}{}
		return nil
	}
	for _, raw := range items {
		switch category {
		case CategoryAuthFiles:
			item, perr := decodeAuthFileItem(raw)
			if perr != nil {
				return nil, perr
			}
			if perr := register(item.AuthIndex); perr != nil {
				return nil, perr
			}
			snapshot.AuthFiles = append(snapshot.AuthFiles, *item)
		case CategoryAPIKeys:
			item, perr := decodeAPIKeyItem(raw)
			if perr != nil {
				return nil, perr
			}
			if perr := register(item.Fingerprint); perr != nil {
				return nil, perr
			}
			snapshot.APIKeys = append(snapshot.APIKeys, *item)
		case CategoryProviderIdentities:
			item, perr := decodeProviderIdentityItem(raw)
			if perr != nil {
				return nil, perr
			}
			if perr := register(item.ProviderType + "\x00" + item.AuthIndex); perr != nil {
				return nil, perr
			}
			snapshot.ProviderIdentities = append(snapshot.ProviderIdentities, *item)
		}
	}
	return snapshot, nil
}

func decodeAuthFileItem(raw json.RawMessage) (*AuthFileItem, *Error) {
	if perr := requireKeys(raw, authFileItemRequiredKeys...); perr != nil {
		return nil, perr
	}
	var wire authFileItemWire
	if perr := decodeTyped(raw, &wire); perr != nil {
		return nil, perr
	}
	invalid := func() *Error { return protocolError("invalid_field") }
	if !stringLenInRange(wire.AuthIndex, 1, 256) ||
		!stringLenInRange(wire.Name, 0, 256) ||
		!stringLenInRange(wire.DisplayName, 0, 256) ||
		!stringLenInRange(wire.Type, 0, 128) ||
		!stringLenInRange(wire.Provider, 0, 256) ||
		!stringLenInRange(wire.Prefix, 0, 256) {
		return nil, invalid()
	}
	if wire.Priority != nil && (*wire.Priority < -1000000 || *wire.Priority > 1000000) {
		return nil, invalid()
	}
	if !stringPtrLenInRange(wire.Note, 0, 1024) ||
		!stringPtrLenInRange(wire.AccountID, 1, 256) ||
		!stringPtrLenInRange(wire.ProjectID, 1, 256) ||
		!stringPtrLenInRange(wire.XAIUserID, 1, 256) ||
		!stringPtrLenInRange(wire.PlanType, 1, 256) {
		return nil, invalid()
	}
	if (wire.ActiveStart != nil && !isTimestamp(*wire.ActiveStart)) ||
		(wire.ActiveUntil != nil && !isTimestamp(*wire.ActiveUntil)) {
		return nil, invalid()
	}
	return &AuthFileItem{
		AuthIndex: wire.AuthIndex, Name: wire.Name, DisplayName: wire.DisplayName,
		Type: wire.Type, Provider: wire.Provider, Prefix: wire.Prefix,
		Priority: wire.Priority, Disabled: wire.Disabled, Note: wire.Note,
		AccountID: wire.AccountID, ProjectID: wire.ProjectID, XAIUserID: wire.XAIUserID,
		ActiveStart: wire.ActiveStart, ActiveUntil: wire.ActiveUntil, PlanType: wire.PlanType,
	}, nil
}

func decodeAPIKeyItem(raw json.RawMessage) (*APIKeyItem, *Error) {
	if perr := requireKeys(raw, apiKeyItemRequiredKeys...); perr != nil {
		return nil, perr
	}
	var wire apiKeyItemWire
	if perr := decodeTyped(raw, &wire); perr != nil {
		return nil, perr
	}
	if !isFingerprint(wire.Fingerprint) ||
		!stringLenInRange(wire.DisplayKey, 1, 128) ||
		!stringLenInRange(wire.Alias, 0, 256) {
		return nil, protocolError("invalid_field")
	}
	return &APIKeyItem{Fingerprint: wire.Fingerprint, DisplayKey: wire.DisplayKey, Alias: wire.Alias}, nil
}

func decodeProviderIdentityItem(raw json.RawMessage) (*ProviderIdentityItem, *Error) {
	if perr := requireKeys(raw, providerIdentityRequiredKeys...); perr != nil {
		return nil, perr
	}
	var wire providerIdentityItemWire
	if perr := decodeTyped(raw, &wire); perr != nil {
		return nil, perr
	}
	invalid := func() *Error { return protocolError("invalid_field") }
	if !stringLenInRange(wire.AuthIndex, 1, 256) ||
		!stringLenInRange(wire.ProviderType, 1, 128) ||
		!stringLenInRange(wire.DisplayName, 0, 256) ||
		!stringLenInRange(wire.Prefix, 0, 256) {
		return nil, invalid()
	}
	if wire.BaseURL != nil && !isHTTPSOrigin(*wire.BaseURL) {
		return nil, invalid()
	}
	if wire.Priority != nil && (*wire.Priority < -1000000 || *wire.Priority > 1000000) {
		return nil, invalid()
	}
	if !stringPtrLenInRange(wire.Note, 0, 1024) {
		return nil, invalid()
	}
	if wire.APIKeyFingerprint != nil && !isFingerprint(*wire.APIKeyFingerprint) {
		return nil, invalid()
	}
	return &ProviderIdentityItem{
		AuthIndex: wire.AuthIndex, ProviderType: wire.ProviderType,
		DisplayName: wire.DisplayName, Prefix: wire.Prefix, BaseURL: wire.BaseURL,
		Priority: wire.Priority, Disabled: wire.Disabled, Note: wire.Note,
		APIKeyFingerprint: wire.APIKeyFingerprint,
	}, nil
}

// isHTTPSOrigin reports whether raw is an absolute https URL within the byte
// limit, with no userinfo, query, or fragment (contract sections 7.4 and 8.1).
func isHTTPSOrigin(raw string) bool {
	if !stringLenInRange(raw, 1, 2048) {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return parsed.Scheme == "https" && parsed.Host != "" &&
		parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}

// isHTTPOrigin reports whether raw is an absolute http or https URL within the
// byte limit, with no userinfo, query, or fragment. The keeper export URL may
// target a plaintext HTTP endpoint (e.g. an RFC1918 address) when TLS is
// terminated elsewhere; provider identity base URLs stay HTTPS-only.
func isHTTPOrigin(raw string) bool {
	if !stringLenInRange(raw, 1, 2048) {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != "" &&
		parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}

// CheckMetadataRevision applies the revision arithmetic of contract section
// 7.1 against the stored current revision and its exact request-body digest.
// It returns nil when the snapshot must be applied (newer revision) or is an
// idempotent exact replay, and the stable conflict/stale errors otherwise.
func CheckMetadataRevision(currentRevision int64, currentDigest []byte, snapshotRevision int64, requestBody []byte) *Error {
	switch {
	case snapshotRevision < currentRevision:
		return protocolError("stale_revision")
	case snapshotRevision == currentRevision:
		digest := sha256.Sum256(requestBody)
		if !bytes.Equal(digest[:], currentDigest) {
			return protocolError("conflicting_revision")
		}
		return nil
	default:
		return nil
	}
}

// DecodeMetadataApplyResponse strictly decodes a snapshot success response.
func DecodeMetadataApplyResponse(data []byte) (*MetadataApplyResponse, *Error) {
	if perr := responsePrecheck(data); perr != nil {
		return nil, perr
	}
	var wire struct {
		ProtocolVersion string `json:"protocolVersion"`
		Category        string `json:"category"`
		Revision        int64  `json:"revision"`
		Applied         bool   `json:"applied"`
		ItemCount       int64  `json:"itemCount"`
		ServerTime      string `json:"serverTime"`
	}
	if perr := decodeTyped(data, &wire); perr != nil {
		return nil, perr
	}
	category := MetadataCategory(wire.Category)
	switch category {
	case CategoryAuthFiles, CategoryAPIKeys, CategoryProviderIdentities:
	default:
		return nil, protocolError("invalid_field")
	}
	if wire.Revision < 0 || wire.Revision > MaxSafeInteger ||
		wire.ItemCount < 0 || wire.ItemCount > MaxSafeInteger ||
		!isTimestamp(wire.ServerTime) {
		return nil, protocolError("invalid_field")
	}
	return &MetadataApplyResponse{
		Category:   category,
		Revision:   wire.Revision,
		Applied:    wire.Applied,
		ItemCount:  wire.ItemCount,
		ServerTime: wire.ServerTime,
	}, nil
}
