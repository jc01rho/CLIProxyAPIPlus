package keeperexport_test

import (
	"encoding/json"
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/keeperexport"
)

// TestFixtureMutation is the manual-QA driver from the protocol contract
// section 13. It is skipped during normal runs; the contract's mutation
// commands invoke it as:
//
//	go test ./internal/keeperexport -run TestFixtureMutation -args <file> <expectedCode>
//
// It decodes a mutated copy of a fixture (never a checked-in fixture) with the
// strict decoder matching its wire shape and requires rejection with the exact
// stable code.
func TestFixtureMutation(t *testing.T) {
	args := flag.Args()
	if len(args) == 0 {
		t.Skip("no mutation arguments; run with -args <file> <expectedCode>")
	}
	if len(args) != 2 {
		t.Fatalf("want exactly 2 args <file> <expectedCode>, got %v", args)
	}
	path, expected := args[0], args[1]

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read mutated fixture: %v", err)
	}

	decodeErr := decodeByShape(path, data)
	if decodeErr == nil {
		t.Fatalf("mutated fixture %s was accepted; want rejection with code %q", path, expected)
	}
	if decodeErr.Code != expected {
		t.Fatalf("mutated fixture %s rejected with code %q, want %q", path, decodeErr.Code, expected)
	}
	t.Logf("rejected %s with expected code %q", path, decodeErr.Code)
}

// decodeByShape selects the strict decoder matching the document's wire shape.
func decodeByShape(path string, data []byte) *keeperexport.Error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		// Not even generic JSON (for example duplicate keys are preserved only
		// by strict decoders); fall through to the usage batch decoder, whose
		// strict scan must report the stable code.
		_, decErr := keeperexport.DecodeUsageBatch(data)
		return decErr
	}
	has := func(keys ...string) bool {
		for _, key := range keys {
			if _, ok := top[key]; ok {
				return true
			}
		}
		return false
	}

	switch {
	case has("error"):
		_, err := keeperexport.DecodeErrorEnvelope(data)
		return err
	case has("events", "streamId"):
		_, err := keeperexport.DecodeUsageBatch(data)
		return err
	case has("acknowledgedThrough"):
		_, err := keeperexport.DecodeUsageAck(data)
		return err
	case has("items", "revision"):
		_, err := keeperexport.DecodeMetadataSnapshot(data, categoryForPath(path, data))
		return err
	case has("settings"):
		_, err := keeperexport.DecodeSettingsPutRequest(data)
		return err
	case has("state"):
		_, err := keeperexport.DecodeStatusResponse(data)
		return err
	case has("ok"):
		_, err := keeperexport.DecodeConnectionTestResponse(data)
		return err
	case has("fingerprintSecretHex"):
		_, err := keeperexport.DecodeFingerprintVectors(data)
		return err
	case has("credential") && has("displayName"):
		_, err := keeperexport.DecodeCreateInstanceRequest(data)
		return err
	case has("instance") && has("credential"):
		_, err := keeperexport.DecodeInstanceRegistration(data)
		return err
	case has("instance"):
		_, err := keeperexport.DecodeIdentityResponse(data)
		return err
	default:
		_, err := keeperexport.DecodeUsageBatch(data)
		return err
	}
}

func categoryForPath(path string, data []byte) keeperexport.MetadataCategory {
	base := path
	if idx := strings.LastIndexByte(base, '/'); idx >= 0 {
		base = base[idx+1:]
	}
	switch {
	case strings.Contains(base, "api-keys"):
		return keeperexport.CategoryAPIKeys
	case strings.Contains(base, "provider-identities"):
		return keeperexport.CategoryProviderIdentities
	case strings.Contains(base, "auth-files"), strings.Contains(base, "empty-complete"), strings.Contains(base, "incomplete"), strings.Contains(base, "duplicate"), strings.Contains(base, "revision"):
		return keeperexport.CategoryAuthFiles
	}
	// Shape sniffing for non-conventional file names.
	var envelope struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(data, &envelope); err == nil && len(envelope.Items) > 0 {
		item := envelope.Items[0]
		if _, ok := item["fingerprint"]; ok {
			return keeperexport.CategoryAPIKeys
		}
		if _, ok := item["providerType"]; ok {
			return keeperexport.CategoryProviderIdentities
		}
	}
	return keeperexport.CategoryAuthFiles
}
