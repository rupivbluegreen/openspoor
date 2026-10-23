package app

import (
	"context"
	"github.com/openspoor/openspoor/internal/act"

	"github.com/google/uuid"

	"github.com/openspoor/openspoor/internal/connectors"
	"github.com/openspoor/openspoor/internal/core/kernel"
	"github.com/openspoor/openspoor/internal/dscsa"
	"github.com/openspoor/openspoor/internal/ecosystem"
	"github.com/openspoor/openspoor/internal/epcis"
	"github.com/openspoor/openspoor/internal/food"
	"github.com/openspoor/openspoor/internal/link"
	"github.com/openspoor/openspoor/internal/masterdata"
	"github.com/openspoor/openspoor/internal/migration"
	"github.com/openspoor/openspoor/internal/partners"
	"github.com/openspoor/openspoor/internal/platform"
	"github.com/openspoor/openspoor/internal/saas"
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
	sa := saas.New(d, plat)
	sa.Wire(func(g func(context.Context, uuid.UUID, int) error) { ep.Gate = g })
	plat.HostTenant = sa.SlugFromHost
	fd := food.New(d, ep)
	fd.Certs = supplierCertificates{}
	lk := link.New(d, ep)
	ac := act.New(d)
	ec := ecosystem.New(d, plat, ep)
	mg := migration.New(d, ep, tr)
	return []kernel.Module{plat, md, ser, ep, tr, pa, ds, vf, co, sa, fd, lk, ac, ec, mg}, nil
}
