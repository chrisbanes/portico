//go:build !darwin

package service

import (
	"errors"
	"os"
)

// Only macOS has legacy Portico readers to guard against.
var errGuardUnsupported = unsupportedErr(errors.New("the legacy guard is macOS-only"))

func guardSupported() error { return errGuardUnsupported }

func setImmutable(*os.Root, string, bool) error { return errGuardUnsupported }

func isImmutable(*os.File) (bool, error) { return false, errGuardUnsupported }
