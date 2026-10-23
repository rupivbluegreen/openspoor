package verify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"strconv"
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
	"github.com/openspoor/openspoor/internal/serialization"
	"github.com/openspoor/openspoor/pkg/gs1"
)

// ConsumerInfo is what the brand owner publishes for patients and shoppers who scan a pack.
type ConsumerInfo struct {
	GTIN         string        `json:"gtin" db:"gtin"`
	Lang         string        `json:"lang" db:"lang"`
	LeafletURL   string        `json:"leafletUrl" db:"leaflet_url"`
	LeafletText  string        `json:"leafletText" db:"leaflet_text"`
	Allergens    []string      `json:"allergens" db:"allergens"`
	Origin       string        `json:"origin" db:"origin"`
	Certificates []Certificate `json:"certificates" db:"certificates"`
	UpdatedAt    time.Time     `json:"updatedAt" db:"updated_at"`
}

// Certificate is a claim shown on the consumer page (organic, halal, origin), with its issuer.
type Certificate struct {
	Name   string `json:"name"`
	Issuer string `json:"issuer,omitempty"`
	URL    string `json:"url,omitempty"`
}

const infoCols = `gtin, lang, leaflet_url, leaflet_text, allergens, origin, certificates, updated_at`

// readerLang picks the page language: an explicit two-letter code, else the first
// Accept-Language tag, else English.
func readerLang(explicit, acceptLanguage string) string {
	for _, c := range append([]string{explicit}, strings.Split(acceptLanguage, ",")...) {
		c = strings.ToLower(strings.TrimSpace(strings.SplitN(strings.SplitN(c, ";", 2)[0], "-", 2)[0]))
		if len(c) == 2 && c[0] >= 'a' && c[0] <= 'z' && c[1] >= 'a' && c[1] <= 'z' {
			return c
		}
	}
	return "en"
}

// infoFor returns the information in lang, falling back to English and then to any language.
func infoFor(ctx context.Context, tx pgx.Tx, gtin, lang string) (*ConsumerInfo, error) {
	ci, err := db.One[ConsumerInfo](ctx, tx, `select `+infoCols+` from consumer_info where tenant_id = app_tenant() and gtin = $1
		order by (lang = $2) desc, (lang = 'en') desc, lang limit 1`, gtin, lang)
	if errors.Is(err, db.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ci, nil
}

// count adds one anonymous scan to the brand owner's daily statistics.
func (m *Module) count(ctx context.Context, tenant uuid.UUID, gtin, outcome string) error {
	return m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into scan_stat (tenant_id, gtin, day, outcome, count) values (app_tenant(), $1, current_date, $2, 1)
			on conflict (tenant_id, gtin, day, outcome) do update set count = scan_stat.count + 1`, gtin, outcome)
		return err
	})
}

// owner finds the brand owner of a GTIN on this node (the same rule as Scan).
func (m *Module) owner(ctx context.Context, gtin string) (uuid.UUID, error) {
	var tenant uuid.UUID
	err := m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select p.tenant_id from product p left join serial_pool s on s.tenant_id=p.tenant_id and s.gtin=p.gtin
			where p.gtin=$1 order by (s.id is null), p.created_at limit 1`, gtin).Scan(&tenant)
	})
	if err != nil {
		return uuid.Nil, httpx.NotFound("this product is not registered on this network")
	}
	return tenant, nil
}

var reportReasons = map[string]bool{"packaging": true, "seal": true, "effect": true, "price": true, "seller": true, "other": true}

// report is the public "report a suspect product" button. It stores no personal data:
// the brand owner sees the pack, the reason and an optional country.
func (m *Module) report(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Code    string `json:"code"`
		Reason  string `json:"reason"`
		Details string `json:"details"`
		Country string `json:"country"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	pack, err := serialization.ParseCode(in.Code)
	if err != nil {
		return httpx.BadRequest("could not read the code: " + err.Error())
	}
	if !reportReasons[in.Reason] {
		return httpx.BadRequest("reason must be one of packaging, seal, effect, price, seller, other")
	}
	if len(in.Details) > 1000 || (in.Country != "" && len(in.Country) != 2) {
		return httpx.BadRequest("details are limited to 1000 characters; country is a two-letter code")
	}
	tenant, err := m.owner(r.Context(), pack.GTIN)
	if err != nil {
		return err
	}
	id := ids.New()
	err = m.DB.Tx(r.Context(), tenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `insert into consumer_report (id, tenant_id, gtin, serial, lot, reason, details, country)
			values ($1, app_tenant(), $2, $3, $4, $5, $6, upper($7))`, id, pack.GTIN, pack.Serial, pack.Lot, in.Reason, in.Details, in.Country); err != nil {
			return err
		}
		_, err := tx.Exec(r.Context(), `insert into alert (id, tenant_id, kind, severity, title, detail, entity, entity_id, dedupe_key)
			values ($1, app_tenant(), 'verify.consumer_report', 'warning', $2, $3, 'serial', $4, $5) on conflict do nothing`,
			ids.New(), "Consumer reported a suspect pack ("+in.Reason+")", map[string]any{"reportId": id, "pack": pack, "reason": in.Reason, "country": in.Country},
			pack.GTIN+"/"+pack.Serial, "report:"+pack.GTIN+"/"+pack.Serial+":"+in.Reason)
		return err
	})
	if err != nil {
		return err
	}
	if err := m.count(r.Context(), tenant, pack.GTIN, "reported"); err != nil {
		return err
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{"received": true})
	return nil
}

// subscribe records an opt-in recall notice for the pack's lot. The address is used for nothing
// else and is deleted once the notice is sent or the subscription expires.
func (m *Module) subscribe(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Code    string `json:"code"`
		Email   string `json:"email"`
		Consent bool   `json:"consent"`
		Lang    string `json:"lang"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if m.Mail == nil {
		return httpx.Conflict("recall notices are not available on this installation")
	}
	if !in.Consent {
		return httpx.BadRequest("consent is required to store your e-mail address for a recall notice")
	}
	addr, err := mail.ParseAddress(in.Email)
	if err != nil || addr.Address != strings.TrimSpace(in.Email) || len(in.Email) > 254 {
		return httpx.BadRequest("enter a valid e-mail address")
	}
	pack, err := serialization.ParseCode(in.Code)
	if err != nil {
		return httpx.BadRequest("could not read the code: " + err.Error())
	}
	res, tenant, err := m.check(r.Context(), pack, "en")
	if err != nil {
		return err
	}
	if !res.RecallAlerts {
		return httpx.BadRequest("recall notices are available for verified packs with a known batch")
	}
	// Kept at the latest until the pack expires (what the reader consents to), and never beyond two years.
	expires := time.Now().AddDate(2, 0, 0)
	if d, err := time.Parse(time.DateOnly, res.ExpiryDate); err == nil && d.Before(expires) {
		expires = d
	}
	err = m.DB.Tx(r.Context(), tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `insert into recall_subscription (id, tenant_id, gtin, lot, email, token, lang, expires_at)
			values ($1, app_tenant(), $2, $3, lower($4), $5, $6, $7) on conflict (tenant_id, gtin, lot, email) do nothing`,
			ids.New(), pack.GTIN, res.Pack.Lot, addr.Address, ids.Secret(24), readerLang(in.Lang, ""), expires)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{"subscribed": true, "until": expires.Format(time.DateOnly)})
	return nil
}

// unsubscribe deletes a subscription by its token (the link in every notice).
func (m *Module) unsubscribe(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Token string `json:"token"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.Token == "" {
		return httpx.BadRequest("token is required")
	}
	err := m.DB.OperatorTx(r.Context(), func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `delete from recall_subscription where token = $1`, in.Token)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"unsubscribed": true})
	return nil
}

// Schedules implements kernel.Scheduled.
func (m *Module) Schedules() []kernel.Schedule {
	return []kernel.Schedule{{Name: "verify.recall_notices", Every: time.Hour, Run: m.SendRecallNotices}}
}

type pendingNotice struct {
	ID       uuid.UUID `db:"id"`
	TenantID uuid.UUID `db:"tenant_id"`
	GTIN     string    `db:"gtin"`
	Lot      string    `db:"lot"`
	Email    string    `db:"email"`
	Token    string    `db:"token"`
	Product  string    `db:"product"`
}

// SendRecallNotices e-mails subscribers whose lot has been recalled, then deletes each sent
// subscription, and deletes expired ones. Without a mail relay it only deletes expired rows.
func (m *Module) SendRecallNotices(ctx context.Context) error {
	var due []pendingNotice
	err := m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `delete from recall_subscription where expires_at < now()`); err != nil {
			return err
		}
		if m.Mail == nil {
			return nil
		}
		rows, err := tx.Query(ctx, `select r.id, r.tenant_id, r.gtin, r.lot, r.email, r.token, coalesce(p.name, r.gtin) as product
			from recall_subscription r left join product p on p.tenant_id = r.tenant_id and p.gtin = r.gtin
			where exists (select 1 from serial_state s where s.tenant_id = r.tenant_id and s.gtin = r.gtin and s.lot = r.lot and s.status = 'recalled')
			limit 500`)
		if err != nil {
			return err
		}
		due, err = pgx.CollectRows(rows, pgx.RowToStructByName[pendingNotice])
		return err
	})
	if err != nil || len(due) == 0 {
		return err
	}
	for _, n := range due {
		body := fmt.Sprintf("Batch %s of %s (GTIN %s) has been recalled by the manufacturer.\n\n"+
			"Do not use it. Return it to your pharmacy or the shop where you bought it, or follow the manufacturer's instructions.\n\n"+
			"You asked to be told about this recall when you checked the pack. This was the only message, and your address has now been deleted.\n"+
			"Reference: %s\n", n.Lot, n.Product, n.GTIN, n.Token[:8])
		if err := m.Mail.Send(ctx, n.Email, "Recall: "+n.Product+" batch "+n.Lot, body); err != nil {
			slog.Warn("recall notice not sent", "subscription", n.ID, "err", err)
			continue
		}
		err := m.DB.Tx(ctx, n.TenantID, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `delete from recall_subscription where id = $1`, n.ID); err != nil {
				return err
			}
			_, err := audit.WriteAs(ctx, tx, n.TenantID, "system:verify", audit.Record{Action: "verify.recall_notice", Entity: "lot",
				EntityID: n.GTIN + "/" + n.Lot, After: map[string]any{"sent": true}})
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// --- brand owner: consumer information and statistics ---

func (m *Module) listInfo(w http.ResponseWriter, r *http.Request) error {
	return m.list(w, r, func(tx pgx.Tx) (any, error) {
		return db.List[ConsumerInfo](r.Context(), tx, `select `+infoCols+` from consumer_info where tenant_id = app_tenant() order by gtin, lang`)
	})
}

// list runs a tenant read and answers {"items": [...]}.
func (m *Module) list(w http.ResponseWriter, r *http.Request, q func(pgx.Tx) (any, error)) error {
	var items any
	err := m.DB.Tx(r.Context(), auth.MustFrom(r.Context()).TenantID, func(tx pgx.Tx) error {
		var err error
		items, err = q(tx)
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

func (m *Module) putInfo(w http.ResponseWriter, r *http.Request) error {
	gtin, lang := chi.URLParam(r, "gtin"), chi.URLParam(r, "lang")
	if len(gtin) != 14 || !gs1.ValidGTIN(gtin) {
		return httpx.BadRequest("gtin must be a valid 14-digit GTIN")
	}
	if readerLang(lang, "") != lang {
		return httpx.BadRequest("lang must be a two-letter ISO 639-1 code")
	}
	var in ConsumerInfo
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.LeafletURL != "" && !strings.HasPrefix(in.LeafletURL, "https://") {
		return httpx.BadRequest("leafletUrl must be an https:// address")
	}
	for _, c := range in.Certificates {
		if c.Name == "" || (c.URL != "" && !strings.HasPrefix(c.URL, "https://")) {
			return httpx.BadRequest("each certificate needs a name, and its url must be https://")
		}
	}
	if len(in.LeafletText) > 100_000 {
		return httpx.BadRequest("leafletText is limited to 100,000 characters")
	}
	if in.Allergens == nil {
		in.Allergens = []string{}
	}
	if in.Certificates == nil {
		in.Certificates = []Certificate{}
	}
	var out ConsumerInfo
	err := m.DB.Tx(r.Context(), auth.MustFrom(r.Context()).TenantID, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(r.Context(), `select exists(select 1 from product where tenant_id = app_tenant() and gtin = $1)`, gtin).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return httpx.NotFound("no product with this GTIN")
		}
		before, _ := infoFor(r.Context(), tx, gtin, lang)
		var err error
		out, err = db.One[ConsumerInfo](r.Context(), tx, `insert into consumer_info (tenant_id, gtin, lang, leaflet_url, leaflet_text, allergens, origin, certificates, updated_at)
			values (app_tenant(), $1, $2, $3, $4, $5, $6, $7, now())
			on conflict (tenant_id, gtin, lang) do update set leaflet_url = excluded.leaflet_url, leaflet_text = excluded.leaflet_text,
			  allergens = excluded.allergens, origin = excluded.origin, certificates = excluded.certificates, updated_at = now()
			returning `+infoCols, gtin, lang, in.LeafletURL, in.LeafletText, in.Allergens, in.Origin, in.Certificates)
		if err != nil {
			return err
		}
		_, err = audit.Write(r.Context(), tx, audit.Record{Action: "consumer_info.put", Entity: "product", EntityID: gtin + "/" + lang, Before: before, After: out})
		return err
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, out)
	return nil
}

func (m *Module) deleteInfo(w http.ResponseWriter, r *http.Request) error {
	gtin, lang := chi.URLParam(r, "gtin"), chi.URLParam(r, "lang")
	err := m.DB.Tx(r.Context(), auth.MustFrom(r.Context()).TenantID, func(tx pgx.Tx) error {
		before, err := infoFor(r.Context(), tx, gtin, lang)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(r.Context(), `delete from consumer_info where tenant_id = app_tenant() and gtin = $1 and lang = $2`, gtin, lang)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.NotFound("no consumer information for this GTIN and language")
		}
		_, err = audit.Write(r.Context(), tx, audit.Record{Action: "consumer_info.delete", Entity: "product", EntityID: gtin + "/" + lang, Before: before})
		return err
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// stats returns anonymous scan counts per GTIN and outcome over the last `days` (default 30, max 366).
func (m *Module) stats(w http.ResponseWriter, r *http.Request) error {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 366 {
		days = 30
	}
	type row struct {
		GTIN    string `json:"gtin" db:"gtin"`
		Outcome string `json:"outcome" db:"outcome"`
		Count   int64  `json:"count" db:"count"`
	}
	var rows []row
	err := m.DB.Tx(r.Context(), auth.MustFrom(r.Context()).TenantID, func(tx pgx.Tx) error {
		var err error
		rows, err = db.List[row](r.Context(), tx, `select gtin, outcome, sum(count)::bigint as count from scan_stat
			where tenant_id = app_tenant() and day > current_date - $1::int group by gtin, outcome order by gtin, outcome`, days)
		return err
	})
	if err != nil {
		return err
	}
	if rows == nil {
		rows = []row{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"days": days, "items": rows})
	return nil
}

type consumerReport struct {
	ID        uuid.UUID `json:"id" db:"id"`
	GTIN      string    `json:"gtin" db:"gtin"`
	Serial    string    `json:"serial" db:"serial"`
	Lot       string    `json:"lot" db:"lot"`
	Reason    string    `json:"reason" db:"reason"`
	Details   string    `json:"details" db:"details"`
	Country   string    `json:"country" db:"country"`
	CreatedAt time.Time `json:"createdAt" db:"created_at"`
}

func (m *Module) listReports(w http.ResponseWriter, r *http.Request) error {
	return m.list(w, r, func(tx pgx.Tx) (any, error) {
		return db.List[consumerReport](r.Context(), tx, `select id, gtin, serial, lot, reason, details, country, created_at from consumer_report
		where tenant_id = app_tenant() order by created_at desc limit 500`)
	})
}
