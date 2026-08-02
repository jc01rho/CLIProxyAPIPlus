package claude

import (
	"reflect"
	"sync"
	"testing"
)

func TestGenerateDeviceIDPool(t *testing.T) {
	deviceIDs, errGenerate := GenerateDeviceIDPool()
	if errGenerate != nil {
		t.Fatalf("GenerateDeviceIDPool() error = %v", errGenerate)
	}
	if len(deviceIDs) != ClaudeDevicePoolSize {
		t.Fatalf("device pool length = %d, want %d", len(deviceIDs), ClaudeDevicePoolSize)
	}
	seen := make(map[string]struct{}, len(deviceIDs))
	for _, deviceID := range deviceIDs {
		if !ValidDeviceID(deviceID) {
			t.Fatalf("device ID = %q, want 64 lowercase hex", deviceID)
		}
		if _, exists := seen[deviceID]; exists {
			t.Fatalf("duplicate device ID %q", deviceID)
		}
		seen[deviceID] = struct{}{}
	}
}

func TestEnsureDeviceIDPoolRepairsAndStabilizesCredentialMetadata(t *testing.T) {
	const first = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	metadata := map[string]any{
		ClaudeDeviceIDsMetadataKey: []any{
			first,
			first,
			"INVALID",
		},
	}

	deviceIDs, changed, errEnsure := EnsureDeviceIDPool(metadata)
	if errEnsure != nil {
		t.Fatalf("EnsureDeviceIDPool() error = %v", errEnsure)
	}
	if !changed {
		t.Fatal("EnsureDeviceIDPool() changed = false, want true")
	}
	if len(deviceIDs) != ClaudeDevicePoolSize || deviceIDs[0] != first {
		t.Fatalf("device IDs = %#v, want repaired single-entry pool preserving first", deviceIDs)
	}

	second, changedAgain, errEnsureAgain := EnsureDeviceIDPool(metadata)
	if errEnsureAgain != nil {
		t.Fatalf("EnsureDeviceIDPool() second error = %v", errEnsureAgain)
	}
	if changedAgain {
		t.Fatal("EnsureDeviceIDPool() second changed = true, want stable canonical pool")
	}
	if !reflect.DeepEqual(second, deviceIDs) {
		t.Fatalf("second device IDs = %#v, want %#v", second, deviceIDs)
	}

	second[0] = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	stored := metadata[ClaudeDeviceIDsMetadataKey].([]string)
	if stored[0] != first {
		t.Fatal("returned pool aliases credential metadata")
	}
}

func TestEnsureDeviceIDPoolCanonicalizesSingleDevice(t *testing.T) {
	const canonical = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	metadata := map[string]any{ClaudeDeviceIDsMetadataKey: []any{"  AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA  "}}
	deviceIDs, changed, errEnsure := EnsureDeviceIDPool(metadata)
	if errEnsure != nil {
		t.Fatalf("EnsureDeviceIDPool() error = %v", errEnsure)
	}
	if !changed || len(deviceIDs) != 1 || deviceIDs[0] != canonical {
		t.Fatalf("EnsureDeviceIDPool() = %#v, changed=%v; want canonical single device", deviceIDs, changed)
	}
	if !HasCanonicalDeviceIDPool(metadata[ClaudeDeviceIDsMetadataKey]) {
		t.Fatalf("stored device pool = %#v, want canonical", metadata[ClaudeDeviceIDsMetadataKey])
	}
}

func TestEnsureDeviceIDPoolMigratesFiveSlotsToOne(t *testing.T) {
	metadata := map[string]any{ClaudeDeviceIDsMetadataKey: []string{
		"0000000000000000000000000000000000000000000000000000000000000000",
		"1111111111111111111111111111111111111111111111111111111111111111",
		"2222222222222222222222222222222222222222222222222222222222222222",
		"3333333333333333333333333333333333333333333333333333333333333333",
		"4444444444444444444444444444444444444444444444444444444444444444",
	}}

	deviceIDs, changed, errEnsure := EnsureDeviceIDPool(metadata)
	if errEnsure != nil {
		t.Fatalf("EnsureDeviceIDPool() error = %v", errEnsure)
	}
	if !changed {
		t.Fatal("EnsureDeviceIDPool() changed = false, want five-slot migration")
	}
	want := []string{"0000000000000000000000000000000000000000000000000000000000000000"}
	if !reflect.DeepEqual(deviceIDs, want) {
		t.Fatalf("device IDs = %#v, want %#v", deviceIDs, want)
	}
	if stored, ok := metadata[ClaudeDeviceIDsMetadataKey].([]string); !ok || !reflect.DeepEqual(stored, want) {
		t.Fatalf("stored device IDs = %#v, want %#v", metadata[ClaudeDeviceIDsMetadataKey], want)
	}
}

func TestEnsureDeviceIDPoolConcurrentInitialization(t *testing.T) {
	metadata := make(map[string]any)
	const workers = 20
	results := make(chan []string, workers)
	errors := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Go(func() {
			deviceIDs, _, errEnsure := EnsureDeviceIDPool(metadata)
			results <- deviceIDs
			errors <- errEnsure
		})
	}
	group.Wait()
	close(results)
	close(errors)

	for errEnsure := range errors {
		if errEnsure != nil {
			t.Fatalf("EnsureDeviceIDPool() concurrent error = %v", errEnsure)
		}
	}
	stored := NormalizeDeviceIDPool(metadata[ClaudeDeviceIDsMetadataKey])
	if len(stored) != ClaudeDevicePoolSize {
		t.Fatalf("stored device pool length = %d, want %d", len(stored), ClaudeDevicePoolSize)
	}
	for result := range results {
		if !reflect.DeepEqual(result, stored) {
			t.Fatalf("concurrent result = %#v, want %#v", result, stored)
		}
	}
}

func TestSelectDeviceIDUsesOneDeviceAcrossSessions(t *testing.T) {
	deviceIDs := []string{
		"0000000000000000000000000000000000000000000000000000000000000000",
	}

	first, errFirst := SelectDeviceID(deviceIDs, "11111111-2222-4333-8444-555555555555")
	if errFirst != nil {
		t.Fatalf("SelectDeviceID() error = %v", errFirst)
	}
	second, errSecond := SelectDeviceID(deviceIDs, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")
	if errSecond != nil {
		t.Fatalf("SelectDeviceID() second error = %v", errSecond)
	}
	if first != second || first != deviceIDs[0] {
		t.Fatalf("single device selection = %q then %q, want %q", first, second, deviceIDs[0])
	}
}
