package app

import (
	"context"
	"errors"
	"github.com/openspoor/openspoor/internal/act"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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
	"github.com/openspoor/openspoor/pkg/edi"
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
	co.Capture = connectors.CaptureVia(func(ctx context.Context, ct string, b []byte, src string) (uuid.UUID, []string, error) {
		c, err := ep.Capture(ctx, ct, b, src)
		var ce *epcis.CaptureError
		if errors.As(err, &ce) {
			msgs := make([]string, 0, len(ce.Capture.Errors))
			for _, fe := range ce.Capture.Errors {
				msgs = append(msgs, fe.Field+": "+fe.Message)
			}
			return uuid.Nil, msgs, err
		}
		return c.ID, nil, err
	})
	sa := saas.New(d, plat)
	sa.Wire(func(g func(context.Context, uuid.UUID, int) error) { ep.Gate = g })
	plat.HostTenant = sa.SlugFromHost
	fd := food.New(d, ep)
	fd.Certs = supplierCertificates{}
	lk := link.New(d, ep)
	co.StoreEDI = func(ctx context.Context, tx pgx.Tx, doc edi.Document) (string, error) {
		stored, err := link.StoreDoc(ctx, tx, "inbound", nil, nil, doc)
		return stored.ID.String(), err
	}
	ac := act.New(d)
	ec := ecosystem.New(d, plat, ep)
	mg := migration.New(d, ep, tr)
	return []kernel.Module{plat, md, ser, ep, tr, pa, ds, vf, co, sa, fd, lk, ac, ec, mg}, nil
}
