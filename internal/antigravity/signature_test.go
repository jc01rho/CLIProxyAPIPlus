package antigravity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/cespare/xxhash/v2"
)

// Expected values below are derived from the constants documented in
// cortexkit/anthropic-auth packages/core/src/cch.ts (CCH_SEED, CCH_SALT,
// CCH_POSITIONS) and computed independently of the code under test.
func TestCCHSeedMatchesReference(t *testing.T) {
	const wantSeed uint64 = 0x4d659218e32a3268
	if CCHSeed != wantSeed {
		t.Fatalf("CCHSeed = %#x, want %#x", CCHSeed, wantSeed)
	}
}

func TestComputeCCHUsesReferenceSeed(t *testing.T) {
	const body = `{"model":"","messages":[]}`
	hasher := xxhash.NewWithSeed(0x4d659218e32a3268)
	if _, err := hasher.Write([]byte(body)); err != nil {
		t.Fatalf("hash reference body: %v", err)
	}
	want := fmt.Sprintf("%05x", hasher.Sum64()&0xfffff)
	if got := ComputeCCH([]byte(body)); got != want {
		t.Fatalf("ComputeCCH = %q, want %q", got, want)
	}
}

func TestComputeVersionSuffixSamplesFirstUserText(t *testing.T) {
	tests := []struct {
		name    string
		first   string
		version string
	}{
		{"reference sample positions", "abcdefghijklmnopqrstu", "2.1.302"},
		{"short text pads with zero", "ab", "2.1.302"},
		{"empty text pads with zero", "", "2.1.302"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			want := referenceSuffix(test.first, test.version)
			if got := ComputeVersionSuffix(test.first, test.version); got != want {
				t.Fatalf("ComputeVersionSuffix(%q, %q) = %q, want %q", test.first, test.version, got, want)
			}
		})
	}
}

func TestComputeVersionSuffixEmptyPromptHasPinnedValue(t *testing.T) {
	if got := ComputeVersionSuffix("", "2.1.302"); got != "ea8" {
		t.Fatalf("ComputeVersionSuffix(\"\", \"2.1.302\") = %q, want %q", got, "ea8")
	}
}

func TestBuildDefaultBillingHeaderValueUsesReferenceVersion(t *testing.T) {
	got := BuildDefaultBillingHeaderValue("abcdefghijklmnopqrstu")
	want := "x-anthropic-billing-header: cc_version=2.1.302." +
		referenceSuffix("abcdefghijklmnopqrstu", "2.1.302") +
		"; cc_entrypoint=cli; cch=00000;"
	if got != want {
		t.Fatalf("BuildDefaultBillingHeaderValue = %q, want %q", got, want)
	}
}

// referenceSuffix is a second implementation of computeCcVersionSuffix over
// ASCII input, so a bug shared with the code under test cannot hide here.
func referenceSuffix(firstUserText, version string) string {
	sampled := make([]byte, 0, 3)
	for _, position := range []int{4, 7, 20} {
		if position < len(firstUserText) {
			sampled = append(sampled, firstUserText[position])
			continue
		}
		sampled = append(sampled, '0')
	}
	hash := sha256.Sum256([]byte("59cf53e54c78" + string(sampled) + version))
	return hex.EncodeToString(hash[:])[:3]
}
