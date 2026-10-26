package teamshare

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var auditMu sync.Mutex

// AuditEntry is one append-only record of who changed which seat and when.
type AuditEntry struct {
	Time   time.Time         `json:"time"`
	Actor  string            `json:"actor"`
	Action string            `json:"action"`
	Name   string            `json:"name,omitempty"`
	Tier   string            `json:"tier,omitempty"`
	Seat   string            `json:"seat,omitempty"`
	Result string            `json:"result,omitempty"`
	Detail map[string]string `json:"detail,omitempty"`
}

// AppendAudit appends one JSON line to path, creating it with owner-only permissions.
func AppendAudit(path string, entry AuditEntry) error {
	if path == "" {
		return fmt.Errorf("audit log path is empty")
	}
	if entry.Time.IsZero() {
		entry.Time = time.Now().UTC()
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	auditMu.Lock()
	defer auditMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}
