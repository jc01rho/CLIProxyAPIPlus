package helps

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	homekv "github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ClaudeAgentSessionUUID maps the downstream agent conversation to one stable UUID.
func ClaudeAgentSessionUUID(headers http.Header, originalPayload, translatedPayload []byte, metadataSets ...map[string]any) string {
	metadata := mergeClaudeSessionMetadata(metadataSets...)
	identity := cliproxyauth.ExtractSessionID(headers, originalPayload, metadata)
	if identity == "" && len(translatedPayload) > 0 {
		identity = cliproxyauth.ExtractSessionID(headers, translatedPayload, metadata)
	}
	if identity == "" {
		return uuid.NewString()
	}
	if strings.HasPrefix(identity, "claude:") {
		if parsed, errParse := uuid.Parse(strings.TrimPrefix(identity, "claude:")); errParse == nil {
			return parsed.String()
		}
	}
	if parsed, errParse := uuid.Parse(identity); errParse == nil {
		return parsed.String()
	}
	stableInput := "cli-proxy-api\x00claude\x00agent-conversation\x00" + identity
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(stableInput)).String()
}

func mergeClaudeSessionMetadata(metadataSets ...map[string]any) map[string]any {
	var merged map[string]any
	for _, metadata := range metadataSets {
		if len(metadata) == 0 {
			continue
		}
		if merged == nil {
			merged = make(map[string]any)
		}
		for key, value := range metadata {
			if _, exists := merged[key]; !exists {
				merged[key] = value
			}
		}
	}
	return merged
}

type claudeCredentialDevicePoolKVClient interface {
	KVGet(context.Context, string) ([]byte, bool, error)
	KVSet(context.Context, string, []byte, homekv.KVSetOptions) (bool, error)
}

var currentClaudeCredentialDevicePoolKVClient = func() (claudeCredentialDevicePoolKVClient, bool, error) {
	client, homeMode, errClient := homekv.CurrentKVClient()
	return client, homeMode, errClient
}

// EnsureClaudeCredentialDevicePoolRequired initializes a credential pool locally,
// or coordinates it through Home KV when the selected auth is a remote dispatch clone.
func EnsureClaudeCredentialDevicePoolRequired(ctx context.Context, auth *cliproxyauth.Auth) ([]string, error) {
	if auth == nil {
		return nil, fmt.Errorf("ensure Claude credential device pool: auth is nil")
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	rawCredentialDeviceIDs := auth.Metadata[claudeauth.ClaudeDeviceIDsMetadataKey]
	if claudeauth.HasCanonicalDeviceIDPool(rawCredentialDeviceIDs) {
		return claudeauth.NormalizeDeviceIDPool(rawCredentialDeviceIDs), nil
	}
	credentialCandidate := claudeauth.NormalizeDeviceIDPool(rawCredentialDeviceIDs)

	client, homeMode, errClient := currentClaudeCredentialDevicePoolKVClient()
	if !homeMode {
		deviceIDs, _, errEnsure := claudeauth.EnsureDeviceIDPool(auth.Metadata)
		return deviceIDs, errEnsure
	}
	if errClient != nil {
		return nil, fmt.Errorf("ensure Claude credential device pool: Home KV client: %w", errClient)
	}
	identity := strings.TrimSpace(auth.EnsureIndex())
	if identity == "" {
		identity = strings.TrimSpace(auth.ID)
	}
	if identity == "" {
		return nil, fmt.Errorf("ensure Claude credential device pool: credential identity is empty")
	}
	key := "cpa:claude:credential-device-pool:" + homekv.HashKeyPart(identity)
	if raw, found, errGet := client.KVGet(ctx, key); errGet != nil {
		return nil, fmt.Errorf("ensure Claude credential device pool: Home KV get: %w", errGet)
	} else if found {
		var stored []string
		if errUnmarshal := json.Unmarshal(raw, &stored); errUnmarshal == nil {
			if deviceIDs := claudeauth.NormalizeDeviceIDPool(stored); len(deviceIDs) == claudeauth.ClaudeDevicePoolSize {
				if !claudeauth.HasCanonicalDeviceIDPool(stored) {
					canonicalRaw, errMarshal := json.Marshal(deviceIDs)
					if errMarshal != nil {
						return nil, fmt.Errorf("ensure Claude credential device pool: marshal canonical Home KV value: %w", errMarshal)
					}
					written, errSet := client.KVSet(ctx, key, canonicalRaw, homekv.KVSetOptions{XX: true})
					if errSet != nil {
						return nil, fmt.Errorf("ensure Claude credential device pool: canonicalize Home KV value: %w", errSet)
					}
					if !written {
						return nil, fmt.Errorf("ensure Claude credential device pool: canonical Home KV value was not written")
					}
				}
				auth.Metadata[claudeauth.ClaudeDeviceIDsMetadataKey] = append([]string(nil), deviceIDs...)
				return deviceIDs, nil
			}
		}
	}

	deviceIDs := credentialCandidate
	if len(deviceIDs) != claudeauth.ClaudeDevicePoolSize {
		var errGenerate error
		deviceIDs, errGenerate = claudeauth.GenerateDeviceIDPool()
		if errGenerate != nil {
			return nil, errGenerate
		}
	}
	raw, errMarshal := json.Marshal(deviceIDs)
	if errMarshal != nil {
		return nil, fmt.Errorf("ensure Claude credential device pool: marshal Home KV value: %w", errMarshal)
	}
	if _, errSet := client.KVSet(ctx, key, raw, homekv.KVSetOptions{NX: true}); errSet != nil {
		return nil, fmt.Errorf("ensure Claude credential device pool: Home KV set: %w", errSet)
	}
	raw, found, errGet := client.KVGet(ctx, key)
	if errGet != nil {
		return nil, fmt.Errorf("ensure Claude credential device pool: Home KV reread: %w", errGet)
	}
	if !found {
		return nil, fmt.Errorf("ensure Claude credential device pool: Home KV value missing after set")
	}
	var stored []string
	if errUnmarshal := json.Unmarshal(raw, &stored); errUnmarshal != nil {
		return nil, fmt.Errorf("ensure Claude credential device pool: decode Home KV value: %w", errUnmarshal)
	}
	deviceIDs = claudeauth.NormalizeDeviceIDPool(stored)
	if len(deviceIDs) != claudeauth.ClaudeDevicePoolSize {
		return nil, fmt.Errorf("ensure Claude credential device pool: Home KV pool has %d entries, want %d", len(deviceIDs), claudeauth.ClaudeDevicePoolSize)
	}
	auth.Metadata[claudeauth.ClaudeDeviceIDsMetadataKey] = append([]string(nil), deviceIDs...)
	return deviceIDs, nil
}

// ClaudeCredentialAccountUUID returns the selected upstream credential's account UUID.
func ClaudeCredentialAccountUUID(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	for _, key := range []string{"account_uuid", "accountUuid"} {
		value, _ := auth.Metadata[key].(string)
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

// ApplyClaudeCredentialMetadata rewrites the identity exception shared by native and cloaked OAuth requests.
func ApplyClaudeCredentialMetadata(payload []byte, auth *cliproxyauth.Auth, sessionID string) ([]byte, string, error) {
	if auth == nil {
		return nil, "", fmt.Errorf("apply Claude credential metadata: auth is nil")
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	deviceIDs, _, errDeviceIDs := claudeauth.EnsureDeviceIDPool(auth.Metadata)
	if errDeviceIDs != nil {
		return nil, "", errDeviceIDs
	}
	deviceID, errDeviceID := claudeauth.SelectDeviceID(deviceIDs, sessionID)
	if errDeviceID != nil {
		return nil, "", errDeviceID
	}

	existing := strings.TrimSpace(gjson.GetBytes(payload, "metadata.user_id").String())
	encoded := []byte(existing)
	if !gjson.ValidBytes(encoded) || !gjson.ParseBytes(encoded).IsObject() {
		encoded = []byte(`{}`)
	}
	var errSetIdentity error
	if encoded, errSetIdentity = sjson.SetBytes(encoded, "device_id", deviceID); errSetIdentity != nil {
		return nil, "", fmt.Errorf("set Claude credential device ID: %w", errSetIdentity)
	}
	if encoded, errSetIdentity = sjson.SetBytes(encoded, "account_uuid", ClaudeCredentialAccountUUID(auth)); errSetIdentity != nil {
		return nil, "", fmt.Errorf("set Claude credential account UUID: %w", errSetIdentity)
	}
	if encoded, errSetIdentity = sjson.SetBytes(encoded, "session_id", sessionID); errSetIdentity != nil {
		return nil, "", fmt.Errorf("set Claude credential session ID: %w", errSetIdentity)
	}
	updated, errSet := sjson.SetBytes(payload, "metadata.user_id", string(encoded))
	if errSet != nil {
		return nil, "", fmt.Errorf("set Claude credential metadata: %w", errSet)
	}
	return updated, deviceID, nil
}
