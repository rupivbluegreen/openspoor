// Package link is "Link at full scale": the full B2B transaction set (orders, ASN, invoices,
// inventory, returns, chargebacks) with X12 / EDIFACT / IDoc mappers, the supplier
// compliance hub (certificates, audits, specifications, expiry alerts), document exchange,
// and node-to-node federation (ADR-008).
package link

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/internal/core/kernel"
	"github.com/openspoor/openspoor/internal/epcis"
	"github.com/openspoor/openspoor/internal/partners"
	"github.com/openspoor/openspoor/pkg/edi"
)

// Module is the Link bounded context.
type Module struct {
	kernel.Deps
	EPCIS  *epcis.Module
	Client *http.Client
	// DeliveryAttempts bounds retries of one federation delivery before it is dead-lettered (default 10).
	DeliveryAttempts int
	// InsecureDIDWeb resolves did:web identifiers over http instead of https (tests and local development only).
	InsecureDIDWeb bool
	// RetryShipment re-sends an outbound TI/TS whose delivery failed through the dscsa outbound
	// path (wired by the app; link does not import dscsa). It returns the shipment's new status
	// ("sent" or "send_failed") and the transport response.
	RetryShipment func(ctx context.Context, id uuid.UUID) (string, map[string]any, error)
	node          *nodeIdentity
}

// New creates the module.
func New(d kernel.Deps, ep *epcis.Module) *Module {
	return &Module{Deps: d, EPCIS: ep, Client: &http.Client{Timeout: 15 * time.Second}}
}

// Name implements kernel.Module.
func (m *Module) Name() string { return "link" }

// Routes implements kernel.Module.
func (m *Module) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.Require(auth.RoleAdmin, auth.RoleOps, auth.RoleIntegration, auth.RoleCompliance))
		r.Get("/b2b/types", httpx.Handler(m.listTypes))
		r.Get("/b2b/documents", httpx.Handler(m.listDocs))
		r.Post("/b2b/documents", httpx.Handler(m.createDoc))
		r.Post("/b2b/documents/import", httpx.Handler(m.importDoc))
		r.Get("/b2b/documents/{id}", httpx.Handler(m.getDoc))
		r.Get("/b2b/documents/{id}/edi", httpx.Handler(m.renderDoc))
		r.Post("/b2b/documents/{id}/send", httpx.Handler(m.sendDoc))
		r.Get("/suppliers/certificates", httpx.Handler(m.listCerts))
		r.Post("/suppliers/certificates", httpx.Handler(m.createCert))
		r.Get("/documents", httpx.Handler(m.listFiles))
		r.Post("/documents", httpx.Handler(m.uploadFile))
		r.Get("/documents/{id}", httpx.Handler(m.downloadFile))
		r.Post("/documents/{id}/share", httpx.Handler(m.shareFile))
		r.Get("/federation/node", httpx.Handler(m.getNode))
		r.Get("/federation/peers", httpx.Handler(m.listPeers))
		r.Post("/federation/peers", httpx.Handler(m.addPeer))
		r.Post("/federation/peers/{id}/approve", httpx.Handler(m.approvePeer))
		r.Post("/federation/send", httpx.Handler(m.fedSend))
		r.Get("/federation/messages", httpx.Handler(m.listMessages))
		// Mapping studio.
		r.Get("/b2b/mappings", httpx.Handler(m.listMappings))
		r.Post("/b2b/mappings", httpx.Handler(m.createMapping))
		r.Post("/b2b/mappings/preview", httpx.Handler(m.previewMapping))
		r.Post("/b2b/mappings/suggest", httpx.Handler(m.suggestMapping))
		r.Get("/b2b/mappings/{id}", httpx.Handler(m.getMapping))
		r.Put("/b2b/mappings/{id}", httpx.Handler(m.updateMapping))
		r.Delete("/b2b/mappings/{id}", httpx.Handler(m.deleteMapping))
		// Message tracking, dead letters and SLAs.
		r.Post("/b2b/documents/{id}/ack", httpx.Handler(m.ackDoc))
		r.Get("/messages", httpx.Handler(m.listMessageLog))
		r.Post("/messages/{id}/retry", httpx.Handler(m.retryMessage))
		r.Get("/messages/slas", httpx.Handler(m.listSLAs))
		r.Put("/messages/slas/{partnerId}", httpx.Handler(m.putSLA))
		r.Delete("/messages/slas/{partnerId}", httpx.Handler(m.deleteSLA))
		// Onboarding test harness.
		r.Post("/onboarding/tests", httpx.Handler(m.runPartnerTest))
		// Authorized trading partner credentials: verification.
		r.Get("/credentials", httpx.Handler(m.listCredentials))
		r.Post("/credentials/verify", httpx.Handler(m.verifyCredential))
		r.Get("/credentials/verifications", httpx.Handler(m.listVerifications))
		r.Get("/credentials/{id}", httpx.Handler(m.getCredential))
	})
	// Issuing and revoking credentials is for administrators and compliance.
	r.Group(func(r chi.Router) {
		r.Use(auth.Require(auth.RoleAdmin, auth.RoleCompliance))
		r.Post("/credentials", httpx.Handler(m.issueCredential))
		r.Post("/credentials/{id}/revoke", httpx.Handler(m.revokeCredential))
	})
}

// Public implements kernel.PublicRoutes.
func (m *Module) Public(r chi.Router) {
	r.Post("/federation/inbox", httpx.Handler(m.inbox))
	r.Get("/credentials/status/revocation", httpx.Handler(m.statusList))
}

// Schedules implements kernel.Scheduled: certificate expiry alerts and partner SLA breaches.
func (m *Module) Schedules() []kernel.Schedule {
	return []kernel.Schedule{
		{Name: "link.cert_expiry", Every: 6 * time.Hour, Run: m.ExpiryAlerts},
		{Name: "link.sla", Every: 15 * time.Minute, Run: m.SLABreaches},
	}
}

// --- B2B documents ---

// Doc is a stored B2B document.
type Doc struct {
	ID        uuid.UUID      `json:"id" db:"id"`
	Type      string         `json:"type" db:"type"`
	Direction string         `json:"direction" db:"direction"`
	PartnerID *uuid.UUID     `json:"partnerId,omitempty" db:"partner_id"`
	Number    string         `json:"number" db:"number"`
	Status    string         `json:"status" db:"status"`
	Payload   edi.Document   `json:"payload" db:"payload"`
	Total     *float64       `json:"total,omitempty" db:"total"`
	Currency  *string        `json:"currency,omitempty" db:"currency"`
	RelatedID *uuid.UUID     `json:"relatedId,omitempty" db:"related_id"`
	Delivery  map[string]any `json:"delivery,omitempty" db:"delivery"`
	CreatedAt time.Time      `json:"createdAt" db:"created_at"`
	SentAt    *time.Time     `json:"sentAt,omitempty" db:"sent_at"`
	AckedAt   *time.Time     `json:"ackedAt,omitempty" db:"acked_at"`
}

const docCols = `id, type, direction, partner_id, number, status, payload, total, currency, related_id, delivery, created_at, sent_at, acked_at`

func (m *Module) listDocs(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[Doc](r.Context(), tx, `select `+docCols+` from b2b_document where ($1='' or type=$1) and ($2='' or direction=$2) order by created_at desc limit 500`, q.Get("type"), q.Get("direction"))
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[Doc]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) getDoc(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		d, err := db.One[Doc](r.Context(), tx, `select `+docCols+` from b2b_document where id=$1`, id)
		if err == nil {
			httpx.JSON(w, http.StatusOK, d)
		}
		return err
	})
}

// listTypes lists the transaction type registry (pkg/edi): key, name, category, direction,
// standard identifiers, required fields, validation rules and available renderings.
// Filters: category, q (matches key, name, X12 set and EDIFACT message).
func (m *Module) listTypes(w http.ResponseWriter, r *http.Request) error {
	cat, q := r.URL.Query().Get("category"), strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	items := []edi.Info{}
	counts := map[edi.Category]int{}
	for _, t := range edi.Infos() {
		counts[t.Category]++
		if cat != "" && string(t.Category) != cat {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(t.Key+" "+t.Name+" "+t.Standards.X12+" "+t.Standards.EDIFACT), q) {
			continue
		}
		items = append(items, t)
	}
	cats := []map[string]any{}
	for _, c := range edi.Categories {
		cats = append(cats, map[string]any{"key": c, "count": counts[c]})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items), "categories": cats})
	return nil
}

// StoreDoc validates and stores a document inside tx.
func StoreDoc(ctx context.Context, tx pgx.Tx, direction string, partnerID, related *uuid.UUID, d edi.Document) (Doc, error) {
	if err := d.Validate(); err != nil {
		return Doc{}, httpx.BadRequest(err.Error())
	}
	total := d.Total()
	status := "draft"
	if direction == "inbound" {
		status = "received"
	}
	doc, err := db.One[Doc](ctx, tx, `insert into b2b_document (id, tenant_id, type, direction, partner_id, number, status, payload, total, currency, related_id)
		values ($1, app_tenant(), $2,$3,$4,$5,$6,$7,$8,$9,$10) returning `+docCols,
		ids.New(), d.Type, direction, partnerID, d.Number, status, d, total, d.Currency, related)
	if err != nil {
		return doc, err
	}
	_, err = audit.Write(ctx, tx, audit.Record{Action: "b2b." + d.Type + "." + direction, Entity: "b2b_document", EntityID: doc.ID.String(), After: map[string]any{"number": d.Number, "lines": len(d.Lines), "total": total}})
	return doc, err
}

func (m *Module) createDoc(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Type      string       `json:"type"`
		PartnerID *uuid.UUID   `json:"partnerId"`
		RelatedID *uuid.UUID   `json:"relatedId"`
		Payload   edi.Document `json:"payload"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.Payload.Type == "" {
		in.Payload.Type = in.Type
	}
	if in.Type != "" && in.Type != in.Payload.Type {
		return httpx.BadRequest("type " + in.Type + " does not match payload.type " + in.Payload.Type)
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		if in.PartnerID != nil {
			if _, err := partners.Get(r.Context(), tx, *in.PartnerID); err != nil {
				return httpx.BadRequest("unknown partnerId")
			}
		}
		d, err := StoreDoc(r.Context(), tx, "outbound", in.PartnerID, in.RelatedID, in.Payload)
		if err == nil {
			httpx.JSON(w, http.StatusCreated, d)
		}
		return err
	})
}

// importDoc parses an inbound X12 or EDIFACT message of any registry type that has a reader
// (see GET /b2b/types, formats), or maps a CSV, JSON, XML, X12, EDIFACT or fixed-width file. For EDIFACT, "type"
// names the registry type when several types share a message and document name code.
func (m *Module) importDoc(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Format        string     `json:"format"`
		Content       string     `json:"content"`
		ContentBase64 string     `json:"contentBase64"`
		PartnerID     *uuid.UUID `json:"partnerId"`
		MappingID     *uuid.UUID `json:"mappingId"`
		Type          string     `json:"type"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	var d edi.Document
	var err error
	f := strings.ToLower(in.Format)
	// X12 and EDIFACT with a mappingId go through the mapping studio (a partner's own layout);
	// without one they are parsed natively into the registry type.
	if (f == "x12" || f == "edifact") && in.MappingID != nil {
		content, err := fileContent(in.Content, in.ContentBase64)
		if err != nil {
			return err
		}
		return m.importMapped(w, r, in.MappingID, in.PartnerID, f, content)
	}
	switch f {
	case "csv", "json", "xml", "flat", "mapped":
		content, err := fileContent(in.Content, in.ContentBase64)
		if err != nil {
			return err
		}
		return m.importMapped(w, r, in.MappingID, in.PartnerID, f, content)
	case "x12":
		d, err = edi.ParseX12(in.Content)
	case "edifact":
		if in.Type != "" {
			d, err = edi.ParseEDIFACTAs(in.Content, in.Type)
		} else {
			d, err = edi.ParseEDIFACT(in.Content)
		}
	default:
		return httpx.BadRequest("format must be x12, edifact, csv, json, xml, flat or mapped")
	}
	if err != nil {
		return httpx.BadRequest("cannot parse: " + err.Error())
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		doc, err := StoreDoc(r.Context(), tx, "inbound", in.PartnerID, nil, d)
		if err == nil {
			httpx.JSON(w, http.StatusCreated, doc)
		}
		return err
	})
}

func (m *Module) renderDoc(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var d Doc
	if err := m.Tx(r.Context(), func(tx pgx.Tx) error {
		d, err = db.One[Doc](r.Context(), tx, `select `+docCols+` from b2b_document where id=$1`, id)
		return err
	}); err != nil {
		return err
	}
	env := edi.Envelope{Control: int(d.CreatedAt.Unix() % 1_000_000_000)}
	var out, ct string
	typ, _ := edi.Lookup(d.Payload.Type)
	format := strings.ToLower(r.URL.Query().Get("format"))
	if format == "" {
		format = "x12"
	}
	if (format == "x12" && !typ.CanWriteX12()) || (format == "edifact" && !typ.CanWriteEDIFACT()) || (format == "idoc" && !typ.CanWriteIDoc()) {
		return httpx.BadRequest("document type " + d.Payload.Type + " has no " + format + " rendering")
	}
	switch format {
	case "x12":
		out, ct = edi.X12(d.Payload, env), "application/edi-x12"
	case "edifact":
		out, ct = edi.EDIFACT(d.Payload, env), "application/edifact"
	case "idoc":
		out, err = edi.IDoc(d.Payload, env)
		ct = "application/xml"
		if err != nil {
			return err
		}
	default:
		return httpx.BadRequest("format must be x12, edifact or idoc")
	}
	w.Header().Set("Content-Type", ct+"; charset=utf-8")
	_, _ = io.WriteString(w, out)
	return nil
}

// sendDoc delivers a document: to a partner tenant on this node, over federation, or marks
// it sent for EDI pickup (VAN/AS2 gateways fetch the rendered EDI).
func (m *Module) sendDoc(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	ctx := r.Context()
	me := auth.MustFrom(ctx)
	var d Doc
	var p partners.Partner
	err = m.Tx(ctx, func(tx pgx.Tx) error {
		if d, err = db.One[Doc](ctx, tx, `select `+docCols+` from b2b_document where id=$1 and direction='outbound'`, id); err != nil {
			return err
		}
		if d.PartnerID == nil {
			return httpx.BadRequest("document has no partner")
		}
		p, err = partners.Get(ctx, tx, *d.PartnerID)
		return err
	})
	if err != nil {
		return err
	}
	delivery := map[string]any{"at": time.Now().UTC()}
	switch {
	case p.LinkedTenant != nil:
		err = m.DB.Tx(ctx, *p.LinkedTenant, func(tx pgx.Tx) error {
			var back *uuid.UUID
			var pid uuid.UUID
			if tx.QueryRow(ctx, `select id from partner where linked_tenant=$1`, me.TenantID).Scan(&pid) == nil {
				back = &pid
			}
			sctx := auth.With(ctx, auth.System(*p.LinkedTenant))
			in, err := StoreDoc(sctx, tx, "inbound", back, nil, d.Payload)
			delivery["channel"], delivery["remoteId"] = "local", in.ID
			return err
		})
	case p.Endpoints.Federation != "":
		var peer uuid.UUID
		if peer, err = uuid.Parse(p.Endpoints.Federation); err != nil {
			err = httpx.Conflict("partner federation endpoint is not a federation peer id")
		} else {
			delivery["channel"] = "federation"
			delivery["messageId"], err = m.Send(ctx, peer, "b2b.document", d.Payload)
		}
	default:
		delivery["channel"] = "edi-pickup"
	}
	if err != nil {
		return err
	}
	return m.Tx(ctx, func(tx pgx.Tx) error {
		d, err = db.One[Doc](ctx, tx, `update b2b_document set status='sent', sent_at=now(), delivery=$2,
			acked_at=case when $2::jsonb->>'channel' = 'local' then now() else acked_at end where id=$1 returning `+docCols, id, delivery)
		if err != nil {
			return err
		}
		if _, err := audit.Write(ctx, tx, audit.Record{Action: "b2b.send", Entity: "b2b_document", EntityID: id.String(), After: delivery}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, d)
		return nil
	})
}

// --- supplier compliance hub ---

// Cert is a supplier certificate, audit or specification.
type Cert struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	PartnerID  *uuid.UUID `json:"partnerId,omitempty" db:"partner_id"`
	Supplier   string     `json:"supplier" db:"supplier"`
	Category   string     `json:"category" db:"category"`
	Kind       string     `json:"kind" db:"kind"`
	Number     string     `json:"number" db:"number"`
	Issuer     string     `json:"issuer" db:"issuer"`
	ValidFrom  *time.Time `json:"validFrom,omitempty" db:"valid_from"`
	ValidTo    *time.Time `json:"validTo,omitempty" db:"valid_to"`
	Result     string     `json:"result" db:"result"`
	DocumentID *uuid.UUID `json:"documentId,omitempty" db:"document_id"`
	Notes      string     `json:"notes" db:"notes"`
	Status     string     `json:"status" db:"status"`
	CreatedAt  time.Time  `json:"createdAt" db:"created_at"`
}

const certSelect = `select s.id, s.partner_id, coalesce(p.name,'') as supplier, s.category, s.kind, s.number, s.issuer, s.valid_from, s.valid_to, s.result, s.document_id, s.notes,
	case when s.valid_to is null then 'valid' when s.valid_to < current_date then 'expired' when s.valid_to < current_date + 30 then 'expiring' else 'valid' end as status, s.created_at
	from supplier_record s left join partner p on p.id = s.partner_id`

func (m *Module) listCerts(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[Cert](r.Context(), tx, certSelect+` where ($1='' or s.category=$1) order by s.valid_to nulls last`, q.Get("category"))
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[Cert]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) createCert(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		PartnerID  *uuid.UUID `json:"partnerId"`
		Category   string     `json:"category"`
		Kind       string     `json:"kind"`
		Number     string     `json:"number"`
		Issuer     string     `json:"issuer"`
		ValidFrom  string     `json:"validFrom"`
		ValidTo    string     `json:"validTo"`
		Result     string     `json:"result"`
		DocumentID *uuid.UUID `json:"documentId"`
		Notes      string     `json:"notes"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.Kind == "" {
		return httpx.BadRequest("kind is required (e.g. GMP, GDP, ISO 22000, BRCGS, organic, audit, spec)")
	}
	if in.Category == "" {
		in.Category = "certificate"
	}
	date := func(s string) (*time.Time, error) {
		if s == "" {
			return nil, nil
		}
		if len(s) > 10 {
			s = s[:10]
		}
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return nil, httpx.BadRequest("dates must be YYYY-MM-DD")
		}
		return &t, nil
	}
	vf, err := date(in.ValidFrom)
	if err != nil {
		return err
	}
	vt, err := date(in.ValidTo)
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		id := ids.New()
		if _, err := tx.Exec(r.Context(), `insert into supplier_record (id, tenant_id, partner_id, category, kind, number, issuer, valid_from, valid_to, result, document_id, notes)
			values ($1, app_tenant(), $2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, id, in.PartnerID, in.Category, in.Kind, in.Number, in.Issuer, vf, vt, in.Result, in.DocumentID, in.Notes); err != nil {
			return err
		}
		c, err := db.One[Cert](r.Context(), tx, certSelect+` where s.id=$1`, id)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "supplier.record.create", Entity: "supplier_record", EntityID: id.String(), After: c}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, c)
		return nil
	})
}

// ExpiryAlerts raises an alert for each certificate expiring within 30 days (deduplicated).
func (m *Module) ExpiryAlerts(ctx context.Context) error {
	return m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into alert (id, tenant_id, kind, severity, title, detail, entity, entity_id, dedupe_key)
			select gen_random_uuid(), s.tenant_id, 'supplier.certificate_expiring', case when s.valid_to < current_date then 'error' else 'warning' end,
				coalesce(p.name, 'Supplier') || ': ' || s.kind || ' certificate ' || case when s.valid_to < current_date then 'expired' else 'expires' end || ' on ' || s.valid_to,
				jsonb_build_object('recordId', s.id, 'validTo', s.valid_to, 'kind', s.kind, 'number', s.number),
				'supplier_record', s.id::text, 'cert:' || s.id || ':' || s.valid_to
			from supplier_record s left join partner p on p.id = s.partner_id
			where s.valid_to is not null and s.valid_to < current_date + 30
			on conflict do nothing`)
		return err
	})
}

// --- document exchange ---

type file struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	PartnerID   *uuid.UUID `json:"partnerId,omitempty" db:"partner_id"`
	Direction   string     `json:"direction" db:"direction"`
	Title       string     `json:"title" db:"title"`
	ContentType string     `json:"contentType" db:"content_type"`
	SHA256      string     `json:"sha256" db:"sha256"`
	Size        int        `json:"size" db:"size"`
	CreatedAt   time.Time  `json:"createdAt" db:"created_at"`
}

const fileCols = `id, partner_id, direction, title, content_type, sha256, size, created_at`

const maxFile = 10 << 20

func (m *Module) listFiles(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[file](r.Context(), tx, `select `+fileCols+` from shared_document order by created_at desc limit 500`)
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[file]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) uploadFile(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Title       string     `json:"title"`
		ContentType string     `json:"contentType"`
		Content     string     `json:"content"` // base64
		PartnerID   *uuid.UUID `json:"partnerId"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	b, err := base64.StdEncoding.DecodeString(in.Content)
	if err != nil || len(b) == 0 {
		return httpx.BadRequest("content must be base64")
	}
	if len(b) > maxFile {
		return httpx.BadRequest("documents are limited to 10 MiB")
	}
	if in.Title == "" {
		return httpx.BadRequest("title is required")
	}
	if in.ContentType == "" {
		in.ContentType = http.DetectContentType(b)
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		f, err := storeFile(r.Context(), tx, "internal", in.PartnerID, in.Title, in.ContentType, b)
		if err == nil {
			httpx.JSON(w, http.StatusCreated, f)
		}
		return err
	})
}

func storeFile(ctx context.Context, tx pgx.Tx, direction string, partner *uuid.UUID, title, ct string, b []byte) (file, error) {
	sum := sha256.Sum256(b)
	f, err := db.One[file](ctx, tx, `insert into shared_document (id, tenant_id, partner_id, direction, title, content_type, sha256, size, content) values ($1, app_tenant(), $2,$3,$4,$5,$6,$7,$8)
		returning `+fileCols, ids.New(), partner, direction, title, ct, hex.EncodeToString(sum[:]), len(b), b)
	if err != nil {
		return f, err
	}
	_, err = audit.Write(ctx, tx, audit.Record{Action: "document." + direction, Entity: "shared_document", EntityID: f.ID.String(), After: f})
	return f, err
}

func (m *Module) downloadFile(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		var title, ct string
		var b []byte
		if err := tx.QueryRow(r.Context(), `select title, content_type, content from shared_document where id=$1`, id).Scan(&title, &ct, &b); err != nil {
			return db.MapErr(err)
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", strings.ReplaceAll(title, `"`, "")))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(b)
		return nil
	})
}

// shareFile sends a copy to a partner tenant on this node.
func (m *Module) shareFile(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		PartnerID uuid.UUID `json:"partnerId"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	me := auth.MustFrom(ctx)
	var title, ct string
	var b []byte
	var p partners.Partner
	err = m.Tx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `select title, content_type, content from shared_document where id=$1`, id).Scan(&title, &ct, &b); err != nil {
			return db.MapErr(err)
		}
		p, err = partners.Get(ctx, tx, in.PartnerID)
		return err
	})
	if err != nil {
		return err
	}
	if p.LinkedTenant == nil {
		return httpx.Conflict("partner is not on this node; use federation")
	}
	var remote file
	err = m.DB.Tx(ctx, *p.LinkedTenant, func(tx pgx.Tx) error {
		var back *uuid.UUID
		var pid uuid.UUID
		if tx.QueryRow(ctx, `select id from partner where linked_tenant=$1`, me.TenantID).Scan(&pid) == nil {
			back = &pid
		}
		remote, err = storeFile(auth.With(ctx, auth.System(*p.LinkedTenant)), tx, "inbound", back, title, ct, b)
		return err
	})
	if err != nil {
		return err
	}
	err = m.Tx(ctx, func(tx pgx.Tx) error {
		_, err := audit.Write(ctx, tx, audit.Record{Action: "document.share", Entity: "shared_document", EntityID: id.String(), After: map[string]any{"partner": p.Name, "sha256": remote.SHA256}})
		return err
	})
	if err == nil {
		httpx.JSON(w, http.StatusOK, map[string]any{"shared": true, "sha256": remote.SHA256})
	}
	return err
}

var _ = json.Marshal
