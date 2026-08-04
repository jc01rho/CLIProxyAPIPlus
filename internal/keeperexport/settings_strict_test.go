package keeperexport_test

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/keeperexport"
)

// TestDecodeSettingsPutRequestRequiredKeys audits that a complete settings
// replacement must carry every required envelope object/field. Any omission is
// a payload-layer invalid_field, never a silent zero-default acceptance.
func TestDecodeSettingsPutRequestRequiredKeys(t *testing.T) {
	valid := settingsPutFixture(t)
	requireSettingsOK(t, valid)

	topLevel := []string{"protocolVersion", "settings"}
	for _, key := range topLevel {
		t.Run("top-"+key, func(t *testing.T) {
			requireSettingsCode(t, dropKey(valid, key), "invalid_field")
		})
	}

	for _, key := range []string{
		"enabled", "mode", "keeper", "outbox", "delivery", "metadata", "privacy",
	} {
		key := key
		t.Run("settings."+key, func(t *testing.T) {
			dropped, err := removeNestedKey(valid, []string{"settings"}, key)
			if err != nil {
				t.Fatal(err)
			}
			requireSettingsCode(t, dropped, "invalid_field")
		})
	}

	for _, key := range []string{"url", "tokenEnv", "caFile", "clientCertFile", "clientKeyFile"} {
		key := key
		t.Run("keeper."+key, func(t *testing.T) {
			dropped, err := removeNestedKey(valid, []string{"settings", "keeper"}, key)
			if err != nil {
				t.Fatal(err)
			}
			requireSettingsCode(t, dropped, "invalid_field")
		})
	}

	for _, key := range []string{"path", "maxBytes"} {
		key := key
		t.Run("outbox."+key, func(t *testing.T) {
			dropped, err := removeNestedKey(valid, []string{"settings", "outbox"}, key)
			if err != nil {
				t.Fatal(err)
			}
			requireSettingsCode(t, dropped, "invalid_field")
		})
	}

	for _, key := range []string{
		"maxBatchEvents", "maxBatchBytes", "flushIntervalMs",
		"requestTimeoutMs", "initialBackoffMs", "maxBackoffMs",
	} {
		key := key
		t.Run("delivery."+key, func(t *testing.T) {
			dropped, err := removeNestedKey(valid, []string{"settings", "delivery"}, key)
			if err != nil {
				t.Fatal(err)
			}
			requireSettingsCode(t, dropped, "invalid_field")
		})
	}

	for _, key := range []string{"enabled", "intervalMs", "categories"} {
		key := key
		t.Run("metadata."+key, func(t *testing.T) {
			dropped, err := removeNestedKey(valid, []string{"settings", "metadata"}, key)
			if err != nil {
				t.Fatal(err)
			}
			requireSettingsCode(t, dropped, "invalid_field")
		})
	}

	for _, key := range []string{"includeClientIp", "includeForwardedFor", "includeUserAgent"} {
		key := key
		t.Run("privacy."+key, func(t *testing.T) {
			dropped, err := removeNestedKey(valid, []string{"settings", "privacy"}, key)
			if err != nil {
				t.Fatal(err)
			}
			requireSettingsCode(t, dropped, "invalid_field")
		})
	}
}

func TestDecodeSettingsPutRequestProtocolVersionAbsentVsWrong(t *testing.T) {
	valid := settingsPutFixture(t)

	absent := dropKey(valid, "protocolVersion")
	if _, perr := keeperexport.DecodeSettingsPutRequest(absent); perr == nil || perr.Code != "invalid_field" {
		t.Fatalf("absent protocolVersion code=%v, want invalid_field", perr)
	}

	wrong := setJSONString(valid, "protocolVersion", "keeper-export/v2")
	if _, perr := keeperexport.DecodeSettingsPutRequest(wrong); perr == nil || perr.Code != "unsupported_protocol_version" {
		t.Fatalf("wrong protocolVersion code=%v, want unsupported_protocol_version", perr)
	}
}

func TestDecodeStatusResponseRejectsUnknownMetadataRevisionsKey(t *testing.T) {
	valid := statusFixture(t)
	cases := []struct {
		name string
		data []byte
	}{
		{"unknown metadata revision", addNestedKey(valid, "metadataRevisions", "bogus_category", 1)},
		{"negative metadata revision", setNestedInt(valid, "metadataRevisions", "api_keys", -1)},
		{"oversized metadata revision", setNestedInt(valid, "metadataRevisions", "api_keys", keeperexport.MaxSafeInteger+1)},
		{"missing required field", dropKey(valid, "lastError")},
		{"retrying without retry timestamp", setJSONNull(valid, "nextRetryAt")},
		{"retrying without retryable error", setJSONNull(valid, "lastError")},
		{"watermark without stream", setJSONNull(valid, "streamId")},
		{"keeper next does not follow ack", setJSONInt(valid, "nextExpectedSequence", 99)},
		{"backlog missing oldest", setJSONNull(valid, "oldestBacklogAt")},
		{"unstable error message", setNestedString(valid, "lastError", "message", "REMOTE_SECRET_MARKER")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, perr := keeperexport.DecodeStatusResponse(tc.data); perr == nil || perr.Code != "invalid_field" {
				t.Fatalf("code=%v, want invalid_field", perr)
			}
		})
	}
}

func requireSettingsOK(t *testing.T, data []byte) {
	t.Helper()
	if _, perr := keeperexport.DecodeSettingsPutRequest(data); perr != nil {
		t.Fatalf("valid settings rejected: %v", perr)
	}
}

func requireSettingsCode(t *testing.T, data []byte, want string) {
	t.Helper()
	if _, perr := keeperexport.DecodeSettingsPutRequest(data); perr == nil || perr.Code != want {
		t.Fatalf("code=%v, want %q", perr, want)
	}
}

func settingsPutFixture(t *testing.T) []byte {
	t.Helper()
	data := readFixture(t, "settings-response.valid.json")
	stripped, err := removeNestedKey(data, []string{"settings", "keeper"}, "tokenConfigured")
	if err != nil {
		t.Fatal(err)
	}
	return stripped
}

func statusFixture(t *testing.T) []byte {
	t.Helper()
	return readFixture(t, "status-retrying.valid.json")
}

func dropKey(data []byte, key string) []byte {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		panic(err)
	}
	delete(m, key)
	out, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return out
}

// removeNestedKey returns data where the JSON value at path (a sequence of
// object keys leading to the parent object) has key removed from it.
func removeNestedKey(data []byte, path []string, key string) ([]byte, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	if len(path) == 0 {
		delete(root, key)
		return json.Marshal(root)
	}
	raw, ok := root[path[0]]
	if !ok {
		return data, nil
	}
	newRaw, err := removeNestedKey(raw, path[1:], key)
	if err != nil {
		return nil, err
	}
	root[path[0]] = newRaw
	return json.Marshal(root)
}

func addNestedKey(data []byte, parentKey, key string, value any) []byte {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		panic(err)
	}
	var nested map[string]json.RawMessage
	if err := json.Unmarshal(root[parentKey], &nested); err != nil {
		panic(err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	nested[key] = raw
	fixed, err := json.Marshal(nested)
	if err != nil {
		panic(err)
	}
	root[parentKey] = fixed
	out, err := json.Marshal(root)
	if err != nil {
		panic(err)
	}
	return out
}

func setNestedInt(data []byte, parentKey, key string, value int64) []byte {
	return addNestedKey(data, parentKey, key, value)
}

func setJSONString(data []byte, key string, value string) []byte {
	return setJSONValue(data, key, value)
}

func setJSONInt(data []byte, key string, value int64) []byte {
	return setJSONValue(data, key, value)
}

func setJSONNull(data []byte, key string) []byte {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		panic(err)
	}
	m[key] = json.RawMessage("null")
	out, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return out
}

func setNestedString(data []byte, parentKey, key, value string) []byte {
	return addNestedKey(data, parentKey, key, value)
}

func setJSONValue(data []byte, key string, value any) []byte {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		panic(err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	m[key] = raw
	out, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return out
}
