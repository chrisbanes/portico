package service

import (
	"errors"
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func guardSupported() error { return nil }

// setImmutable sets or clears name's UF_IMMUTABLE (uchg) flag, which its
// owner may change without root, and verifies the result.
func setImmutable(root *os.Root, name string, on bool) error {
	file, err := root.Open(name)
	if err != nil {
		return persistenceErr(err)
	}
	defer file.Close()
	var st unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &st); err != nil {
		return persistenceErr(err)
	}
	flags := st.Flags &^ unix.UF_IMMUTABLE
	if on {
		flags |= unix.UF_IMMUTABLE
	}
	if err := unix.Fchflags(int(file.Fd()), int(flags)); err != nil {
		return persistenceErr(err)
	}
	if err := file.Sync(); err != nil {
		return persistenceErr(err)
	}
	if immutable, err := isImmutable(file); err != nil {
		return err
	} else if immutable != on {
		return persistenceErr(os.ErrPermission)
	}
	return nil
}

func isImmutable(file *os.File) (bool, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &st); err != nil {
		return false, persistenceErr(err)
	}
	return st.Flags&unix.UF_IMMUTABLE != 0, nil
}

// refuseFlags refuses a target that already has a flag the guard would
// clear or that would stop it: uchg, uappnd, or any system (SF_*) flag.
// Other user flags, such as hidden, are not preserved by the guard.
func refuseFlags(info fs.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return unsupportedErr(errors.New("no file flags"))
	}
	if st.Flags&(unix.UF_IMMUTABLE|unix.UF_APPEND|unix.SF_SETTABLE|unix.SF_SYNTHETIC) != 0 {
		return unsupportedErr(errors.New(info.Name() + " already has file flags"))
	}
	return nil
}
