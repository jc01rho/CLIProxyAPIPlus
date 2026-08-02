package helps

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

const (
	claudeDiagnosticsTTL           = time.Hour
	claudeDiagnosticsCleanupPeriod = 15 * time.Minute
)

type claudeDiagnosticsEntry struct {
	previousMessageID string
	nextSequence      uint64
	committedSequence uint64
	expiresAt         time.Time
}

var claudeDiagnosticsState = struct {
	sync.Mutex
	entries     map[string]claudeDiagnosticsEntry
	lastCleanup time.Time
}{entries: make(map[string]claudeDiagnosticsEntry)}

// BeginClaudeDiagnostics starts one request generation for a stable credential
// identity and Claude conversation. It returns the last successfully completed
// upstream message ID, if any. Only a SHA-256 digest of the credential identity
// and session is retained as the cache key, so access-token rotation does not
// interrupt continuity.
func BeginClaudeDiagnostics(credentialIdentity, sessionID string) (key string, sequence uint64, previousMessageID string) {
	credentialIdentity = strings.TrimSpace(credentialIdentity)
	sessionID = strings.TrimSpace(sessionID)
	if credentialIdentity == "" || sessionID == "" {
		return "", 0, ""
	}
	digest := sha256.Sum256([]byte(credentialIdentity + "\x00" + sessionID))
	key = hex.EncodeToString(digest[:])
	now := time.Now()

	claudeDiagnosticsState.Lock()
	defer claudeDiagnosticsState.Unlock()
	if claudeDiagnosticsState.lastCleanup.IsZero() || now.Sub(claudeDiagnosticsState.lastCleanup) >= claudeDiagnosticsCleanupPeriod {
		for candidateKey, candidate := range claudeDiagnosticsState.entries {
			if !candidate.expiresAt.IsZero() && now.After(candidate.expiresAt) {
				delete(claudeDiagnosticsState.entries, candidateKey)
			}
		}
		claudeDiagnosticsState.lastCleanup = now
	}
	entry := claudeDiagnosticsState.entries[key]
	if !entry.expiresAt.IsZero() && now.After(entry.expiresAt) {
		entry = claudeDiagnosticsEntry{}
	}
	entry.nextSequence++
	entry.expiresAt = now.Add(claudeDiagnosticsTTL)
	claudeDiagnosticsState.entries[key] = entry
	return key, entry.nextSequence, entry.previousMessageID
}

// CommitClaudeDiagnostics advances continuity only after a response completes.
// A response from an older concurrently-started request cannot overwrite a
// newer committed generation.
func CommitClaudeDiagnostics(key string, sequence uint64, messageID string) {
	key = strings.TrimSpace(key)
	messageID = strings.TrimSpace(messageID)
	if key == "" || sequence == 0 || messageID == "" {
		return
	}
	now := time.Now()

	claudeDiagnosticsState.Lock()
	defer claudeDiagnosticsState.Unlock()
	entry, ok := claudeDiagnosticsState.entries[key]
	if !ok || sequence < entry.committedSequence {
		return
	}
	entry.previousMessageID = messageID
	entry.committedSequence = sequence
	entry.expiresAt = now.Add(claudeDiagnosticsTTL)
	claudeDiagnosticsState.entries[key] = entry
}

func resetClaudeDiagnosticsForTest() {
	claudeDiagnosticsState.Lock()
	defer claudeDiagnosticsState.Unlock()
	claudeDiagnosticsState.entries = make(map[string]claudeDiagnosticsEntry)
	claudeDiagnosticsState.lastCleanup = time.Time{}
}
