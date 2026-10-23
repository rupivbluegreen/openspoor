package epcis

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/kernel"
	lib "github.com/openspoor/openspoor/pkg/epcis"
)

// archiveBatch bounds one archive run so a backlog drains over several runs.
const archiveBatch = 50

// Schedules implements kernel.Scheduled: copy old captures to S3 as gzipped EPCIS 2.0
// JSON-LD. Events stay in PostgreSQL (queries and the six-year retention are unaffected);
// the archive is the cold, compressed copy the scale plan moves old documents into.
func (m *Module) Schedules() []kernel.Schedule {
	if m.Archive == nil {
		return nil
	}
	return []kernel.Schedule{{Name: "epcis.archive", Every: time.Hour, Run: m.ArchiveOld}}
}

type pendingArchive struct {
	ID        uuid.UUID `db:"id"`
	TenantID  uuid.UUID `db:"tenant_id"`
	CreatedAt time.Time `db:"created_at"`
}

// ArchiveOld archives up to archiveBatch stored captures (accepted or projected) older than ArchiveAfterDays.
func (m *Module) ArchiveOld(ctx context.Context) error {
	if m.Archive == nil {
		return nil
	}
	var todo []pendingArchive
	err := m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id, tenant_id, created_at from epcis_capture
			where status in ('accepted', 'completed') and archive_key is null and created_at < now() - make_interval(days => $1)
			order by created_at limit $2`, m.Cfg.ArchiveAfterDays, archiveBatch)
		if err != nil {
			return err
		}
		todo, err = pgx.CollectRows(rows, pgx.RowToStructByName[pendingArchive])
		return err
	})
	if err != nil {
		return err
	}
	for _, c := range todo {
		if err := m.archiveCapture(ctx, c); err != nil {
			slog.Warn("archive capture failed", "capture", c.ID, "err", err)
		}
	}
	return nil
}

func (m *Module) archiveCapture(ctx context.Context, c pendingArchive) error {
	var events []lib.Event
	err := m.DB.Tx(ctx, c.TenantID, func(tx pgx.Tx) error {
		q := Query{"EQ_captureID": c.ID.String(), "perPage": "1000"}
		for {
			page, next, err := q.Run(ctx, tx)
			if err != nil {
				return err
			}
			events = append(events, page...)
			if next == "" {
				return nil
			}
			q["nextPageToken"] = next
		}
	})
	if err != nil {
		return err
	}
	doc, err := lib.WriteJSON(lib.NewDocument(events))
	if err != nil {
		return err
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(doc); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	key := fmt.Sprintf("%s/epcis/%s/%s.jsonld.gz", c.TenantID, c.CreatedAt.UTC().Format("2006/01"), c.ID)
	if err := m.Archive.Put(ctx, key, "application/ld+json", gz.Bytes()); err != nil {
		return err
	}
	return m.DB.Tx(ctx, c.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `update epcis_capture set archive_key = $2 where id = $1 and archive_key is null`, c.ID, key); err != nil {
			return err
		}
		_, err := audit.WriteAs(ctx, tx, c.TenantID, "system:archive", audit.Record{Action: "epcis.archive", Entity: "epcis_capture",
			EntityID: c.ID.String(), After: map[string]any{"archiveKey": key, "events": len(events), "bytes": gz.Len()}})
		return err
	})
}
