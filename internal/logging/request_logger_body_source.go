package logging

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	// bodySourceDirPrefix is the temp directory prefix used for log part sources.
	bodySourceDirPrefix = "request-log-parts-"
	// staleBodySourceMaxAge is how old an orphaned part directory must be before
	// startup cleanup removes it. It stays well above any realistic request
	// duration so a concurrently running process never loses an in-flight body.
	staleBodySourceMaxAge = 6 * time.Hour
)

// sweptBodySourceDirs records log directories already swept in this process.
var sweptBodySourceDirs sync.Map

// cleanupStaleBodySources removes log part directories older than maxAge.
// Part directories are created per request and removed when the request ends, so
// any directory left behind belongs to a process that was killed mid-request.
// Directories newer than maxAge are kept because another process may still own them.
func cleanupStaleBodySources(baseDir string, maxAge time.Duration) error {
	entries, errRead := os.ReadDir(baseDir)
	if errRead != nil {
		if os.IsNotExist(errRead) {
			return nil
		}
		return errRead
	}
	cutoff := time.Now().Add(-maxAge)
	var firstErr error
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), bodySourceDirPrefix) {
			continue
		}
		info, errInfo := entry.Info()
		if errInfo != nil {
			if os.IsNotExist(errInfo) {
				continue
			}
			if firstErr == nil {
				firstErr = errInfo
			}
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		if errRemove := os.RemoveAll(filepath.Join(baseDir, entry.Name())); errRemove != nil && firstErr == nil {
			firstErr = errRemove
		}
	}
	return firstErr
}

// sweepStaleBodySourcesOnce removes orphaned part directories at most once per
// log directory in this process.
func sweepStaleBodySourcesOnce(baseDir string) {
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		return
	}
	if _, loaded := sweptBodySourceDirs.LoadOrStore(baseDir, struct{}{}); loaded {
		return
	}
	if errSweep := cleanupStaleBodySources(baseDir, staleBodySourceMaxAge); errSweep != nil {
		log.WithError(errSweep).Warn("failed to remove stale request log part directories")
	}
}

// FileBodySource stores large log sections as ordered temp-file parts.
type FileBodySource struct {
	mu      sync.Mutex
	dir     string
	paths   []string
	cleaned bool
}

// NewFileBodySourceInDir creates a temp-backed source under baseDir.
func NewFileBodySourceInDir(baseDir string, prefix string) (*FileBodySource, error) {
	prefix = sanitizeTempPrefix(prefix)
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		return nil, fmt.Errorf("base directory is required")
	}
	if errMkdir := os.MkdirAll(baseDir, 0755); errMkdir != nil {
		return nil, errMkdir
	}
	dir, errCreate := os.MkdirTemp(baseDir, bodySourceDirPrefix+prefix+"-*")
	if errCreate != nil {
		return nil, errCreate
	}
	return &FileBodySource{dir: dir}, nil
}

func sanitizeTempPrefix(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return "log"
	}
	var builder strings.Builder
	for _, r := range prefix {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '-' || r == '_':
			builder.WriteRune(r)
		default:
			builder.WriteByte('-')
		}
	}
	out := strings.Trim(builder.String(), "-_")
	if out == "" {
		return "log"
	}
	return out
}

// CreatePart creates one ordered detail log part.
func (s *FileBodySource) CreatePart(prefix string) (*os.File, error) {
	if s == nil {
		return nil, fmt.Errorf("file body source is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cleaned {
		return nil, fmt.Errorf("file body source has been cleaned")
	}
	prefix = sanitizeTempPrefix(prefix)
	if errMkdir := os.MkdirAll(s.dir, 0755); errMkdir != nil {
		return nil, errMkdir
	}
	file, errCreate := os.CreateTemp(s.dir, prefix+"-*.tmp")
	if errCreate != nil {
		return nil, errCreate
	}
	s.paths = append(s.paths, file.Name())
	return file, nil
}

// AppendPart appends one complete ordered part to the source.
func (s *FileBodySource) AppendPart(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil
	}
	file, errCreate := s.CreatePart("part")
	if errCreate != nil {
		return errCreate
	}
	writeErr := writeLogPart(file, data, false)
	if errClose := file.Close(); errClose != nil {
		if writeErr == nil {
			writeErr = errClose
		}
	}
	return writeErr
}

// AppendBytes appends raw bytes to a single ordered part.
func (s *FileBodySource) AppendBytes(data []byte) error {
	if s == nil {
		return fmt.Errorf("file body source is nil")
	}
	if len(data) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cleaned {
		return fmt.Errorf("file body source has been cleaned")
	}
	if errMkdir := os.MkdirAll(s.dir, 0755); errMkdir != nil {
		return errMkdir
	}

	var file *os.File
	var errOpen error
	if len(s.paths) == 0 {
		file, errOpen = os.CreateTemp(s.dir, "part-*.tmp")
		if errOpen == nil {
			s.paths = append(s.paths, file.Name())
		}
	} else {
		file, errOpen = os.OpenFile(s.paths[len(s.paths)-1], os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	}
	if errOpen != nil {
		return errOpen
	}

	_, writeErr := file.Write(data)
	if errClose := file.Close(); errClose != nil {
		if writeErr == nil {
			writeErr = errClose
		}
	}
	return writeErr
}

// HasPayload reports whether any detail parts were recorded.
func (s *FileBodySource) HasPayload() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.paths) > 0 && !s.cleaned
}

// Paths returns a copy of the ordered part paths.
func (s *FileBodySource) Paths() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.paths))
	copy(out, s.paths)
	return out
}

// WriteTo merges all ordered parts into w.
func (s *FileBodySource) WriteTo(w io.Writer) (int64, error) {
	if s == nil || w == nil {
		return 0, nil
	}
	var totalWritten int64
	paths := s.Paths()
	wrote := false
	for _, path := range paths {
		file, errOpen := os.Open(path)
		if errOpen != nil {
			if os.IsNotExist(errOpen) {
				continue
			}
			return totalWritten, errOpen
		}
		if wrote {
			n, errWrite := io.WriteString(w, "\n")
			totalWritten += int64(n)
			if errWrite != nil {
				if errClose := file.Close(); errClose != nil {
					log.WithError(errClose).Warn("failed to close log part file")
				}
				return totalWritten, errWrite
			}
		}
		n, errCopy := io.Copy(w, file)
		totalWritten += n
		if errClose := file.Close(); errClose != nil {
			log.WithError(errClose).Warn("failed to close log part file")
			if errCopy == nil {
				errCopy = errClose
			}
		}
		if errCopy != nil {
			return totalWritten, errCopy
		}
		wrote = true
	}
	return totalWritten, nil
}

// Bytes merges all ordered parts into memory.
func (s *FileBodySource) Bytes() ([]byte, error) {
	var buf bytes.Buffer
	if _, errWrite := s.WriteTo(&buf); errWrite != nil {
		return nil, errWrite
	}
	return buf.Bytes(), nil
}

// Cleanup removes all temp detail parts and their directory.
func (s *FileBodySource) Cleanup() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.cleaned {
		s.mu.Unlock()
		return nil
	}
	paths := make([]string, len(s.paths))
	copy(paths, s.paths)
	dir := s.dir
	s.paths = nil
	s.cleaned = true
	s.mu.Unlock()

	var firstErr error
	for _, path := range paths {
		if errRemove := os.Remove(path); errRemove != nil && !os.IsNotExist(errRemove) && firstErr == nil {
			firstErr = errRemove
		}
	}
	if dir != "" {
		if errRemove := os.RemoveAll(dir); errRemove != nil && firstErr == nil {
			firstErr = errRemove
		}
	}
	return firstErr
}

func cleanupFileBodySources(sources ...*FileBodySource) {
	for _, source := range sources {
		if source == nil {
			continue
		}
		if errCleanup := source.Cleanup(); errCleanup != nil {
			log.WithError(errCleanup).Warn("failed to clean up log part files")
		}
	}
}
