package executor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	xxHash64 "github.com/pierrec/xxHash/xxHash64"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// claudeOAuthWireVectorsSHA256 pins testdata/claude_oauth_wire/vectors.json,
// vendored byte for byte from cortexkit/anthropic-auth b9a8bd928
// (packages/opencode/src/tests/fixtures/claude-oauth-wire).
const claudeOAuthWireVectorsSHA256 = "47072a18eb3640dd03502a4f24e7e9b5325160faffacdf47cb99db6462cb4ba5"

type claudeOAuthWireVectors struct {
	Fixed struct {
		AccessToken         string `json:"accessToken"`
		AccountUUID         string `json:"accountUuid"`
		DeviceID            string `json:"deviceId"`
		ClaudeCodeSessionID string `json:"claudeCodeSessionId"`
	} `json:"fixed"`
	Primitives map[string]string `json:"primitives"`
	Cases      []struct {
		ID    string `json:"id"`
		Input struct {
			HostHeaders map[string]string `json:"host_headers"`
		} `json:"input"`
		Output struct {
			HeadersWireOrder [][2]string `json:"headers_wire_order"`
			Body             string      `json:"body"`
			BillingHeader    string      `json:"billing_header"`
			CcVersionSuffix  struct {
				FirstUserText string `json:"first_user_text"`
				Suffix        string `json:"suffix"`
			} `json:"cc_version_suffix"`
			CCH              string `json:"cch"`
			CCHCanonicalBody string `json:"cch_canonical_body"`
		} `json:"output"`
	} `json:"cases"`
}

func loadClaudeOAuthWireVectors(t *testing.T) claudeOAuthWireVectors {
	t.Helper()
	raw, errRead := os.ReadFile(filepath.Join("testdata", "claude_oauth_wire", "vectors.json"))
	if errRead != nil {
		t.Fatalf("read vectors: %v", errRead)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != claudeOAuthWireVectorsSHA256 {
		t.Fatalf("vectors.json sha256 = %s, want %s", got, claudeOAuthWireVectorsSHA256)
	}
	var vectors claudeOAuthWireVectors
	if errUnmarshal := json.Unmarshal(raw, &vectors); errUnmarshal != nil {
		t.Fatalf("decode vectors: %v", errUnmarshal)
	}
	if len(vectors.Cases) == 0 {
		t.Fatal("vectors.json has no cases")
	}
	return vectors
}

func claudeWireVectorHeader(pairs [][2]string, name string) string {
	for _, pair := range pairs {
		if strings.EqualFold(pair[0], name) {
			return pair[1]
		}
	}
	return ""
}

var claudeWireVectorCCH = regexp.MustCompile(`cch=[0-9a-f]{5};`)

func TestClaudeOAuthWireVectors_FingerprintAndBilling(t *testing.T) {
	vectors := loadClaudeOAuthWireVectors(t)
	if got := computeFingerprint("short", "2.1.258"); got != vectors.Primitives["computeCcVersionSuffix_short_2_1_258"] {
		t.Fatalf("computeFingerprint(short, 2.1.258) = %q, want %q", got, vectors.Primitives["computeCcVersionSuffix_short_2_1_258"])
	}
	for _, tc := range vectors.Cases {
		t.Run(tc.ID, func(t *testing.T) {
			out := tc.Output
			if got := computeFingerprint(out.CcVersionSuffix.FirstUserText, helps.DefaultClaudeCodeVersion); got != out.CcVersionSuffix.Suffix {
				t.Fatalf("fingerprint = %q, want %q", got, out.CcVersionSuffix.Suffix)
			}
			body := []byte(out.Body)
			prevReq, promptID := helps.ExtractClaudeBillingTags(body)
			turnOrigin := ""
			if strings.Contains(out.BillingHeader, "cc_turn_origin=human;") {
				turnOrigin = "human"
			}
			got := generateBillingHeader(true, helps.DefaultClaudeCodeVersion, out.CcVersionSuffix.FirstUserText, "cli", "", false, prevReq, promptID, claudeCountUserPrompts(body), turnOrigin)
			want := claudeWireVectorCCH.ReplaceAllString(out.BillingHeader, "cch=00000;")
			if got != want {
				t.Fatalf("billing header\n got %q\nwant %q", got, want)
			}
		})
	}
}

func TestClaudeOAuthWireVectors_MetadataUserIDFieldOrder(t *testing.T) {
	vectors := loadClaudeOAuthWireVectors(t)
	got := helps.BuildClaudeCodeMetadataUserID(&helps.ClaudeCodeIdentity{
		DeviceID:    vectors.Fixed.DeviceID,
		AccountUUID: vectors.Fixed.AccountUUID,
		SessionID:   vectors.Fixed.ClaudeCodeSessionID,
	})
	for _, tc := range vectors.Cases {
		if want := gjson.Get(tc.Output.Body, "metadata.user_id").String(); got != want {
			t.Fatalf("%s: metadata.user_id = %s, want %s", tc.ID, got, want)
		}
	}
}

// Every vector's canonical body hashes to its cch with our xxHash64 seed and
// mask. Our signer reproduces the full cch except where the body carries
// fallbacks: CPA leaves fallbacks out of the hashed view (measured on 2.1.220),
// cortexkit hashes them. That difference is kept on purpose and listed in
// docs/anthropic-oauth-migration-plan_KO.md.
func TestClaudeOAuthWireVectors_CCHSignature(t *testing.T) {
	vectors := loadClaudeOAuthWireVectors(t)
	for _, tc := range vectors.Cases {
		t.Run(tc.ID, func(t *testing.T) {
			hasher := xxHash64.New(claudeCCHSeed)
			if _, errWrite := hasher.Write([]byte(tc.Output.CCHCanonicalBody)); errWrite != nil {
				t.Fatal(errWrite)
			}
			if got := fmt.Sprintf("%05x", hasher.Sum64()&0xFFFFF); got != tc.Output.CCH {
				t.Fatalf("hash(cch_canonical_body) = %s, want %s", got, tc.Output.CCH)
			}
			body := []byte(claudeWireVectorCCH.ReplaceAllString(tc.Output.Body, "cch=00000;"))
			signed, errSign := signAnthropicMessagesBody(body)
			if errSign != nil {
				t.Fatalf("sign: %v", errSign)
			}
			signedCCH := regexp.MustCompile(`cch=([0-9a-f]{5});`).FindStringSubmatch(string(signed))
			if len(signedCCH) != 2 {
				t.Fatalf("signed body has no cch: %s", signed)
			}
			if sameView := !gjson.GetBytes(body, "fallbacks").Exists(); sameView != (signedCCH[1] == tc.Output.CCH) {
				t.Fatalf("signed cch = %s, vector %s, fallbacks present = %t", signedCCH[1], tc.Output.CCH, !sameView)
			}
		})
	}
}

// Feeding each vector's final body through the header builder must reproduce
// the vector's identity headers and beta list. Excluded on purpose: fast mode,
// which CPA never enables for proxied requests; betas the OpenCode host itself
// appended (fine-grained-tool-streaming), which CPA forwards only when its own
// caller sends them; and the server-side-fallback date: CPA sends the array
// fallbacks body measured with server-side-fallback-2026-06-01, the vectors send
// fallbacks:"default" with server-side-fallback-2026-07-01.
func TestClaudeOAuthWireVectors_Headers(t *testing.T) {
	vectors := loadClaudeOAuthWireVectors(t)
	for _, tc := range vectors.Cases {
		t.Run(tc.ID, func(t *testing.T) {
			wire := tc.Output.HeadersWireOrder
			auth := &cliproxyauth.Auth{ID: "wire-vector", Provider: "claude", Metadata: map[string]any{"access_token": vectors.Fixed.AccessToken}}
			req, errReq := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages?beta=true", bytes.NewReader([]byte(tc.Output.Body)))
			if errReq != nil {
				t.Fatal(errReq)
			}
			if errHeaders := applyClaudeHeaders(req, auth, vectors.Fixed.AccessToken, true, nil, []byte(tc.Output.Body), &config.Config{}, http.Header{}, false, vectors.Fixed.ClaudeCodeSessionID); errHeaders != nil {
				t.Fatalf("applyClaudeHeaders: %v", errHeaders)
			}
			for _, name := range []string{
				"Accept", "Content-Type", "User-Agent", "anthropic-dangerous-direct-browser-access", "anthropic-version",
				"x-app", "x-claude-code-prompt-id", "x-claude-code-request-class", "x-claude-code-session-id",
				"x-stainless-arch", "x-stainless-lang", "x-stainless-os", "x-stainless-package-version",
				"x-stainless-retry-count", "x-stainless-runtime", "x-stainless-runtime-version", "x-stainless-timeout",
			} {
				if got, want := req.Header.Get(name), claudeWireVectorHeader(wire, name); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
			if got := req.Header.Get("x-client-request-id"); !uuidRe.MatchString(got) {
				t.Errorf("x-client-request-id = %q, want a UUID", got)
			}
			normalize := func(betas string) string {
				for _, beta := range []string{"fine-grained-tool-streaming-2025-05-14", claudeFastModeBeta} {
					betas = withoutClaudeBeta(betas, beta)
				}
				return strings.ReplaceAll(betas, "server-side-fallback-2026-07-01", claudeServerSideFallbackBeta)
			}
			if got, want := normalize(req.Header.Get("Anthropic-Beta")), normalize(claudeWireVectorHeader(wire, "anthropic-beta")); got != want {
				t.Errorf("anthropic-beta\n got %s\nwant %s", got, want)
			}
		})
	}
}
