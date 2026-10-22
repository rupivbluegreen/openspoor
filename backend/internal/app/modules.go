package app

import (
	"github.com/openspoor/openspoor/internal/connectors"
	"github.com/openspoor/openspoor/internal/core/kernel"
	"github.com/openspoor/openspoor/internal/dscsa"
	"github.com/openspoor/openspoor/internal/epcis"
	"github.com/openspoor/openspoor/internal/masterdata"
	"github.com/openspoor/openspoor/internal/partners"
	"github.com/openspoor/openspoor/internal/platform"
	"github.com/openspoor/openspoor/internal/serialization"
	"github.com/openspoor/openspoor/internal/trace"
	"github.com/openspoor/openspoor/internal/verify"
)

// buildModules lists every bounded context in mount order.
func buildModules(d kernel.Deps, plat *platform.Module) ([]kernel.Module, error) {
	md := masterdata.New(d)
	ser := serialization.New(d)
	ep := epcis.New(d, ser)
	tr := trace.New(d)
	pa := partners.New(d)
	ds := dscsa.New(d, ep)
	vf := verify.New(d)
	co, err := connectors.New(d)
	if err != nil {
		return nil, err
	}
	ep.OnCapture(connectors.JobDispatch)
	return []kernel.Module{plat, md, ser, ep, tr, pa, ds, vf, co}, nil
}
