package app

import (
	"github.com/openspoor/openspoor/internal/core/kernel"
	"github.com/openspoor/openspoor/internal/platform"
)

// buildModules lists every bounded context in mount order.
func buildModules(d kernel.Deps, plat *platform.Module) []kernel.Module {
	return []kernel.Module{plat}
}
