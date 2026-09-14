package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
)

// devinDumpDirEnv opts into writing the client payload behind an upstream
// rejection to disk. Rejections carry no HTTP body, and the logged request
// shape only summarises sizes and counts, so a reproduction needs the exact
// bytes the client sent.
const devinDumpDirEnv = "DEVIN_REJECT_DUMP_DIR"

// devinDumpLimit caps how many payloads one process writes, so enabling the
// dump on a busy host cannot fill the disk.
const devinDumpLimit = 20

var devinDumpCount atomic.Int64

// devinDumpRejectedRequest writes the client request body behind a rejection
// when DEVIN_REJECT_DUMP_DIR is set. It never fails the request: diagnostics
// must not change the outcome the client sees.
func devinDumpRejectedRequest(body []byte, failure error) {
	dir := strings.TrimSpace(os.Getenv(devinDumpDirEnv))
	if dir == "" || len(body) == 0 {
		return
	}
	if devinDumpCount.Add(1) > devinDumpLimit {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.WithError(err).Debug("devin: failed to create the rejection dump directory")
		return
	}
	name := fmt.Sprintf("devin-rejected-%s-%d.json", time.Now().UTC().Format("20060102T150405"), devinDumpCount.Load())
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		log.WithError(err).Debug("devin: failed to write the rejection dump")
		return
	}
	log.WithFields(log.Fields{
		"provider": "devin",
		"path":     path,
		"bytes":    len(body),
	}).WithError(failure).Warn("devin: wrote the client payload behind an upstream rejection")
}
