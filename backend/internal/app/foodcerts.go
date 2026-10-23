package app

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/internal/food"
)

// supplierCertificates lets the food module use the supplier compliance hub's certificates
// (supplier_record) without depending on the link module: certificates linked to lots,
// products and sites are the same records the hub lists and alerts on.
type supplierCertificates struct{}

var _ food.CertificateStore = supplierCertificates{}

const supplierCertSelect = `select s.id, s.kind, s.number, s.issuer, s.scope, s.valid_from, s.valid_to, s.document_id, s.partner_id, coalesce(p.name, ''), coalesce(p.gln, '')
	from supplier_record s left join partner p on p.id = s.partner_id`

// CreateCertificate implements food.CertificateStore.
func (supplierCertificates) CreateCertificate(ctx context.Context, tx pgx.Tx, in food.CertificateInput) (food.Certificate, error) {
	if in.PartnerID != nil {
		var n int
		if err := tx.QueryRow(ctx, `select count(*) from partner where id=$1`, *in.PartnerID).Scan(&n); err != nil || n == 0 {
			return food.Certificate{}, httpx.BadRequest("unknown partnerId")
		}
	}
	if in.DocumentID != nil {
		var n int
		if err := tx.QueryRow(ctx, `select count(*) from shared_document where id=$1`, *in.DocumentID).Scan(&n); err != nil || n == 0 {
			return food.Certificate{}, httpx.BadRequest("unknown documentId (upload the document first)")
		}
	}
	id := ids.New()
	if _, err := tx.Exec(ctx, `insert into supplier_record (id, tenant_id, partner_id, category, kind, number, issuer, scope, valid_from, valid_to, document_id, notes)
		values ($1, app_tenant(), $2, 'certificate', $3,$4,$5,$6,$7,$8,$9,$10)`,
		id, in.PartnerID, in.Kind, in.Number, in.Issuer, in.Scope, in.ValidFrom, in.ValidTo, in.DocumentID, in.Notes); err != nil {
		return food.Certificate{}, err
	}
	cs, err := scanCerts(ctx, tx, supplierCertSelect+` where s.id = $1`, id)
	if err != nil {
		return food.Certificate{}, err
	}
	c := cs[0]
	_, err = audit.Write(ctx, tx, audit.Record{Action: "supplier.record.create", Entity: "supplier_record", EntityID: id.String(), After: c})
	return c, err
}

// Certificates implements food.CertificateStore.
func (supplierCertificates) Certificates(ctx context.Context, tx pgx.Tx, certIDs []uuid.UUID, supplierGLNs []string) ([]food.Certificate, error) {
	if len(certIDs) == 0 && len(supplierGLNs) == 0 {
		return []food.Certificate{}, nil
	}
	if certIDs == nil {
		certIDs = []uuid.UUID{}
	}
	if supplierGLNs == nil {
		supplierGLNs = []string{}
	}
	cs, err := scanCerts(ctx, tx, supplierCertSelect+` where s.id = any($1) and s.category = 'certificate' order by s.valid_to nulls last, s.kind`, certIDs)
	if err != nil || len(supplierGLNs) == 0 {
		return cs, err
	}
	held, err := scanCerts(ctx, tx, supplierCertSelect+` where p.gln = any($1) and s.category = 'certificate' order by s.valid_to nulls last, s.kind`, supplierGLNs)
	if err != nil {
		return nil, err
	}
	for i := range cs {
		cs[i].SupplierGLN = ""
	}
	return append(cs, held...), nil
}

func scanCerts(ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]food.Certificate, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []food.Certificate{}
	for rows.Next() {
		var c food.Certificate
		var from, to *time.Time
		if err := rows.Scan(&c.ID, &c.Kind, &c.Number, &c.Issuer, &c.Scope, &from, &to, &c.DocumentID, &c.PartnerID, &c.Supplier, &c.SupplierGLN); err != nil {
			return nil, err
		}
		c.ValidFrom, c.ValidTo = from, to
		out = append(out, c)
	}
	return out, rows.Err()
}
