//go:build !darwin || !cgo

package main

import "github.com/chrisbanes/portico/helper/internal/service"

// discoverer is nil: automatic discovery is unavailable, explicit ports work.
func discoverer() service.Discoverer { return nil }
