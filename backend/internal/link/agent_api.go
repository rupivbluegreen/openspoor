package link

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
)

// Typed entry points for the mapping agent in the act module (wired through an interface in
// internal/app). They apply the same validation as the mapping studio endpoints.

// MappingInput is the definition of a mapping to store.
type MappingInput = mappingInput

// SaveMapping validates and stores a mapping, and audits it as the caller.
func SaveMapping(ctx context.Context, tx pgx.Tx, in MappingInput) (Mapping, error) {
	if err := in.validate(ctx, tx); err != nil {
		return Mapping{}, err
	}
	mp, err := db.One[Mapping](ctx, tx, `insert into b2b_mapping (id, tenant_id, name, partner_id, doc_type, format, options, fields)
		values ($1, app_tenant(), $2,$3,$4,$5,$6,$7) returning `+mappingCols, ids.New(), strings.TrimSpace(in.Name), in.PartnerID, in.DocType, in.Format, in.Options, in.Fields)
	if err != nil {
		return mp, err
	}
	_, err = audit.Write(ctx, tx, audit.Record{Action: "b2b.mapping.create", Entity: "b2b_mapping", EntityID: mp.ID.String(), After: mp})
	return mp, err
}

// LoadMapping reads a saved mapping.
func LoadMapping(ctx context.Context, tx pgx.Tx, id uuid.UUID) (Mapping, error) {
	return db.One[Mapping](ctx, tx, `select `+mappingCols+` from b2b_mapping where id=$1`, id)
}

// RemoveMapping deletes a saved mapping, and audits it as the caller.
func RemoveMapping(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	before, err := db.One[Mapping](ctx, tx, `delete from b2b_mapping where id=$1 returning `+mappingCols, id)
	if err != nil {
		return err
	}
	_, err = audit.Write(ctx, tx, audit.Record{Action: "b2b.mapping.delete", Entity: "b2b_mapping", EntityID: id.String(), Before: before})
	return err
}

// ValidateSpec checks a mapping definition's shape without storing it.
func ValidateSpec(s MappingSpec) error {
	if err := s.Validate(); err != nil {
		return httpx.BadRequest(err.Error())
	}
	return nil
}

// ValidDocType reports whether t is a mapping document type.
func ValidDocType(t string) bool { return validDocType(t) }

// EvalRow resolves every mapped field of the spec for one input row (column name to value),
// returning the canonical value per target and a message for each field that could not be
// resolved. Nothing is stored.
func EvalRow(s MappingSpec, row map[string]string) (map[string]string, map[string]string) {
	out, bad := map[string]string{}, map[string]string{}
	for _, f := range s.Fields {
		v, err := f.value(row, row)
		if err != nil {
			bad[f.Target] = err.Error()
			continue
		}
		out[f.Target] = v
	}
	return out, bad
}
