package executor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"github.com/google/uuid"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const kiroLineageKey = "kiro_credential_lineage"

var errKiroCredentialReplaced = errors.New("kiro: credential lineage changed; discard stale refresh and reload the auth file")

func (e *KiroExecutor) kiroAuthPath(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if path := strings.TrimSpace(auth.Attributes["path"]); path != "" {
		return path
	}
	if filepath.IsAbs(auth.FileName) {
		return auth.FileName
	}
	if auth.FileName != "" && e.cfg != nil && e.cfg.AuthDir != "" {
		return filepath.Join(e.cfg.AuthDir, auth.FileName)
	}
	return ""
}

func readKiroMetadata(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("kiro: read credential: %w", err)
	}
	var metadata map[string]any
	if err = json.Unmarshal(raw, &metadata); err != nil {
		return nil, fmt.Errorf("kiro: decode credential: %w", err)
	}
	if metadata == nil {
		return nil, errors.New("kiro: credential must be a JSON object")
	}
	normalizeKiroAuthMetadata(metadata)
	return metadata, nil
}

func kiroCredentialFingerprint(metadata map[string]any) [32]byte {
	// The lineage is random and durable, while this snapshot catches replacement
	// by an external writer that copied (or omitted) the lineage metadata.
	hash := sha256.New()
	for _, key := range []string{"access_token", "refresh_token", "client_id", "client_secret"} {
		value, _ := metadata[key].(string)
		fmt.Fprintf(hash, "%d:%s", len(value), value)
	}
	return [32]byte(hash.Sum(nil))
}

// Ported from kiro-lb src/auth.rs and src/store.rs (1581af9). The OS lease is
// released by process exit, and never permits refreshing without ownership.
func (e *KiroExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (updated *cliproxyauth.Auth, err error) {
	if auth == nil {
		return nil, errors.New("kiro: auth is nil")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	path := e.kiroAuthPath(auth)
	if path == "" {
		return e.refreshKiroUnlocked(ctx, auth.Clone())
	}
	lease, err := acquireKiroRefreshLease(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, lease.Close())
		if err != nil {
			updated = nil
		}
	}()
	metadata, err := readKiroMetadata(path)
	if err != nil {
		return nil, err
	}
	expectedLineage, _ := auth.Metadata[kiroLineageKey].(string)
	lineage, _ := metadata[kiroLineageKey].(string)
	if expectedLineage != "" && expectedLineage != lineage {
		return nil, errKiroCredentialReplaced
	}
	if lineage == "" {
		lineage = "lineage:" + uuid.NewString()
		metadata[kiroLineageKey] = lineage
	}
	fileAuth := auth.Clone()
	fileAuth.Metadata = metadata
	metadata["kiro_machine_id"] = ensureKiroMachineID(fileAuth)
	// Persist lineage before the network operation, so all processes observe it.
	if err = writeKiroMetadata(path, metadata); err != nil {
		return nil, err
	}
	expected := kiroCredentialFingerprint(metadata)
	// Reload under the lease before even the first token request. A process that
	// lost the refresh race must use the winner's rotated refresh token.
	updated, err = e.refreshKiroUnlocked(ctx, fileAuth)
	if err != nil {
		return nil, err
	}
	current, err := readKiroMetadata(path)
	if err != nil {
		return nil, err
	}
	if current[kiroLineageKey] != lineage || kiroCredentialFingerprint(current) != expected {
		return nil, errKiroCredentialReplaced
	}
	// Retain concurrent noncredential metadata rather than replacing the document.
	for key, value := range updated.Metadata {
		if original, present := metadata[key]; !present || !reflect.DeepEqual(original, value) {
			current[key] = value
		}
	}
	current[kiroLineageKey] = lineage
	if err = writeKiroMetadata(path, current); err != nil {
		return nil, err
	}
	updated.Metadata = current
	// The manager persists Refresh's return value again. Its existing Storage
	// hook must also compare lineage, otherwise that later save can undo this CAS.
	storage := &kiroLineageStorage{lineage: lineage, fingerprint: kiroCredentialFingerprint(current)}
	storage.SetMetadata(current)
	updated.Storage = storage
	return updated, nil
}

// The adapter also protects a later manager save without modifying shared auth
// code. It merges management metadata only when the credential snapshot matches.
type kiroLineageStorage struct {
	mu          sync.Mutex
	lineage     string
	fingerprint [32]byte
	metadata    map[string]any
}

func (s *kiroLineageStorage) SetMetadata(metadata map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metadata = make(map[string]any, len(metadata))
	for key, value := range metadata {
		s.metadata[key] = value
	}
}

func (s *kiroLineageStorage) SaveTokenToFile(path string) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lease, err := acquireKiroRefreshLease(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	current, err := readKiroMetadata(path)
	if err != nil {
		return err
	}
	if current[kiroLineageKey] != s.lineage || kiroCredentialFingerprint(current) != s.fingerprint {
		return errKiroCredentialReplaced
	}
	for key, value := range s.metadata {
		current[key] = value
	}
	return writeKiroMetadata(path, current)
}

func writeKiroMetadata(path string, metadata map[string]any) (err error) {
	raw, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("kiro: encode credential: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".kiro-credential-*")
	if err != nil {
		return fmt.Errorf("kiro: create credential temp: %w", err)
	}
	name := tmp.Name()
	defer func() {
		if removeErr := os.Remove(name); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			log.Warnf("kiro: remove credential temp: %v", removeErr)
		}
	}()
	_, writeErr := tmp.Write(raw)
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("kiro: persist credential: %w", err)
	}
	if err = os.Rename(name, path); err != nil {
		return fmt.Errorf("kiro: replace credential: %w", err)
	}
	return nil
}
