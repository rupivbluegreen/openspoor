// Package audit appends tamper-evident, hash-chained audit entries. Rows are never
// updated or deleted (a database trigger enforces this).
package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/auth"
)

// Entry is one audit record.
type Entry struct {
	ID       int64           `json:"id"`
	TenantID uuid.UUID       `json:"tenantId"`
	Seq      int64           `json:"seq"`
	At       time.Time       `json:"at"`
	Actor    string          `json:"actor"`
	Action   string          `json:"action"`
	Entity   string          `json:"entity"`
	EntityID string          `json:"entityId"`
	Before   json.RawMessage `json:"before,omitempty"`
	After    json.RawMessage `json:"after,omitempty"`
	Reason   string          `json:"reason,omitempty"`
	PrevHash string          `json:"prevHash"`
	Hash     string          `json:"hash"`
}

// Record is the input for Write.
type Record struct {
	Action   string
	Entity   string
	EntityID string
	Before   any
	After    any
	Reason   string
}

// Write appends an entry inside tx. Callers must be in a tenant-scoped transaction.
func Write(ctx context.Context, tx pgx.Tx, rec Record) (Entry, error) {
	p, ok := auth.From(ctx)
	if !ok {
		return Entry{}, fmt.Errorf("audit: no principal")
	}
	return WriteAs(ctx, tx, p.TenantID, p.Actor(), rec)
}

// WriteAs appends an entry for an explicit tenant and actor.
func WriteAs(ctx context.Context, tx pgx.Tx, tenant uuid.UUID, actor string, rec Record) (Entry, error) {
	before, err := marshal(rec.Before)
	if err != nil {
		return Entry{}, err
	}
	after, err := marshal(rec.After)
	if err != nil {
		return Entry{}, err
	}
	// Serialize appends per tenant so the chain has no forks.
	if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(hashtextextended('audit:'||$1, 0))", tenant.String()); err != nil {
		return Entry{}, err
	}
	var prevHash string
	var prevSeq int64
	err = tx.QueryRow(ctx, `select seq, hash from audit_entry where tenant_id=$1 order by seq desc limit 1`, tenant).Scan(&prevSeq, &prevHash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, err
	}
	e := Entry{
		TenantID: tenant, Seq: prevSeq + 1, At: time.Now().UTC().Truncate(time.Microsecond),
		Actor: actor, Action: rec.Action, Entity: rec.Entity, EntityID: rec.EntityID,
		Before: before, After: after, Reason: rec.Reason, PrevHash: prevHash,
	}
	e.Hash = Hash(e)
	err = tx.QueryRow(ctx, `insert into audit_entry (tenant_id, seq, at, actor, action, entity, entity_id, before, after, reason, prev_hash, hash)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) returning id`,
		e.TenantID, e.Seq, e.At, e.Actor, e.Action, e.Entity, e.EntityID, nullJSON(e.Before), nullJSON(e.After), e.Reason, e.PrevHash, e.Hash).Scan(&e.ID)
	return e, err
}

// Hash computes the chain hash of an entry over its content and the previous hash.
func Hash(e Entry) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%d|%s|%s|%s|%s|%s|", e.TenantID, e.Seq, e.At.UTC().Format(time.RFC3339Nano), e.Actor, e.Action, e.Entity, e.EntityID)
	h.Write(canon(e.Before))
	h.Write([]byte("|"))
	h.Write(canon(e.After))
	fmt.Fprintf(h, "|%s|%s", e.Reason, e.PrevHash)
	return hex.EncodeToString(h.Sum(nil))
}

// Verify walks the tenant's chain and returns the first broken sequence number, or 0 if intact.
func Verify(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) (checked int64, brokenAt int64, err error) {
	rows, err := tx.Query(ctx, `select id, tenant_id, seq, at, actor, action, entity, entity_id, coalesce(before,'null'), coalesce(after,'null'), reason, prev_hash, hash
		from audit_entry where tenant_id=$1 order by seq`, tenant)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	prev := ""
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.TenantID, &e.Seq, &e.At, &e.Actor, &e.Action, &e.Entity, &e.EntityID, &e.Before, &e.After, &e.Reason, &e.PrevHash, &e.Hash); err != nil {
			return checked, 0, err
		}
		e.Before, e.After = denull(e.Before), denull(e.After)
		checked++
		if e.PrevHash != prev || Hash(e) != e.Hash {
			return checked, e.Seq, nil
		}
		prev = e.Hash
	}
	return checked, 0, rows.Err()
}

func marshal(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	if raw, ok := v.(json.RawMessage); ok {
		return raw, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return canon(b), nil
}

// canon re-encodes JSON so key order matches what PostgreSQL jsonb returns (sorted keys after round-trip).
func canon(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return b
	}
	out, _ := json.Marshal(v)
	return out
}

func nullJSON(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return []byte(b)
}

func denull(b json.RawMessage) json.RawMessage {
	if string(b) == "null" {
		return nil
	}
	return b
}
