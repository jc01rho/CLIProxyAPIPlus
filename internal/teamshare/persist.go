package teamshare

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

const (
	stateVersion  = 1
	flushInterval = 30 * time.Second
)

type persistedState struct {
	Version  int                      `json:"version"`
	SavedAt  time.Time                `json:"saved_at"`
	Accounts map[string]*accountState `json:"accounts"`
	Pools    map[string]*poolState    `json:"pools"`
}

// SetStatePath selects the file used by Load and Flush.
func (e *Engine) SetStatePath(path string) {
	e.mu.Lock()
	e.statePath = strings.TrimSpace(path)
	e.mu.Unlock()
}

// Load restores counters and calibration saved by Flush. A missing file is not an error.
func (e *Engine) Load() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.statePath == "" {
		return nil
	}
	raw, err := os.ReadFile(e.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var ps persistedState
	if err := json.Unmarshal(raw, &ps); err != nil {
		return err
	}
	if ps.Version != stateVersion {
		return errors.New("team-share state version mismatch")
	}
	for id, st := range ps.Accounts {
		if st == nil {
			continue
		}
		if st.Models == nil {
			st.Models = map[string]*ModelTotals{}
		}
		e.accounts[id] = st
	}
	for name, p := range ps.Pools {
		if p == nil {
			continue
		}
		if p.Members == nil {
			p.Members = map[string]*memberState{}
		}
		if cur := e.pools[name]; cur != nil {
			for id, m := range p.Members {
				if _, ok := cur.Members[id]; ok && m != nil {
					cur.Members[id] = m
				}
			}
			cur.FiveHour, cur.Weekly = p.FiveHour, p.Weekly
		}
	}
	return nil
}

// Flush writes the state atomically with owner-only permissions.
func (e *Engine) Flush() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.flushLocked(e.now())
}

func (e *Engine) maybeFlushLocked(now time.Time) {
	if e.statePath == "" || !e.dirty || now.Sub(e.lastFlush) < flushInterval {
		return
	}
	_ = e.flushLocked(now)
}

func (e *Engine) flushLocked(now time.Time) error {
	if e.statePath == "" {
		return nil
	}
	raw, err := json.Marshal(persistedState{Version: stateVersion, SavedAt: now.UTC(), Accounts: e.accounts, Pools: e.pools})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(e.statePath), 0o700); err != nil {
		return err
	}
	tmp := e.statePath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, e.statePath); err != nil {
		return err
	}
	e.dirty = false
	e.lastFlush = now
	return nil
}

// HandleUsage implements the usage plugin interface for Claude requests.
func (e *Engine) HandleUsage(_ context.Context, record coreusage.Record) {
	if e == nil || !strings.EqualFold(strings.TrimSpace(record.Provider), "claude") {
		return
	}
	e.Record(Sample{
		AuthID:    record.AuthID,
		AuthIndex: record.AuthIndex,
		APIKey:    record.APIKey,
		Model:     record.Model,
		Usage: Usage{
			Input:      record.Detail.InputTokens,
			Output:     record.Detail.OutputTokens,
			CacheRead:  record.Detail.CacheReadTokens,
			CacheWrite: record.Detail.CacheCreationTokens,
		},
		Headers:    record.ResponseHeaders,
		Failed:     record.Failed,
		StatusCode: record.Fail.StatusCode,
		At:         record.RequestedAt,
	})
}
