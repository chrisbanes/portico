//go:build darwin && cgo

package main

import (
	"github.com/chrisbanes/portico/helper/internal/discovery"
	"github.com/chrisbanes/portico/helper/internal/service"
)

func discoverer() service.Discoverer { return discovery.New() }
