//go:build unix

package service

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

const lockFile = "service-owner.lock"

// lockOwner takes the lifetime flock on service-owner.lock. The file is never
// unlinked or replaced; closing the returned file releases the lock.
func lockOwner(root *os.Root) (*os.File, error) {
	if info, err := root.Lstat(lockFile); err == nil && !info.Mode().IsRegular() {
		return nil, unsafeErr(lockFile)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, persistenceErr(err)
	}
	file, err := root.OpenFile(lockFile, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, persistenceErr(err)
	}
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() || checkOwned(info) != nil {
		file.Close()
		return nil, unsafeErr(lockFile)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrOwnershipBusy
		}
		return nil, persistenceErr(err)
	}
	return file, nil
}

// checkOwned rejects an entry owned by another user or writable by group or
// others, either of which could have altered state behind the service.
func checkOwned(info fs.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Geteuid() || info.Mode().Perm()&0o022 != 0 {
		return errors.New("unsafe ownership or mode")
	}
	return nil
}
