//go:build !windows

package executor

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// A persistent sibling lock file avoids inode replacement races on auth JSON.
// Never unlink it: flock ownership is released on close or process death.
func acquireKiroRefreshLease(path string) (*os.File, error) {
	f, err := os.OpenFile(path+".refresh.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("kiro: open refresh lease: %w", err)
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		closeErr := f.Close()
		if closeErr != nil {
			return nil, errors.Join(err, closeErr)
		}
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, kiroHealthRetryError(503, "kiro local refresh owned by another process", kiroBusyRetryAfter)
		}
		return nil, fmt.Errorf("kiro: acquire refresh lease: %w", err)
	}
	return f, nil
}
