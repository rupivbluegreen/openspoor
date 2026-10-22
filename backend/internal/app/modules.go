package app

import (
	"github.com/openspoor/openspoor/internal/core/kernel"
	"github.com/openspoor/openspoor/internal/epcis"
	"github.com/openspoor/openspoor/internal/masterdata"
	"github.com/openspoor/openspoor/internal/platform"
	"github.com/openspoor/openspoor/internal/serialization"
	"github.com/openspoor/openspoor/internal/trace"
)

// buildModules lists every bounded context in mount order.
func buildModules(d kernel.Deps, plat *platform.Module) []kernel.Module {
	md := masterdata.New(d)
	ser := serialization.New(d)
	ep := epcis.New(d, ser)
	tr := trace.New(d)
	return []kernel.Module{plat, md, ser, ep, tr}
}
