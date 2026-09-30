//go:build windows

package executor

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// LockFileEx ownership is released on close or process death, like flock.
func acquireKiroRefreshLease(path string) (*os.File, error) {
	f, err := os.OpenFile(path+".refresh.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("kiro: open refresh lease: %w", err)
	}
	var overlapped windows.Overlapped
	if err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped); err != nil {
		closeErr := f.Close()
		if closeErr != nil {
			return nil, errors.Join(err, closeErr)
		}
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, kiroHealthRetryError(503, "kiro local refresh owned by another process", kiroBusyRetryAfter)
		}
		return nil, fmt.Errorf("kiro: acquire refresh lease: %w", err)
	}
	return f, nil
}
