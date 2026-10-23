package saas

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/kernel"
)

// exportTables lists what a tenant export contains (secrets and password hashes excluded).
var exportTables = []struct{ name, sql string }{
	{"tenant", `select id, slug, name, plan, region, verticals, brand, created_at from tenant where id=app_tenant()`},
	{"organizations", `select * from organization`},
	{"sites", `select * from site`},
	{"licenses", `select * from license`},
	{"users", `select id, email, name, roles, status, locale, created_at from app_user`},
	{"products", `select * from product`},
	{"packaging", `select * from packaging_template`},
	{"serialPools", `select id, product_id, gtin, market, mode, alphabet, length, prefix, next_counter, capacity, status, created_at from serial_pool`},
	{"allocations", `select * from serial_allocation`},
	{"partners", `select id, name, gln, company_prefix, country, kind, status, endpoints, created_at from partner`},
	{"shipments", `select * from shipment`},
	{"regulatoryReports", `select * from regulatory_report`},
	{"serialState", `select * from serial_state`},
	{"epcisEvents", `select doc from epcis_event order by record_time`},
	{"auditTrail", `select seq, at, actor, action, entity, entity_id, before, after, reason, prev_hash, hash from audit_entry order by seq`},
}

// export streams the tenant's data as one JSON document (GDPR Art. 20 portability).
func (m *Module) export(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="openspoor-export-`+time.Now().UTC().Format("20060102")+`.json"`)
	return m.Tx(ctx, func(tx pgx.Tx) error {
		if _, err := audit.Write(ctx, tx, audit.Record{Action: "tenant.export", Entity: "tenant", EntityID: kernel.TenantOf(ctx).String()}); err != nil {
			return err
		}
		enc := json.NewEncoder(w)
		_, _ = w.Write([]byte(`{"exportedAt":`))
		_ = enc.Encode(time.Now().UTC())
		for _, t := range exportTables {
			_, _ = w.Write([]byte(`,"` + t.name + `":[`))
			rows, err := tx.Query(ctx, `select to_jsonb(x) from (`+t.sql+`) x`)
			if err != nil {
				return err
			}
			first := true
			for rows.Next() {
				var b json.RawMessage
				if err := rows.Scan(&b); err != nil {
					rows.Close()
					return err
				}
				if !first {
					_, _ = w.Write([]byte(","))
				}
				first = false
				_, _ = w.Write(b)
			}
			rows.Close()
			_, _ = w.Write([]byte("]"))
		}
		_, _ = w.Write([]byte("}"))
		return nil
	})
}

// deleteTenant erases a tenant: the admin re-authenticates and confirms the slug. The tenant's
// data key is destroyed first (crypto-shredding every sealed secret), then all rows are deleted.
// The hash-chained audit trail is retained (append-only, regulatory retention) — see OPEN_QUESTIONS.md.
func (m *Module) deleteTenant(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Password    string `json:"password"`
		ConfirmSlug string `json:"confirmSlug"`
		Reason      string `json:"reason"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := auth.MustFrom(r.Context())
	if p.Kind != "user" {
		return httpx.Forbidden("only a user can delete the tenant")
	}
	t, err := m.Platform.TenantBySlug(r.Context(), in.ConfirmSlug)
	if err != nil || t.ID != p.TenantID {
		return httpx.BadRequest("confirmSlug must match this tenant")
	}
	if _, err := m.Platform.Login(r.Context(), t.Slug, p.Email, in.Password); err != nil {
		return httpx.Unauthorized("re-authentication failed")
	}
	if in.Reason == "" {
		return httpx.BadRequest("reason is required")
	}
	ctx := r.Context()
	err = m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		if _, err := audit.WriteAs(ctx, tx, t.ID, p.AuditActor(), audit.Record{Action: "tenant.delete", Entity: "tenant", EntityID: t.ID.String(), Reason: in.Reason}); err != nil {
			return err
		}
		for _, q := range []string{
			`delete from tenant_key where tenant_id=$1`,
			`delete from epcis_event where tenant_id=$1`,
			`delete from serial_state where tenant_id=$1`,
			`delete from job where tenant_id=$1`,
			`update partner set linked_tenant=null where linked_tenant=$1`,
			`delete from tenant where id=$1`,
		} {
			if _, err := tx.Exec(ctx, q, t.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	m.Keys.Forget(t.ID)
	httpx.JSON(w, http.StatusOK, map[string]any{"deleted": t.Slug})
	return nil
}
