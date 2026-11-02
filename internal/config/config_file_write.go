package config

import (
	"os"
	"sync"
)

// configFileMu serializes every in-process write of a config file, so a
// read-modify-write such as LoadConfig hashing a plaintext secret cannot
// interleave with a management save and roll back or tear the file.
var configFileMu sync.Mutex

// Test seams for the serialized config write path. Production code leaves them nil.
var (
	// onConfigFileLockWait runs when a config writer is about to block on the config file lock.
	onConfigFileLockWait func()
	// afterConfigRewriteRead runs inside a config read-modify-write after the file was read.
	afterConfigRewriteRead func()
)

// lockConfigFile acquires the config file lock and returns its release function.
// It never calls back into callers, so holding a caller lock (such as the
// management handler mutex) while acquiring it cannot invert lock order.
func lockConfigFile() func() {
	if !configFileMu.TryLock() {
		if onConfigFileLockWait != nil {
			onConfigFileLockWait()
		}
		configFileMu.Lock()
	}
	return configFileMu.Unlock
}

// WriteConfigFile writes data to configFile under the config file lock and
// syncs it to disk. The file is rewritten in place instead of being replaced
// through a rename, so a config file bind-mounted into a container keeps its
// inode (rename onto a single-file bind mount fails with EBUSY).
func WriteConfigFile(configFile string, data []byte, perm os.FileMode) error {
	unlock := lockConfigFile()
	defer unlock()
	return writeConfigFileLocked(configFile, data, perm)
}

// writeConfigFileLocked writes and syncs configFile in place. Callers must hold configFileMu.
func writeConfigFileLocked(configFile string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(configFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, errWrite := f.Write(data); errWrite != nil {
		_ = f.Close()
		return errWrite
	}
	if errSync := f.Sync(); errSync != nil {
		_ = f.Close()
		return errSync
	}
	return f.Close()
}

// rewriteConfigFile runs a read-modify-write of configFile under the config file
// lock. A nil result from transform leaves the file untouched. It returns the
// bytes it read and, when it wrote the file, the bytes it wrote.
func rewriteConfigFile(configFile string, transform func(current []byte) ([]byte, error)) (read, written []byte, err error) {
	unlock := lockConfigFile()
	defer unlock()
	current, errRead := os.ReadFile(configFile)
	if errRead != nil {
		return nil, nil, errRead
	}
	if afterConfigRewriteRead != nil {
		afterConfigRewriteRead()
	}
	next, errTransform := transform(current)
	if errTransform != nil || next == nil {
		return current, nil, errTransform
	}
	if errWrite := writeConfigFileLocked(configFile, next, 0600); errWrite != nil {
		return current, nil, errWrite
	}
	return current, next, nil
}
