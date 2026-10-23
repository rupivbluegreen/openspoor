package trace

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/checkpoint"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/kernel"
)

// defaultSettleLag keeps events whose transaction may still be committing out of a batch: a batch
// covers record times up to now minus this lag. An event that becomes visible later with an
// older record time is not covered by any batch; the online verification reports a batch whose
// stored events no longer match its root.
const defaultSettleLag = 30 * time.Second

// maxEvidenceLeaves bounds an evidence export.
const maxEvidenceLeaves = 1000000

const nodeKeyAAD = "federation-node"

// ErrNothingNew is returned when neither the audit chain nor the events changed since the
// last checkpoint.
var ErrNothingNew = errors.New("nothing changed since the last checkpoint")

type nodeKey struct {
	ID   string
	Pub  ed25519.PublicKey
	priv ed25519.PrivateKey
}

// didFor mirrors the did:web identifier the link module publishes for this installation.
func didFor(publicURL string) string {
	u, err := url.Parse(publicURL)
	if err != nil || u.Host == "" {
		return "did:web:localhost"
	}
	did := "did:web:" + strings.ReplaceAll(u.Host, ":", "%3A")
	if p := strings.Trim(u.Path, "/"); p != "" {
		did += ":" + strings.ReplaceAll(p, "/", ":")
	}
	return did
}

// loadNodeKey reads the installation's Ed25519 key (the one the federation identity uses),
// creating it on first use exactly as the link module does.
func (m *Module) loadNodeKey(ctx context.Context) (*nodeKey, error) {
	master := m.Keys.Master()
	var did string
	var pub, sealed []byte
	err := m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select did, public_key, private_key from federation_node where id=1`).Scan(&did, &pub, &sealed)
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		p, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		did, pub, sealed = didFor(m.Cfg.PublicURL), p, master.Seal(priv, []byte(nodeKeyAAD))
		if _, err := tx.Exec(ctx, `insert into federation_node (id, did, public_key, private_key) values (1, $1, $2, $3) on conflict (id) do nothing`, did, pub, sealed); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `select did, public_key, private_key from federation_node where id=1`).Scan(&did, &pub, &sealed)
	})
	if err != nil {
		return nil, err
	}
	priv, err := master.Open(sealed, []byte(nodeKeyAAD))
	if err != nil {
		return nil, fmt.Errorf("unseal node key: %w", err)
	}
	return &nodeKey{ID: did, Pub: pub, priv: priv}, nil
}

// CheckpointRow is a stored checkpoint without its envelope.
type CheckpointRow struct {
	Seq        int64      `json:"seq"`
	CreatedAt  time.Time  `json:"createdAt"`
	AuditSeq   int64      `json:"auditSeq"`
	AuditHash  string     `json:"auditHash"`
	EventsFrom *time.Time `json:"eventsFrom,omitempty"`
	EventsTo   time.Time  `json:"eventsTo"`
	EventCount int64      `json:"eventCount"`
	EventRoot  string     `json:"eventRoot"`
	DocHash    string     `json:"docHash"`
	KeyID      string     `json:"keyId"`
}

const cpCols = `seq, created_at, audit_seq, audit_hash, events_from, events_to, event_count, event_root, doc_hash, envelope->'checkpoint'->>'keyId'`

func scanCheckpoint(row pgx.Row) (CheckpointRow, error) {
	var c CheckpointRow
	err := row.Scan(&c.Seq, &c.CreatedAt, &c.AuditSeq, &c.AuditHash, &c.EventsFrom, &c.EventsTo, &c.EventCount, &c.EventRoot, &c.DocHash, &c.KeyID)
	return c, err
}

// eventLeaves streams the leaves of events recorded in (from, to].
func eventLeaves(ctx context.Context, tx pgx.Tx, from *time.Time, to time.Time, limit int) ([]checkpoint.Leaf, error) {
	f := time.Unix(0, 0).UTC()
	if from != nil {
		f = *from
	}
	rows, err := tx.Query(ctx, `select event_id, record_time, doc from epcis_event
		where tenant_id = app_tenant() and record_time > $1 and record_time <= $2 order by record_time, event_id, id limit $3`, f, to, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []checkpoint.Leaf
	for rows.Next() {
		var id string
		var rt time.Time
		var doc []byte
		if err := rows.Scan(&id, &rt, &doc); err != nil {
			return nil, err
		}
		out = append(out, checkpoint.Leaf{EventID: id, RecordTime: rt.UTC().Format(time.RFC3339Nano), Hash: hexOf(checkpoint.LeafHash(id, rt, doc))})
		if len(out) > limit {
			return nil, fmt.Errorf("more than %d events in one batch", limit)
		}
	}
	return out, rows.Err()
}

func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2], out[i*2+1] = digits[c>>4], digits[c&15]
	}
	return string(out)
}

// CreateCheckpoint signs the audit chain head and the events recorded since the previous
// checkpoint, stores the envelope and writes an audit entry. Callers are in a tenant transaction.
func (m *Module) CreateCheckpoint(ctx context.Context, tx pgx.Tx, tenant uuid.UUID, actor string, now time.Time) (CheckpointRow, error) {
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('checkpoint:'||$1, 0))`, tenant.String()); err != nil {
		return CheckpointRow{}, err
	}
	prev, err := scanCheckpoint(tx.QueryRow(ctx, `select `+cpCols+` from integrity_checkpoint where tenant_id=$1 order by seq desc limit 1`, tenant))
	havePrev := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return CheckpointRow{}, err
	}
	var headSeq int64
	var headHash string
	err = tx.QueryRow(ctx, `select seq, hash from audit_entry where tenant_id=$1 order by seq desc limit 1`, tenant).Scan(&headSeq, &headHash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return CheckpointRow{}, err
	}
	to := now.UTC().Add(-m.SettleLag).Truncate(time.Microsecond)
	var from *time.Time
	if havePrev {
		from = &prev.EventsTo
		if to.Before(prev.EventsTo) {
			to = prev.EventsTo
		}
	}
	leaves, err := eventLeaves(ctx, tx, from, to, maxEvidenceLeaves)
	if err != nil {
		return CheckpointRow{}, err
	}
	if havePrev && len(leaves) == 0 {
		// Checkpoints are themselves audited; only other entries count as a change.
		var others int
		if err := tx.QueryRow(ctx, `select count(*) from audit_entry where tenant_id=$1 and seq > $2 and action <> 'trace.checkpoint.create'`, tenant, prev.AuditSeq).Scan(&others); err != nil {
			return CheckpointRow{}, err
		}
		if others == 0 {
			return prev, ErrNothingNew
		}
	}
	root, err := checkpoint.RootOfLeaves(leaves)
	if err != nil {
		return CheckpointRow{}, err
	}
	key, err := m.loadNodeKey(ctx)
	if err != nil {
		return CheckpointRow{}, err
	}
	doc := checkpoint.Doc{TenantID: tenant.String(), Seq: prev.Seq + 1, CreatedAt: now.UTC().Format(time.RFC3339Nano),
		Audit:  checkpoint.AuditHead{Seq: headSeq, Hash: headHash},
		Events: checkpoint.EventBatch{To: to.Format(time.RFC3339Nano), Count: int64(len(leaves)), Root: root, Algorithm: checkpoint.RootAlgorithm}}
	if havePrev {
		doc.Previous = &checkpoint.Link{Seq: prev.Seq, DocHash: prev.DocHash}
		doc.Events.From = prev.EventsTo.UTC().Format(time.RFC3339Nano)
	}
	env, err := checkpoint.Sign(doc, key.ID, key.priv)
	if err != nil {
		return CheckpointRow{}, err
	}
	dh, err := checkpoint.DocHash(env.Checkpoint)
	if err != nil {
		return CheckpointRow{}, err
	}
	blob, _ := json.Marshal(env)
	if _, err := tx.Exec(ctx, `insert into integrity_checkpoint (tenant_id, seq, created_at, audit_seq, audit_hash, events_from, events_to, event_count, event_root, doc_hash, envelope)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, tenant, doc.Seq, now.UTC().Truncate(time.Microsecond), headSeq, headHash, from, to, len(leaves), root, dh, blob); err != nil {
		return CheckpointRow{}, err
	}
	if _, err := audit.WriteAs(ctx, tx, tenant, actor, audit.Record{Action: "trace.checkpoint.create", Entity: "integrity_checkpoint", EntityID: strconv.FormatInt(doc.Seq, 10),
		After: map[string]any{"seq": doc.Seq, "auditSeq": headSeq, "auditHash": headHash, "eventCount": len(leaves), "eventRoot": root, "docHash": dh, "keyId": key.ID}}); err != nil {
		return CheckpointRow{}, err
	}
	return scanCheckpoint(tx.QueryRow(ctx, `select `+cpCols+` from integrity_checkpoint where tenant_id=$1 and seq=$2`, tenant, doc.Seq))
}

// Schedules implements kernel.Scheduled: signed checkpoints and the optional lot reconciliation.
func (m *Module) Schedules() []kernel.Schedule {
	return []kernel.Schedule{
		{Name: "trace.checkpoint", Every: time.Hour, Run: m.autoCheckpoint},
		{Name: "trace.balance", Every: 6 * time.Hour, Run: m.autoBalance},
	}
}

// autoCheckpoint signs a checkpoint for every tenant whose last one is older than its interval.
func (m *Module) autoCheckpoint(ctx context.Context) error {
	type due struct {
		tenant uuid.UUID
		hours  int
	}
	var tenants []due
	err := m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select t.id, coalesce(s.checkpoint_hours, 24) from tenant t left join trace_setting s on s.tenant_id = t.id
			where coalesce(s.checkpoint_hours, 24) > 0`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d due
			if err := rows.Scan(&d.tenant, &d.hours); err != nil {
				return err
			}
			tenants = append(tenants, d)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	for _, d := range tenants {
		err := m.DB.Tx(ctx, d.tenant, func(tx pgx.Tx) error {
			var last *time.Time
			if err := tx.QueryRow(ctx, `select max(created_at) from integrity_checkpoint where tenant_id=$1`, d.tenant).Scan(&last); err != nil {
				return err
			}
			if last != nil && time.Since(*last) < time.Duration(d.hours)*time.Hour {
				return nil
			}
			_, err := m.CreateCheckpoint(ctx, tx, d.tenant, "system", time.Now())
			if errors.Is(err, ErrNothingNew) {
				return nil
			}
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) integrityRoutes(r chi.Router) {
	r.Get("/integrity/node", httpx.Handler(m.getNode))
	r.Get("/integrity/checkpoints", httpx.Handler(m.listCheckpoints))
	r.Get("/integrity/checkpoints/{seq}", httpx.Handler(m.exportCheckpoint))
	r.Get("/integrity/verify", httpx.Handler(m.verifyIntegrity))
	r.With(auth.Require(auth.RoleAdmin, auth.RoleCompliance)).Post("/integrity/checkpoints", httpx.Handler(m.createCheckpoint))
}

func (m *Module) getNode(w http.ResponseWriter, r *http.Request) error {
	k, err := m.loadNodeKey(r.Context())
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"keyId": k.ID, "publicKey": checkpoint.PublicKeyString(k.Pub), "algorithm": checkpoint.SigAlgorithm,
		"use": "Pass publicKey to `openspoor verify-checkpoint --pubkey` to check that an exported checkpoint was signed by this node."})
	return nil
}

func (m *Module) listCheckpoints(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `select `+cpCols+` from integrity_checkpoint where tenant_id = app_tenant() order by seq desc limit 200`)
		if err != nil {
			return err
		}
		defer rows.Close()
		items := []CheckpointRow{}
		for rows.Next() {
			c, err := scanCheckpoint(rows)
			if err != nil {
				return err
			}
			items = append(items, c)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, httpx.Page[CheckpointRow]{Items: items, Total: len(items)})
		return nil
	})
}

func (m *Module) createCheckpoint(w http.ResponseWriter, r *http.Request) error {
	p := auth.MustFrom(r.Context())
	var row CheckpointRow
	err := m.Tx(r.Context(), func(tx pgx.Tx) error {
		var err error
		row, err = m.CreateCheckpoint(r.Context(), tx, p.TenantID, p.AuditActor(), time.Now())
		return err
	})
	if errors.Is(err, ErrNothingNew) {
		return httpx.Conflict(fmt.Sprintf("nothing changed since checkpoint %d", row.Seq))
	}
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, row)
	return nil
}

func loadEnvelope(ctx context.Context, tx pgx.Tx, seq int64) (checkpoint.Envelope, CheckpointRow, error) {
	var env checkpoint.Envelope
	var blob []byte
	var c CheckpointRow
	err := tx.QueryRow(ctx, `select `+cpCols+`, envelope from integrity_checkpoint where tenant_id = app_tenant() and seq=$1`, seq).
		Scan(&c.Seq, &c.CreatedAt, &c.AuditSeq, &c.AuditHash, &c.EventsFrom, &c.EventsTo, &c.EventCount, &c.EventRoot, &c.DocHash, &c.KeyID, &blob)
	if errors.Is(err, pgx.ErrNoRows) {
		return env, c, httpx.NotFound("no such checkpoint")
	}
	if err != nil {
		return env, c, err
	}
	return env, c, json.Unmarshal(blob, &env)
}

// bundleFor builds an export; with evidence it attaches the audit slice since the previous
// checkpoint and the event leaves of the batch, read from the database now.
func bundleFor(ctx context.Context, tx pgx.Tx, tenant uuid.UUID, seq int64, evidence bool) (checkpoint.Bundle, error) {
	env, c, err := loadEnvelope(ctx, tx, seq)
	if err != nil {
		return checkpoint.Bundle{}, err
	}
	b := checkpoint.Bundle{Envelope: env}
	if !evidence {
		return b, nil
	}
	ev := &checkpoint.Evidence{Audit: []audit.Entry{}}
	if seq > 1 {
		_, pc, err := loadEnvelope(ctx, tx, seq-1)
		if err != nil {
			return b, err
		}
		ev.AuditFromSeq, ev.AuditFromHash = pc.AuditSeq, pc.AuditHash
	}
	rows, err := tx.Query(ctx, `select id, tenant_id, seq, at, actor, action, entity, entity_id, coalesce(before,'null'), coalesce(after,'null'), reason, prev_hash, hash
		from audit_entry where tenant_id=$1 and seq > $2 and seq <= $3 order by seq`, tenant, ev.AuditFromSeq, c.AuditSeq)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	for rows.Next() {
		var e audit.Entry
		if err := rows.Scan(&e.ID, &e.TenantID, &e.Seq, &e.At, &e.Actor, &e.Action, &e.Entity, &e.EntityID, &e.Before, &e.After, &e.Reason, &e.PrevHash, &e.Hash); err != nil {
			return b, err
		}
		if string(e.Before) == "null" {
			e.Before = nil
		}
		if string(e.After) == "null" {
			e.After = nil
		}
		ev.Audit = append(ev.Audit, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return b, err
	}
	if ev.Leaves, err = eventLeaves(ctx, tx, c.EventsFrom, c.EventsTo, maxEvidenceLeaves); err != nil {
		return b, err
	}
	b.Evidence = ev
	return b, nil
}

func (m *Module) exportCheckpoint(w http.ResponseWriter, r *http.Request) error {
	seq, err := strconv.ParseInt(chi.URLParam(r, "seq"), 10, 64)
	if err != nil || seq < 1 {
		return httpx.BadRequest("invalid checkpoint number")
	}
	evidence := r.URL.Query().Get("evidence") == "1" || r.URL.Query().Get("evidence") == "true"
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		b, err := bundleFor(r.Context(), tx, auth.MustFrom(r.Context()).TenantID, seq, evidence)
		if err != nil {
			return err
		}
		name := fmt.Sprintf("checkpoint-%d", seq)
		if evidence {
			name += "-evidence"
		}
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.json"`)
		httpx.JSON(w, http.StatusOK, b)
		return nil
	})
}

// IntegrityReport is the online verification result.
type IntegrityReport struct {
	OK          bool                `json:"ok"`
	AuditChain  AuditChainResult    `json:"auditChain"`
	Checkpoints []checkpoint.Result `json:"checkpoints"`
	Notes       []string            `json:"notes"`
}

// AuditChainResult is the walk over the live audit chain.
type AuditChainResult struct {
	Checked  int64 `json:"checked"`
	BrokenAt int64 `json:"brokenAt"`
	OK       bool  `json:"ok"`
}

func (m *Module) verifyIntegrity(w http.ResponseWriter, r *http.Request) error {
	last := httpx.IntQuery(r, "last", 10, 1, 200)
	ctx := r.Context()
	p := auth.MustFrom(ctx)
	return m.Tx(ctx, func(tx pgx.Tx) error {
		rep := IntegrityReport{OK: true, Checkpoints: []checkpoint.Result{}, Notes: []string{
			"Checks the live database against the signed checkpoints: audit chain, signatures, audit hash at each checkpointed sequence, and the event root recomputed from the stored events.",
			"A checkpoint proves the state at signing time only up to the signer's own clock; no external timestamp is used. TODO(spec): optional RFC 3161 timestamp authority.",
		}}
		checked, broken, err := audit.Verify(ctx, tx, p.TenantID)
		if err != nil {
			return err
		}
		rep.AuditChain = AuditChainResult{Checked: checked, BrokenAt: broken, OK: broken == 0}
		rep.OK = broken == 0
		key, err := m.loadNodeKey(ctx)
		if err != nil {
			return err
		}
		var seqs []int64
		rows, err := tx.Query(ctx, `select seq from integrity_checkpoint where tenant_id=$1 order by seq desc limit $2`, p.TenantID, last)
		if err != nil {
			return err
		}
		for rows.Next() {
			var s int64
			if err := rows.Scan(&s); err != nil {
				rows.Close()
				return err
			}
			seqs = append(seqs, s)
		}
		rows.Close()
		for i := len(seqs) - 1; i >= 0; i-- {
			b, err := bundleFor(ctx, tx, p.TenantID, seqs[i], true)
			if err != nil {
				return err
			}
			res := checkpoint.Verify(b, key.Pub)
			// The audit entry the checkpoint names must still be the one in the live chain.
			var live string
			err = tx.QueryRow(ctx, `select hash from audit_entry where tenant_id=$1 and seq=$2`, p.TenantID, b.Checkpoint.Audit.Seq).Scan(&live)
			switch {
			case b.Checkpoint.Audit.Seq == 0:
			case errors.Is(err, pgx.ErrNoRows):
				res.Checks = append(res.Checks, checkpoint.Check{Name: "live audit head", OK: false, Detail: "the audited entry no longer exists"})
				res.OK = false
			case err != nil:
				return err
			default:
				ok := live == b.Checkpoint.Audit.Hash
				res.Checks = append(res.Checks, checkpoint.Check{Name: "live audit head", OK: ok, Detail: "hash of the audit entry at the checkpointed sequence"})
				res.OK = res.OK && ok
			}
			if !res.OK {
				rep.OK = false
			}
			rep.Checkpoints = append(rep.Checkpoints, res)
		}
		httpx.JSON(w, http.StatusOK, rep)
		return nil
	})
}
