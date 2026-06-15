package antigravity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/cespare/xxhash/v2"
)

// CCH constants from anthropic-auth reference
const (
	// CCHSeed is the fixed seed used by Claude Code for xxHash64
	CCHSeed uint64 = 0x6e52736ac806831e

	// CCHPlaceholder is the placeholder value used in billing header before signing
	CCHPlaceholder = "cch=00000;"

	// BillingHeaderCCHPattern matches the cch value in a billing header within system message
	// Example: "x-anthropic-billing-header: cc_version=...; cc_entrypoint=...; cch=abcde;"
	billingHeaderCCHPattern = `(?i)(x-anthropic-billing-header: cc_version=[^;"]+; cc_entrypoint=[^;"]+; )cch=([0-9a-f]{5});`

	// BillingHeaderCCHPlaceholderPattern matches the placeholder cch=00000; in billing header
	billingHeaderCCHPlaceholderPattern = `(?i)(x-anthropic-billing-header: cc_version=[^;"]+; cc_entrypoint=[^;"]+; )cch=00000;`

	// DefaultClaudeCodeVersion is the version used by Claude Code
	DefaultClaudeCodeVersion = "2.1.141"

	// DefaultClaudeCodeBuildHash is the build hash for the default version
	DefaultClaudeCodeBuildHash = "67b"

	// DefaultClaudeCodeEntrypoint is the entrypoint used by Claude Code
	DefaultClaudeCodeEntrypoint = "sdk-cli"
)

var (
	billingHeaderCCHRe          = regexp.MustCompile(billingHeaderCCHPattern)
	billingHeaderCCHPlaceholderRe = regexp.MustCompile(billingHeaderCCHPlaceholderPattern)
)

// ComputeCCH computes the Claude Code CCH token over the final serialized request body.
// It uses xxHash64 with a fixed seed, masks to 20 bits, and returns a 5-character hex string.
//
// Reference: cortexkit/anthropic-auth/packages/core/src/cch.ts computeCCH
func ComputeCCH(bodyBytes []byte) string {
	h := xxhash.NewWithSeed(CCHSeed)
	h.Write(bodyBytes)
	hash := h.Sum64()
	// Mask to 20 bits (0xfffff)
	masked := hash & 0xfffff
	// Format as 5-character hex, zero-padded
	return fmt.Sprintf("%05x", masked)
}

// ResetBillingHeaderCCH replaces the actual CCH value in a billing header with the placeholder.
// This is used to prepare the body for signing.
func ResetBillingHeaderCCH(bodyString string) string {
	return billingHeaderCCHRe.ReplaceAllString(bodyString, "${1}"+CCHPlaceholder)
}

// ExtractBillingHeaderCCH extracts the CCH value from a billing header in the body string.
// Returns empty string if not found.
func ExtractBillingHeaderCCH(bodyString string) string {
	matches := billingHeaderCCHRe.FindStringSubmatch(bodyString)
	if len(matches) >= 3 {
		return matches[2]
	}
	return ""
}

// SignRequestBody signs the request body by computing the CCH value and replacing
// the placeholder in the billing header.
// Returns the signed body string, or the original if no billing header placeholder is found.
func SignRequestBody(bodyString string) string {
	// Check if there's a billing header with placeholder
	if !strings.Contains(bodyString, CCHPlaceholder) {
		// Try to find actual CCH pattern - if found, reset it first
		if billingHeaderCCHRe.MatchString(bodyString) {
			bodyString = ResetBillingHeaderCCH(bodyString)
		} else {
			return bodyString
		}
	}

	// Compute CCH over the body with placeholder
	unsignedBodyString := ResetBillingHeaderCCH(bodyString)
	token := ComputeCCH([]byte(unsignedBodyString))

	// Replace placeholder with actual token
	return billingHeaderCCHPlaceholderRe.ReplaceAllString(unsignedBodyString, "${1}cch="+token+";")
}

// ComputeVersionSuffix computes a stable 3-character suffix for cc_version.
// For the default version, it returns the build hash.
// For other versions, it computes a deterministic SHA256-based suffix.
func ComputeVersionSuffix(version string) string {
	if version == DefaultClaudeCodeVersion {
		return DefaultClaudeCodeBuildHash
	}
	// Compute SHA256 hash of version and take first 3 hex characters
	hash := sha256.Sum256([]byte(version))
	return hex.EncodeToString(hash[:])[:3]
}

// BuildBillingHeaderValue builds the billing header value with a CCH placeholder.
// The placeholder must be replaced by SignRequestBody() after final serialization.
func BuildBillingHeaderValue(version, entrypoint string) string {
	suffix := ComputeVersionSuffix(version)
	return fmt.Sprintf("x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=%s; %s",
		version, suffix, entrypoint, CCHPlaceholder)
}

// BuildDefaultBillingHeaderValue builds the billing header with default values.
func BuildDefaultBillingHeaderValue() string {
	return BuildBillingHeaderValue(DefaultClaudeCodeVersion, DefaultClaudeCodeEntrypoint)
}
