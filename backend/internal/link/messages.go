package link

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/internal/partners"
)

// Message tracking: one log over B2B documents, federation messages, TI/TS deliveries and AS2
// messages (each AS2 transmission is also a row of its own, with its receipt state).
// Attempts, last error and the dead-letter state of queued deliveries come from the job
// queue (job.status = 'failed' means retries are exhausted); nothing is copied.

// LogEntry is one tracked message.
type LogEntry struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	Kind        string     `json:"kind" db:"kind"` // b2b | federation | tits | as2
	Direction   string     `json:"direction" db:"direction"`
	Type        string     `json:"type" db:"type"`
	PartnerID   *uuid.UUID `json:"partnerId,omitempty" db:"partner_id"`
	PartnerName string     `json:"partnerName" db:"partner_name"`
	Reference   string     `json:"reference" db:"reference"`
	Channel     string     `json:"channel" db:"channel"`
	// Status: received, queued, retrying, sent, delivered, acknowledged, failed, dead.
	Status    string     `json:"status" db:"status"`
	Attempts  int        `json:"attempts" db:"attempts"`
	LastError string     `json:"lastError" db:"last_error"`
	Retryable bool       `json:"retryable" db:"retryable"`
	CreatedAt time.Time  `json:"createdAt" db:"created_at"`
	SentAt    *time.Time `json:"sentAt,omitempty" db:"sent_at"`
	AckedAt   *time.Time `json:"ackedAt,omitempty" db:"acked_at"`
	UpdatedAt time.Time  `json:"updatedAt" db:"updated_at"`
}

// messageLog is the union behind /messages. TI/TS and AS2 rows are read from the dscsa shipment
// and as2_message tables (read-only; link does not import dscsa).
const messageLog = `with fed as (
	select fm.id, fm.direction, fm.type, fm.msg_id, fm.status as fm_status, fm.detail, fm.created_at, fm.peer_id,
		j.status as job_status, j.attempts, j.last_error, j.updated_at as job_updated
	from federation_message fm left join job j on j.id = fm.job_id and j.tenant_id = fm.tenant_id
), msg as (
	select f.id, 'federation'::text as kind, f.direction, f.type, pp.id as partner_id, coalesce(pp.name, fp.name, '') as partner_name,
		f.msg_id as reference, 'federation'::text as channel,
		case when f.job_status = 'failed' then 'dead'
		     when f.direction = 'outbound' and f.fm_status in ('queued','retrying') and f.job_status in ('queued','running') and coalesce(f.attempts,0) > 0 then 'retrying'
		     else f.fm_status end as status,
		coalesce(f.attempts, case when f.direction = 'inbound' then 0 else 1 end) as attempts,
		coalesce(nullif(f.last_error, ''), f.detail->>'error', '') as last_error,
		coalesce(f.direction = 'outbound' and f.job_status in ('failed','queued'), false) as retryable,
		f.created_at, case when f.direction = 'outbound' then f.created_at end as sent_at,
		case when f.fm_status = 'delivered' then f.job_updated end as acked_at,
		coalesce(f.job_updated, f.created_at) as updated_at
	from fed f
	left join federation_peer fp on fp.id = f.peer_id
	left join lateral (select p.id, p.name from partner p where p.endpoints->>'federation' = f.peer_id::text order by p.created_at limit 1) pp on true
	where f.type <> 'b2b.document'
	union all
	select d.id, 'b2b', d.direction, d.type, d.partner_id, coalesce(p.name, ''), d.number, coalesce(d.delivery->>'channel', case when d.direction = 'inbound' then 'inbound' else '' end),
		case when d.direction = 'inbound' then 'received'
		     when d.acked_at is not null then 'acknowledged'
		     when f.job_status = 'failed' then 'dead'
		     when f.fm_status in ('queued','retrying') and coalesce(f.attempts,0) > 0 then 'retrying'
		     when f.fm_status = 'queued' then 'queued'
		     else 'sent' end,
		coalesce(f.attempts, case when d.direction = 'inbound' then 0 else 1 end),
		coalesce(nullif(f.last_error, ''), f.detail->>'error', ''),
		coalesce(d.acked_at is null and f.job_status in ('failed','queued'), false),
		d.created_at, d.sent_at, d.acked_at, coalesce(f.job_updated, d.acked_at, d.sent_at, d.created_at)
	from b2b_document d
	left join partner p on p.id = d.partner_id
	left join fed f on f.direction = 'outbound' and f.msg_id = d.delivery->>'messageId'
	where d.status <> 'draft'
	union all
	select s.id, 'tits', s.direction, 'tits', s.partner_id, coalesce(p.name, ''), coalesce(nullif(s.asn_number, ''), nullif(s.po_number, ''), s.id::text), s.transport,
		case when s.direction = 'inbound' then 'received' when s.status = 'send_failed' then 'failed' when s.status = 'sending' then 'retrying'
		     when s.response->>'mdn' = 'processed' then 'acknowledged' else 'sent' end,
		case when s.direction = 'inbound' then 0 else greatest(s.send_attempts, 1) end,
		coalesce(s.response->>'error', ''),
		-- A failed delivery can be re-sent; so can one whose retry claim went stale (the retry crashed).
		s.direction = 'outbound' and (s.status = 'send_failed' or (s.status = 'sending' and coalesce((s.response->>'claimedAt')::timestamptz, s.created_at) < now() - interval '5 minutes')),
		s.created_at, s.sent_at, null::timestamptz, coalesce(s.sent_at, s.received_at, s.created_at)
	from shipment s left join partner p on p.id = s.partner_id
	where s.direction = 'inbound' or s.status in ('sent', 'send_failed', 'sending')
	union all
	select a.id, 'as2', a.direction, 'as2', a.partner_id, coalesce(p.name, ''), a.message_id, 'as2',
		case a.status when 'processing' then 'queued' when 'received' then 'received' when 'sent' then 'sent' when 'mdn_pending' then 'sent'
		              when 'delivered' then 'acknowledged' else 'failed' end,
		case when a.direction = 'inbound' then 0 else 1 end,
		a.error, false,
		a.created_at, case when a.direction = 'outbound' then a.created_at end, case when a.status = 'delivered' then a.updated_at end, a.updated_at
	from as2_message a left join partner p on p.id = a.partner_id
)
select id, kind, direction, type, partner_id, partner_name, reference, channel, status, attempts, last_error, retryable, created_at, sent_at, acked_at, updated_at from msg`

func (m *Module) listMessageLog(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	limit := httpx.IntQuery(r, "limit", 200, 1, 1000)
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[LogEntry](r.Context(), tx, messageLog+`
			where ($1 = '' or kind = $1) and ($2 = '' or direction = $2) and ($3 = '' or status = $3) and ($4 = '' or partner_id::text = $4)
			  and ($5 = '' or reference ilike '%'||$5||'%' or partner_name ilike '%'||$5||'%' or type ilike '%'||$5||'%')
			order by created_at desc limit $6`, q.Get("kind"), q.Get("direction"), q.Get("status"), q.Get("partnerId"), strings.TrimSpace(q.Get("q")), limit)
		if err != nil {
			return err
		}
		counts := map[string]int{}
		rows, err := tx.Query(r.Context(), `select status, count(*) from (`+messageLog+`) x group by status`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s string
			var n int
			if err := rows.Scan(&s, &n); err != nil {
				return err
			}
			counts[s] = n
		}
		if err := rows.Err(); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items), "counts": counts})
		return nil
	})
}

// retryMessage puts a dead (or backing-off) delivery back on the queue to run now.
func (m *Module) retryMessage(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	ctx := r.Context()
	var isShipment bool
	if err := m.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select exists (select 1 from shipment where id = $1 and direction = 'outbound')`, id).Scan(&isShipment)
	}); err != nil {
		return err
	}
	if isShipment {
		return m.retryShipment(w, r, id)
	}
	return m.Tx(ctx, func(tx pgx.Tx) error {
		var fmID uuid.UUID
		var jobID *int64
		err := tx.QueryRow(ctx, `select fm.id, fm.job_id from federation_message fm where fm.direction = 'outbound' and (fm.id = $1
			or fm.msg_id = (select delivery->>'messageId' from b2b_document where id = $1 and delivery->>'channel' = 'federation'))`, id).Scan(&fmID, &jobID)
		if errors.Is(err, pgx.ErrNoRows) {
			var exists bool
			_ = tx.QueryRow(ctx, `select exists (`+messageLog+` where id = $1)`, id).Scan(&exists)
			if exists {
				return httpx.Conflict("this message is not delivered through the retry queue")
			}
			return db.ErrNotFound
		}
		if err != nil {
			return err
		}
		if jobID == nil {
			return httpx.Conflict("this message has no delivery job")
		}
		var status string
		if err := tx.QueryRow(ctx, `update job set status = 'queued', attempts = 0, last_error = '', run_at = now(), updated_at = now()
			where id = $1 and tenant_id = app_tenant() and status in ('failed', 'queued') returning status`, *jobID).Scan(&status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.Conflict("the message is already delivered or being delivered")
			}
			return err
		}
		if _, err := tx.Exec(ctx, `update federation_message set status = 'queued', detail = '{}' where id = $1`, fmID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `select pg_notify('openspoor_jobs', $1)`, JobDeliver); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update alert set status = 'resolved', resolved_at = now() where entity = 'federation_message' and entity_id = $1 and status = 'open' and kind = 'link.message_dead'`, fmID.String()); err != nil {
			return err
		}
		if _, err := audit.Write(ctx, tx, audit.Record{Action: "message.retry", Entity: "federation_message", EntityID: fmID.String(), After: map[string]any{"jobId": *jobID}}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusAccepted, map[string]any{"id": id, "status": "queued"})
		return nil
	})
}

// retryShipment re-sends a failed outbound TI/TS through the dscsa outbound path. The send is
// synchronous (the partner's answer is the outcome), so unlike a queued delivery the response
// carries the new status.
func (m *Module) retryShipment(w http.ResponseWriter, r *http.Request, id uuid.UUID) error {
	ctx := r.Context()
	if m.RetryShipment == nil {
		return httpx.Conflict("TI/TS retry is not available on this installation")
	}
	status, resp, err := m.RetryShipment(ctx, id)
	if err != nil {
		return err
	}
	if err := m.Tx(ctx, func(tx pgx.Tx) error {
		if status == "sent" {
			if _, err := tx.Exec(ctx, `update alert set status = 'resolved', resolved_at = now() where entity = 'shipment' and entity_id = $1 and status = 'open' and kind = 'dscsa.send_failed'`, id.String()); err != nil {
				return err
			}
		}
		_, err := audit.Write(ctx, tx, audit.Record{Action: "message.retry", Entity: "shipment", EntityID: id.String(), After: map[string]any{"status": status}})
		return err
	}); err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"id": id, "status": status, "response": resp})
	return nil
}

// ackDoc records a functional acknowledgement (e.g. X12 997 / EDIFACT CONTRL received through
// an EDI gateway) for an outbound document.
func (m *Module) ackDoc(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Reference string `json:"reference"`
	}
	if r.ContentLength != 0 {
		if err := httpx.Decode(r, &in); err != nil {
			return err
		}
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		d, err := db.One[Doc](r.Context(), tx, `select `+docCols+` from b2b_document where id=$1`, id)
		if err != nil {
			return err
		}
		if d.Direction != "outbound" || d.SentAt == nil {
			return httpx.Conflict("only sent outbound documents can be acknowledged")
		}
		if d, err = db.One[Doc](r.Context(), tx, `update b2b_document set acked_at = coalesce(acked_at, now()) where id=$1 returning `+docCols, id); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `update alert set status = 'resolved', resolved_at = now() where entity = 'b2b_document' and entity_id = $1 and status = 'open' and kind = 'link.sla_breach'`, id.String()); err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "b2b.ack", Entity: "b2b_document", EntityID: id.String(), After: map[string]any{"reference": in.Reference}}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, d)
		return nil
	})
}

// --- partner SLAs ---

// SLA is a partner's message service level.
type SLA struct {
	PartnerID   uuid.UUID `json:"partnerId" db:"partner_id"`
	PartnerName string    `json:"partnerName" db:"partner_name"`
	AckHours    int       `json:"ackHours" db:"ack_hours"`
	Enabled     bool      `json:"enabled" db:"enabled"`
	UpdatedAt   time.Time `json:"updatedAt" db:"updated_at"`
}

const slaSelect = `select s.partner_id, p.name as partner_name, s.ack_hours, s.enabled, s.updated_at from partner_sla s join partner p on p.id = s.partner_id`

func (m *Module) listSLAs(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[SLA](r.Context(), tx, slaSelect+` where ($1 = '' or s.partner_id::text = $1) order by p.name`, r.URL.Query().Get("partnerId"))
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[SLA]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) putSLA(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "partnerId")
	if err != nil {
		return err
	}
	var in struct {
		AckHours int   `json:"ackHours"`
		Enabled  *bool `json:"enabled"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.AckHours < 1 || in.AckHours > 720 {
		return httpx.BadRequest("ackHours must be between 1 and 720")
	}
	enabled := in.Enabled == nil || *in.Enabled
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		if _, err := partners.Get(r.Context(), tx, id); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `insert into partner_sla (tenant_id, partner_id, ack_hours, enabled) values (app_tenant(), $1, $2, $3)
			on conflict (tenant_id, partner_id) do update set ack_hours = excluded.ack_hours, enabled = excluded.enabled, updated_at = now()`, id, in.AckHours, enabled); err != nil {
			return err
		}
		s, err := db.One[SLA](r.Context(), tx, slaSelect+` where s.partner_id = $1`, id)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "partner.sla.set", Entity: "partner", EntityID: id.String(), After: s}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, s)
		return nil
	})
}

func (m *Module) deleteSLA(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "partnerId")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		ct, err := tx.Exec(r.Context(), `delete from partner_sla where partner_id = $1`, id)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return db.ErrNotFound
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "partner.sla.delete", Entity: "partner", EntityID: id.String()}); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}

// SLABreaches raises one alert per outbound message that a partner has not acknowledged within
// its SLA: B2B documents without an acknowledgement and federation messages not delivered.
func (m *Module) SLABreaches(ctx context.Context) error {
	return m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into alert (id, tenant_id, kind, severity, title, detail, entity, entity_id, dedupe_key)
			select gen_random_uuid(), d.tenant_id, 'link.sla_breach', 'warning',
				p.name || ': ' || d.type || ' ' || d.number || ' not acknowledged within ' || s.ack_hours || ' h',
				jsonb_build_object('documentId', d.id, 'partnerId', p.id, 'ackHours', s.ack_hours, 'sentAt', d.sent_at),
				'b2b_document', d.id::text, 'sla:' || d.id
			from b2b_document d
			join partner_sla s on s.tenant_id = d.tenant_id and s.partner_id = d.partner_id and s.enabled
			join partner p on p.id = d.partner_id
			where d.direction = 'outbound' and d.sent_at is not null and d.acked_at is null
			  and d.sent_at < now() - make_interval(hours => s.ack_hours)
			on conflict do nothing`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `insert into alert (id, tenant_id, kind, severity, title, detail, entity, entity_id, dedupe_key)
			select gen_random_uuid(), fm.tenant_id, 'link.sla_breach', 'warning',
				p.name || ': federation ' || fm.type || ' not delivered within ' || s.ack_hours || ' h',
				jsonb_build_object('messageId', fm.msg_id, 'partnerId', p.id, 'ackHours', s.ack_hours),
				'federation_message', fm.id::text, 'sla:' || fm.id
			from federation_message fm
			join partner p on p.tenant_id = fm.tenant_id and p.endpoints->>'federation' = fm.peer_id::text
			join partner_sla s on s.tenant_id = fm.tenant_id and s.partner_id = p.id and s.enabled
			where fm.direction = 'outbound' and fm.type <> 'b2b.document' and fm.status <> 'delivered'
			  and fm.created_at < now() - make_interval(hours => s.ack_hours)
			on conflict do nothing`)
		return err
	})
}
