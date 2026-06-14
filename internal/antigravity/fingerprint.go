package antigravity

import (
	"crypto/rand"
	"encoding/hex"
	"runtime"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	// AgyCLIVersion is the captured agy CLI version used for content-request identity.
	AgyCLIVersion = "1.0.4"

	antigravityAPIClient = "antigravity-cli"

	// MaxFingerprintHistory is the maximum number of fingerprint versions to keep per account.
	MaxFingerprintHistory = 5
)

// ClientMetadata contains IDE metadata stored with a generated fingerprint.
type ClientMetadata struct {
	IDEType    string `json:"ideType"`
	Platform   string `json:"platform"`
	PluginType string `json:"pluginType"`
}

// Fingerprint is the per-account content-request identity snapshot.
type Fingerprint struct {
	DeviceID       string         `json:"deviceId"`
	SessionToken   string         `json:"sessionToken"`
	UserAgent      string         `json:"userAgent"`
	APIClient      string         `json:"apiClient"`
	ClientMetadata ClientMetadata `json:"clientMetadata"`
	CreatedAt      int64          `json:"createdAt"`
}

// FingerprintVersion stores a fingerprint snapshot and why it was saved.
type FingerprintVersion struct {
	Fingerprint Fingerprint `json:"fingerprint"`
	Timestamp   int64       `json:"timestamp"`
	Reason      string      `json:"reason"`
}

// FingerprintHeaders contains request headers derived from a fingerprint.
type FingerprintHeaders struct {
	UserAgent string `json:"User-Agent"`
}

func normalizeHarnessPlatform(platform string) string {
	if platform == "" {
		platform = nodePlatform()
	}
	if platform == "win32" || platform == "windows" {
		return "windows"
	}
	return platform
}

func normalizeHarnessArch(arch string) string {
	if arch == "" {
		arch = nodeArch()
	}
	switch arch {
	case "x64", "amd64":
		return "amd64"
	case "ia32", "386":
		return "386"
	default:
		return arch
	}
}

// BuildAntigravityHarnessPlatformArch returns the normalized agy CLI platform/arch tuple.
func BuildAntigravityHarnessPlatformArch(platform, arch string) string {
	return normalizeHarnessPlatform(platform) + "/" + normalizeHarnessArch(arch)
}

// BuildAntigravityHarnessUserAgent returns the captured short agy CLI User-Agent.
func BuildAntigravityHarnessUserAgent(version, platform, arch string) string {
	if version == "" {
		version = AgyCLIVersion
	}
	return "antigravity/cli/" + version + " " + BuildAntigravityHarnessPlatformArch(platform, arch)
}

// BuildAntigravityHarnessLoadCodeAssistUserAgent returns the loadCodeAssist agy CLI User-Agent.
func BuildAntigravityHarnessLoadCodeAssistUserAgent(version string) string {
	return BuildAntigravityHarnessUserAgent(version, "", "")
}

func platformToMetadataPlatform(platform string) string {
	if platform == "" {
		if runtime.GOOS == "windows" {
			platform = "win32"
		} else {
			platform = runtime.GOOS
		}
	}
	if platform == "win32" || platform == "windows" {
		return "WINDOWS"
	}
	return "MACOS"
}

// BuildAntigravityLoadCodeAssistMetadata returns metadata used by bootstrap requests.
func BuildAntigravityLoadCodeAssistMetadata() map[string]string {
	return map[string]string{"ideType": "ANTIGRAVITY"}
}

// BuildAntigravityHarnessBootstrapHeaders returns the raw agy CLI bootstrap headers.
func BuildAntigravityHarnessBootstrapHeaders(accessToken string) map[string]string {
	return map[string]string{
		"User-Agent":      BuildAntigravityHarnessLoadCodeAssistUserAgent(""),
		"Authorization":   "Bearer " + accessToken,
		"Content-Type":    "application/json",
		"Accept-Encoding": "gzip",
	}
}

func generateDeviceID() string {
	return uuid.NewString()
}

func generateSessionToken() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	return hex.EncodeToString(buf)
}

// GenerateFingerprint generates the per-account content-request fingerprint.
func GenerateFingerprint() Fingerprint {
	return Fingerprint{
		DeviceID:     generateDeviceID(),
		SessionToken: generateSessionToken(),
		UserAgent:    BuildAntigravityHarnessUserAgent("", "", ""),
		APIClient:    antigravityAPIClient,
		ClientMetadata: ClientMetadata{
			IDEType:    "ANTIGRAVITY",
			Platform:   platformToMetadataPlatform(""),
			PluginType: "GEMINI",
		},
		CreatedAt: time.Now().UnixMilli(),
	}
}

// CollectCurrentFingerprint collects the current content-request fingerprint.
func CollectCurrentFingerprint() Fingerprint {
	return GenerateFingerprint()
}

// UpdateFingerprintVersion updates a saved fingerprint to the current agy CLI User-Agent.
func UpdateFingerprintVersion(fingerprint *Fingerprint) bool {
	if fingerprint == nil {
		return false
	}
	userAgent := BuildAntigravityHarnessUserAgent("", "", "")
	if fingerprint.UserAgent == userAgent {
		return false
	}
	fingerprint.UserAgent = userAgent
	return true
}

// BuildFingerprintHeaders builds HTTP headers from a fingerprint object.
func BuildFingerprintHeaders(fingerprint *Fingerprint) map[string]string {
	if fingerprint == nil {
		return map[string]string{}
	}
	return map[string]string{"User-Agent": fingerprint.UserAgent}
}

var (
	sessionFingerprintMu sync.Mutex
	sessionFingerprint   *Fingerprint
)

// GetSessionFingerprint returns the process-level session fingerprint.
func GetSessionFingerprint() Fingerprint {
	sessionFingerprintMu.Lock()
	defer sessionFingerprintMu.Unlock()
	if sessionFingerprint == nil {
		fingerprint := GenerateFingerprint()
		sessionFingerprint = &fingerprint
	}
	return *sessionFingerprint
}

// RegenerateSessionFingerprint replaces and returns the process-level session fingerprint.
func RegenerateSessionFingerprint() Fingerprint {
	sessionFingerprintMu.Lock()
	defer sessionFingerprintMu.Unlock()
	fingerprint := GenerateFingerprint()
	sessionFingerprint = &fingerprint
	return fingerprint
}
