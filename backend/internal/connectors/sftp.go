package connectors

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	connector "github.com/openspoor/connectors-sdk"
	"github.com/openspoor/openspoor/internal/connectors/sftpdrop"
	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
	"github.com/openspoor/openspoor/internal/core/kernel"
	"github.com/openspoor/openspoor/pkg/edi"
)

// CaptureFunc captures an EPCIS document for the tenant of the principal in ctx through the
// EPCIS module's capture path and returns the capture id. Wired by the application.
type CaptureFunc func(ctx context.Context, contentType string, body []byte, source string) (uuid.UUID, error)

// EDIFunc stores a parsed inbound EDI document inside tx and returns its id. Wired by the
// application (B2B documents); when nil, EDI files are parsed and logged only.
type EDIFunc func(ctx context.Context, tx pgx.Tx, d edi.Document) (string, error)

// SFTP inbound limits.
const (
	sftpMaxFilesPerPoll = 100
	sftpPollEvery       = 2 * time.Minute
)

// SFTPFile is one logged inbound file.
type SFTPFile struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	InstanceID uuid.UUID  `json:"instanceId" db:"instance_id"`
	Direction  string     `json:"direction" db:"direction"`
	Name       string     `json:"name" db:"name"`
	Size       int64      `json:"size" db:"size"`
	SHA256     string     `json:"sha256" db:"sha256"`
	Format     string     `json:"format" db:"format"`
	Status     string     `json:"status" db:"status"`
	CaptureID  *uuid.UUID `json:"captureId,omitempty" db:"capture_id"`
	DocumentID string     `json:"documentId,omitempty" db:"document_id"`
	MovedTo    string     `json:"movedTo" db:"moved_to"`
	Error      string     `json:"error,omitempty" db:"error"`
	CreatedAt  time.Time  `json:"createdAt" db:"created_at"`
}

const sftpFileCols = `id, instance_id, direction, name, size, sha256, format, status, capture_id, document_id, moved_to, error, created_at`

// PollResult summarises one inbound poll.
type PollResult struct {
	Processed int        `json:"processed"`
	Failed    int        `json:"failed"`
	Files     []SFTPFile `json:"files"`
}

// Schedules implements kernel.Scheduled: SFTP inbound polling.
func (m *Module) Schedules() []kernel.Schedule {
	return []kernel.Schedule{{Name: "connectors.sftp.inbound", Every: sftpPollEvery, Run: m.pollAllSFTP}}
}

func (m *Module) pollAllSFTP(ctx context.Context) error {
	var insts []Instance
	if err := m.DB.OperatorTx(ctx, func(tx pgx.Tx) error {
		var err error
		insts, err = db.List[Instance](ctx, tx, `select `+instCols+` from connector_instance where enabled and connector=$1`, sftpdrop.ID)
		return err
	}); err != nil {
		return err
	}
	var errs []error
	for _, in := range insts {
		if _, err := m.PollSFTP(auth.With(ctx, auth.System(in.TenantID)), in); err != nil && !errors.Is(err, errBusy) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

var errBusy = errors.New("another poll of this instance is running")

// PollSFTP polls one instance's inbound folder: every file is captured (EPCIS) or parsed
// (EDI), then moved to the archive folder, or to the error folder with an alert on failure.
// ctx must carry a principal of the instance's tenant.
func (m *Module) PollSFTP(ctx context.Context, in Instance) (PollResult, error) {
	res := PollResult{Files: []SFTPFile{}}
	cfg := m.effectiveConfig(ctx, in)
	if cfg["inboundDir"] == "" {
		return res, nil
	}
	conn, err := m.DB.Pool.Acquire(ctx)
	if err != nil {
		return res, err
	}
	defer conn.Release()
	lockKey := "sftp:" + in.ID.String()
	var got bool
	if err := conn.QueryRow(ctx, `select pg_try_advisory_lock(hashtextextended($1, 0))`, lockKey).Scan(&got); err != nil {
		return res, err
	}
	if !got {
		return res, errBusy
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `select pg_advisory_unlock(hashtextextended($1, 0))`, lockKey)
	}()

	sess, err := m.dialSFTP(ctx, cfg)
	if err != nil {
		m.connectionAlert(ctx, in, err)
		return res, err
	}
	defer func() { _ = sess.Close() }()
	m.resolveAlert(ctx, in.TenantID, "sftp-conn:"+in.ID.String())
	files, err := sess.List(cfg["inboundDir"])
	if err != nil {
		m.connectionAlert(ctx, in, err)
		return res, err
	}
	if len(files) > sftpMaxFilesPerPoll {
		files = files[:sftpMaxFilesPerPoll]
	}
	for _, f := range files {
		rec := m.processSFTPFile(ctx, in, cfg, sess, f)
		res.Files = append(res.Files, rec)
		if rec.Status == "processed" {
			res.Processed++
		} else {
			res.Failed++
		}
	}
	return res, nil
}

func (m *Module) dialSFTP(ctx context.Context, cfg connector.Config) (*sftpdrop.Session, error) {
	ep, err := sftpdrop.EndpointFromConfig(cfg)
	if err != nil {
		return nil, err
	}
	return sftpdrop.Dial(ctx, ep)
}

// processSFTPFile handles one file and records the outcome (row, audit entry, alert).
func (m *Module) processSFTPFile(ctx context.Context, in Instance, cfg connector.Config, sess *sftpdrop.Session, f sftpdrop.File) SFTPFile {
	rec := SFTPFile{ID: ids.New(), InstanceID: in.ID, Direction: "inbound", Name: f.Name, Size: f.Size, Status: "processed"}
	data, err := sess.Read(f.Path, sftpdrop.MaxFileSize)
	var ediDoc *edi.Document
	if err == nil {
		sum := sha256.Sum256(data)
		rec.SHA256 = hex.EncodeToString(sum[:])
		rec.Format, ediDoc, rec.CaptureID, err = m.ingest(ctx, in, f.Name, data)
	}
	if err != nil {
		rec.Status, rec.Error = "failed", err.Error()
	}
	dest := cfg["archiveDir"]
	if rec.Status == "failed" {
		dest = cfg["errorDir"]
	}
	if dest != "" {
		if moved, merr := sess.Move(f.Path, dest, time.Now()); merr != nil {
			rec.Status = "failed"
			rec.Error = strings.TrimPrefix(rec.Error+"; ", "; ") + "could not move the file to " + dest + ": " + merr.Error()
		} else {
			rec.MovedTo = moved
		}
	}
	err = m.DB.Tx(ctx, in.TenantID, func(tx pgx.Tx) error {
		if ediDoc != nil && rec.Status == "processed" && m.StoreEDI != nil {
			id, err := m.StoreEDI(ctx, tx, *ediDoc)
			if err != nil {
				return err
			}
			rec.DocumentID = id
		}
		r, err := db.One[SFTPFile](ctx, tx, `insert into sftp_file (id, tenant_id, instance_id, direction, name, size, sha256, format, status, capture_id, document_id, moved_to, error)
			values ($1, app_tenant(), $2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) returning `+sftpFileCols,
			rec.ID, rec.InstanceID, rec.Direction, rec.Name, rec.Size, rec.SHA256, rec.Format, rec.Status, rec.CaptureID, rec.DocumentID, rec.MovedTo, rec.Error)
		if err != nil {
			return err
		}
		rec = r
		if _, err := audit.Write(ctx, tx, audit.Record{Action: "sftp.inbound." + rec.Status, Entity: "sftp_file", EntityID: rec.ID.String(), After: rec}); err != nil {
			return err
		}
		if rec.Status == "failed" {
			fix := "Correct the file (or the master data it refers to) and put it back in the inbound folder; it is picked up on the next poll."
			if rec.MovedTo != "" {
				fix = "The file was moved to " + rec.MovedTo + ". " + fix
			}
			return m.raiseAlert(ctx, tx, "integration.sftp", "error", in.Name+": inbound file "+rec.Name+" failed", map[string]any{
				"instanceId": in.ID, "connector": in.Connector, "file": rec.Name, "movedTo": rec.MovedTo, "reason": rec.Error, "retryable": true,
				"suggestedFix": fix,
			}, "sftp_file", rec.ID.String(), "sftp-file:"+in.ID.String()+":"+rec.Name+":"+rec.SHA256)
		}
		return nil
	})
	if err != nil && rec.Error == "" {
		rec.Status, rec.Error = "failed", err.Error()
	}
	return rec
}

// ingest captures (EPCIS) or parses (EDI) one file.
func (m *Module) ingest(ctx context.Context, in Instance, name string, data []byte) (string, *edi.Document, *uuid.UUID, error) {
	body := bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	head := bytes.TrimSpace(body)
	switch {
	case bytes.HasPrefix(head, []byte("ISA")):
		d, err := edi.ParseX12(string(head))
		if err != nil {
			return "x12", nil, nil, err
		}
		return "x12", &d, nil, nil
	case bytes.HasPrefix(head, []byte("UNA")) || bytes.HasPrefix(head, []byte("UNB")):
		d, err := edi.ParseEDIFACT(string(head))
		if err != nil {
			return "edifact", nil, nil, err
		}
		return "edifact", &d, nil, nil
	}
	ct := ""
	switch strings.ToLower(path.Ext(name)) {
	case ".xml":
		ct = "application/xml"
	case ".json", ".jsonld":
		ct = "application/ld+json"
	}
	format := "epcis"
	if m.Capture == nil {
		return format, nil, nil, errors.New("EPCIS capture is not available on this installation")
	}
	id, err := m.Capture(ctx, ct, body, "sftp:"+in.Name+"/"+name)
	if err != nil {
		return format, nil, nil, err
	}
	return format, nil, &id, nil
}

func (m *Module) raiseAlert(ctx context.Context, tx pgx.Tx, kind, severity, title string, detail map[string]any, entity, entityID, dedupe string) error {
	_, err := tx.Exec(ctx, `insert into alert (id, tenant_id, kind, severity, title, detail, entity, entity_id, dedupe_key)
		values ($1, app_tenant(), $2, $3, $4, $5, $6, $7, $8) on conflict do nothing`, ids.New(), kind, severity, title, detail, entity, entityID, dedupe)
	return err
}

func (m *Module) resolveAlert(ctx context.Context, tenant uuid.UUID, dedupe string) {
	_ = m.DB.Tx(ctx, tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update alert set status='resolved', resolved_at=now() where dedupe_key=$1 and status='open'`, dedupe)
		return err
	})
}

func (m *Module) connectionAlert(ctx context.Context, in Instance, cause error) {
	fix := "Check the SFTP host, port, username, credentials and folders in the connector settings."
	var hk *sftpdrop.HostKeyError
	if errors.As(cause, &hk) {
		fix = "The server presented host key " + hk.Presented + ", which is not the pinned fingerprint. Confirm the key with the partner out of band; " +
			"only if the partner rotated its key, update the host key fingerprint. Never pin a key you have not verified."
	}
	_ = m.DB.Tx(ctx, in.TenantID, func(tx pgx.Tx) error {
		return m.raiseAlert(ctx, tx, "integration.sftp", "error", in.Name+": SFTP connection failed", map[string]any{
			"instanceId": in.ID, "connector": in.Connector, "reason": cause.Error(), "suggestedFix": fix, "retryable": true,
		}, "connector_instance", in.ID.String(), "sftp-conn:"+in.ID.String())
	})
}

// --- HTTP ---

func (m *Module) sftpInstance(r *http.Request) (Instance, error) {
	id, err := ids.Param(r, "id")
	if err != nil {
		return Instance{}, err
	}
	var in Instance
	err = m.Tx(r.Context(), func(tx pgx.Tx) error {
		in, err = db.One[Instance](r.Context(), tx, `select `+instCols+` from connector_instance where id=$1`, id)
		return err
	})
	if err == nil && in.Connector != sftpdrop.ID {
		err = httpx.BadRequest("not an SFTP connector instance")
	}
	return in, err
}

func (m *Module) pollNow(w http.ResponseWriter, r *http.Request) error {
	in, err := m.sftpInstance(r)
	if err != nil {
		return err
	}
	if !in.Enabled {
		return httpx.Conflict("instance is disabled")
	}
	res, err := m.PollSFTP(r.Context(), in)
	if errors.Is(err, errBusy) {
		return httpx.Conflict(err.Error())
	}
	if err != nil {
		return httpx.Unprocessable("SFTP poll failed: "+err.Error(), nil)
	}
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

// sftpTest connects with the stored configuration and lists the configured folders.
func (m *Module) sftpTest(w http.ResponseWriter, r *http.Request) error {
	in, err := m.sftpInstance(r)
	if err != nil {
		return err
	}
	cfg := m.effectiveConfig(r.Context(), in)
	out := map[string]any{"ok": false}
	sess, err := m.dialSFTP(r.Context(), cfg)
	if err != nil {
		out["error"] = err.Error()
		var hk *sftpdrop.HostKeyError
		if errors.As(err, &hk) {
			out["presentedFingerprint"] = hk.Presented
		}
		httpx.JSON(w, http.StatusOK, out)
		return nil
	}
	defer func() { _ = sess.Close() }()
	out["ok"] = true
	folders := map[string]any{}
	for _, k := range []string{"inboundDir", "outboundDir", "archiveDir", "errorDir"} {
		if cfg[k] == "" {
			continue
		}
		if fs, err := sess.List(cfg[k]); err != nil {
			folders[k] = map[string]any{"path": sess.Abs(cfg[k]), "error": err.Error()}
		} else {
			folders[k] = map[string]any{"path": sess.Abs(cfg[k]), "files": len(fs)}
		}
	}
	out["folders"] = folders
	httpx.JSON(w, http.StatusOK, out)
	return nil
}

// hostKey returns a server's host key fingerprint so an administrator can compare it with
// the one the partner communicated. It never pins anything.
func (m *Module) hostKey(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.Port == 0 {
		in.Port = 22
	}
	if strings.TrimSpace(in.Host) == "" || in.Port < 1 || in.Port > 65535 {
		return httpx.BadRequest("host and a valid port are required")
	}
	fp, err := sftpdrop.Probe(r.Context(), in.Host, in.Port)
	if err != nil {
		return httpx.Unprocessable("could not read the host key: "+err.Error(), nil)
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"fingerprint": fp, "host": in.Host, "port": strconv.Itoa(in.Port)})
	return nil
}

func (m *Module) listFiles(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[SFTPFile](r.Context(), tx, `select `+sftpFileCols+` from sftp_file where instance_id=$1 and ($2='' or status=$2)
			order by created_at desc limit 200`, id, r.URL.Query().Get("status"))
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[SFTPFile]{Items: items, Total: len(items)})
		}
		return err
	})
}

// CaptureVia adapts a capture function that also reports per-field validation messages into
// a CaptureFunc whose error names the first problems, so they reach the file log and alert.
func CaptureVia(fn func(ctx context.Context, contentType string, body []byte, source string) (uuid.UUID, []string, error)) CaptureFunc {
	return func(ctx context.Context, ct string, b []byte, src string) (uuid.UUID, error) {
		id, msgs, err := fn(ctx, ct, b, src)
		if err != nil && len(msgs) > 0 {
			if len(msgs) > 5 {
				msgs = append(msgs[:5], "…")
			}
			return id, fmt.Errorf("%w: %s", err, strings.Join(msgs, "; "))
		}
		return id, err
	}
}
