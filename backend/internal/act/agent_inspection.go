package act

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
)

// inspection-agent assembles an inspection package on request: the FSMA 204 sortable
// spreadsheet (food module) or a DSCSA tracing answer (dscsa module), stored as a draft
// `inspection_package` with a checklist. A draft is not a record of anything: it is released only
// by a person's electronic signature after every checklist item is satisfied. Nothing is sent to
// any authority or partner from here; the released file is downloaded and sent by a person.
//
// TODO(spec): the checklist items are working steps, not a statement of what an authority
// requires; confirm the required content, format and lead time of an FSMA 204 records request and
// of a DSCSA tracing response per requester before presenting the checklist as a compliance one.

const maxPackageBytes = 8 << 20

type inspectionAgent struct{}

func (inspectionAgent) polished()    {}
func (inspectionAgent) Name() string { return "inspection-agent" }
func (inspectionAgent) Description() string {
	return "On request, assembles the FSMA 204 spreadsheet or a DSCSA tracing answer as a draft package with a checklist. A person signs to release it; nothing is sent to an authority automatically."
}

// Observe: the agent works on request (POST /agents/inspection/assemble), not on a schedule.
func (inspectionAgent) Observe(context.Context, pgx.Tx) ([]Proposal, error) { return nil, nil }

// ChecklistItem is one step before a package may be released.
type ChecklistItem struct {
	Key         string     `json:"key"`
	Title       string     `json:"title"`
	Kind        string     `json:"kind"`   // auto (checked by the agent) | manual (a person confirms)
	Status      string     `json:"status"` // ok | warn | todo
	Detail      string     `json:"detail,omitempty"`
	ConfirmedBy string     `json:"confirmedBy,omitempty"`
	ConfirmedAt *time.Time `json:"confirmedAt,omitempty"`
}

func (c ChecklistItem) satisfied() bool { return c.Status == "ok" || c.ConfirmedBy != "" }

// Package is an inspection package (content is served by the download endpoint).
type Package struct {
	ID          uuid.UUID       `json:"id"`
	Kind        string          `json:"kind"`
	Title       string          `json:"title"`
	Scope       json.RawMessage `json:"scope"`
	Status      string          `json:"status"`
	ContentType string          `json:"contentType"`
	Size        int             `json:"size"`
	SHA256      string          `json:"sha256"`
	Summary     json.RawMessage `json:"summary"`
	Checklist   []ChecklistItem `json:"checklist"`
	CreatedBy   string          `json:"createdBy"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
	SignatureID *uuid.UUID      `json:"signatureId,omitempty"`
	ReleasedBy  string          `json:"releasedBy,omitempty"`
	ReleasedAt  *time.Time      `json:"releasedAt,omitempty"`
	ActionID    *uuid.UUID      `json:"actionId,omitempty"`
}

const pkgCols = `id, kind, title, scope, status, content_type, length(content), sha256, summary, checklist, created_by, created_at, updated_at, signature_id, released_by, released_at`

func scanPackage(row pgx.Row) (Package, error) {
	var p Package
	var cl []byte
	err := row.Scan(&p.ID, &p.Kind, &p.Title, &p.Scope, &p.Status, &p.ContentType, &p.Size, &p.SHA256, &p.Summary, &cl, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt, &p.SignatureID, &p.ReleasedBy, &p.ReleasedAt)
	if err == nil {
		err = json.Unmarshal(cl, &p.Checklist)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		err = db.ErrNotFound
	}
	return p, err
}

type inspectionInput struct {
	PackageID uuid.UUID `json:"packageId"`
	Kind      string    `json:"kind"`
	Scope     struct {
		From            string `json:"from,omitempty"`            // fsma204: YYYY-MM-DD
		To              string `json:"to,omitempty"`              // fsma204: YYYY-MM-DD
		TLC             string `json:"tlc,omitempty"`             // fsma204: one lot and its lineage
		InvestigationID string `json:"investigationId,omitempty"` // tracing
	} `json:"scope"`
}

// validate rejects a request before anything is recorded.
func (in inspectionInput) validate(ctx context.Context, tx pgx.Tx) error {
	switch in.Kind {
	case "fsma204":
		for _, d := range []string{in.Scope.From, in.Scope.To} {
			if _, err := time.Parse(time.DateOnly, d); d != "" && err != nil {
				return httpx.BadRequest("scope.from and scope.to must be YYYY-MM-DD")
			}
		}
	case "tracing":
		id, err := uuid.Parse(in.Scope.InvestigationID)
		if err != nil {
			return httpx.BadRequest("scope.investigationId is required for a tracing package")
		}
		var n int
		if err := tx.QueryRow(ctx, `select count(*) from investigation where id=$1`, id).Scan(&n); err != nil || n == 0 {
			return httpx.BadRequest("unknown investigation")
		}
	default:
		return httpx.BadRequest("kind must be fsma204 or tracing")
	}
	return nil
}

func manualItems(extra ...ChecklistItem) []ChecklistItem {
	items := []ChecklistItem{{Key: "scope", Title: "The scope matches what was requested", Kind: "manual", Status: "todo"}}
	items = append(items, extra...)
	return append(items,
		ChecklistItem{Key: "reviewed", Title: "The content was reviewed against the source records", Kind: "manual", Status: "todo"},
		ChecklistItem{Key: "sender", Title: "Who sends the file, to whom and how is decided and done by a person outside this system", Kind: "manual", Status: "todo"})
}

// build assembles the content, summary and checklist through the other modules' entry points.
func (m *Module) buildPackage(ctx context.Context, tx pgx.Tx, in inspectionInput) (title, contentType, content string, summary map[string]any, list []ChecklistItem, err error) {
	switch in.Kind {
	case "fsma204":
		if m.FSMA == nil {
			return "", "", "", nil, nil, errors.New("the food module is not available")
		}
		from, to := time.Unix(0, 0), time.Now().Add(24*time.Hour)
		if in.Scope.From != "" {
			if from, err = time.Parse(time.DateOnly, in.Scope.From); err != nil {
				return "", "", "", nil, nil, httpx.BadRequest("scope.from must be YYYY-MM-DD")
			}
		}
		if in.Scope.To != "" {
			t, perr := time.Parse(time.DateOnly, in.Scope.To)
			if perr != nil {
				return "", "", "", nil, nil, httpx.BadRequest("scope.to must be YYYY-MM-DD")
			}
			to = t.Add(24 * time.Hour)
		}
		csv, rows, cerr := m.FSMA.CSV(ctx, tx, from, to, in.Scope.TLC)
		if cerr != nil {
			return "", "", "", nil, nil, cerr
		}
		title = "FSMA 204 sortable spreadsheet"
		if in.Scope.TLC != "" {
			title += " - lot " + in.Scope.TLC
		}
		summary = map[string]any{"rows": rows, "from": in.Scope.From, "to": in.Scope.To, "tlc": in.Scope.TLC}
		rowItem := ChecklistItem{Key: "rows", Title: "The spreadsheet has rows", Kind: "auto", Status: "ok", Detail: fmt.Sprintf("%d critical tracking event rows.", rows)}
		if rows == 0 {
			rowItem.Status, rowItem.Detail = "warn", "No critical tracking events match the scope; check the dates and lot code."
		}
		list = manualItems(rowItem, ChecklistItem{Key: "layout", Title: "The column layout was checked against the template the authority asked for", Kind: "manual", Status: "todo",
			Detail: "TODO(spec): align the column names and order with the authority's published template."})
		// rows is an auto item and belongs after the scope item
		list[0], list[1] = list[1], list[0]
		return title, "text/csv; charset=utf-8", csv, summary, list, nil
	case "tracing":
		if m.Tracing == nil {
			return "", "", "", nil, nil, errors.New("the tracing module is not available")
		}
		id, perr := uuid.Parse(in.Scope.InvestigationID)
		if perr != nil {
			return "", "", "", nil, nil, httpx.BadRequest("scope.investigationId is required for a tracing package")
		}
		var invTitle, invKind string
		if qerr := tx.QueryRow(ctx, `select title, kind from investigation where id=$1`, id).Scan(&invTitle, &invKind); qerr != nil {
			return "", "", "", nil, nil, httpx.BadRequest("unknown investigation")
		}
		ans, sum, perr := m.Tracing.Preview(ctx, tx, id)
		if perr != nil {
			return "", "", "", nil, nil, perr
		}
		title = "Tracing answer: " + nonEmpty(invTitle, invKind)
		summary = sum
		summary["investigationId"] = id
		n, _ := sum["items"].(int)
		itemsItem := ChecklistItem{Key: "items", Title: "The answer covers identifiers", Kind: "auto", Status: "ok", Detail: fmt.Sprintf("%d identifiers, %v transactions.", n, sum["transactions"])}
		if n == 0 {
			itemsItem.Status, itemsItem.Detail = "warn", "The answer has no identifiers; check the investigation's scope."
		}
		list = manualItems(itemsItem)
		list[0], list[1] = list[1], list[0]
		if nf, _ := sum["notFound"].(int); nf > 0 {
			list = append(list, ChecklistItem{Key: "not_found", Title: "Identifiers not found in the records were explained", Kind: "auto", Status: "warn", Detail: fmt.Sprintf("%d identifiers in scope were not found.", nf)})
		}
		if tr, _ := sum["truncated"].(bool); tr {
			list = append(list, ChecklistItem{Key: "truncated", Title: "The truncated answer was completed or the omission explained", Kind: "auto", Status: "warn", Detail: "The answer lists only the first identifiers; the rest are omitted."})
		}
		list = append(list, ChecklistItem{Key: "format", Title: "The content and format suit the requester", Kind: "manual", Status: "todo",
			Detail: "TODO(spec): confirm the content and format a tracing response must take for each requester."})
		return title, "application/json", string(ans), summary, list, nil
	}
	return "", "", "", nil, nil, httpx.BadRequest("kind must be fsma204 or tracing")
}

// --- tool ---

type inspectionAssembleTool struct{ m *Module }

func (inspectionAssembleTool) Name() string { return "inspection.assemble" }
func (inspectionAssembleTool) Scope() []string {
	return []string{auth.RoleCompliance, auth.RoleOps}
}
func (inspectionAssembleTool) Risk() string     { return "low" }
func (inspectionAssembleTool) Reversible() bool { return true } // a draft can be deleted until it is released

func (t inspectionAssembleTool) Execute(ctx context.Context, tx pgx.Tx, input json.RawMessage) (any, any, error) {
	var in inspectionInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, nil, err
	}
	title, ctype, content, summary, list, err := t.m.buildPackage(ctx, tx, in)
	if err != nil {
		return nil, nil, err
	}
	if len(content) > maxPackageBytes {
		return nil, nil, httpx.BadRequest("the package is larger than 8 MiB; narrow the scope")
	}
	sum := sha256.Sum256([]byte(content))
	if _, err := tx.Exec(ctx, `insert into inspection_package (id, tenant_id, kind, title, scope, content_type, content, sha256, summary, checklist, created_by)
		values ($1, app_tenant(), $2,$3,$4,$5,$6,$7,$8,$9,$10)`, in.PackageID, in.Kind, title, in.Scope, ctype, content, hex.EncodeToString(sum[:]), summary, list, auth.MustFrom(ctx).Actor()); err != nil {
		return nil, nil, err
	}
	return nil, map[string]any{"packageId": in.PackageID, "kind": in.Kind, "sha256": hex.EncodeToString(sum[:]), "summary": summary}, nil
}

func (inspectionAssembleTool) Revert(ctx context.Context, tx pgx.Tx, input, _ json.RawMessage) error {
	var in inspectionInput
	if err := json.Unmarshal(input, &in); err != nil {
		return err
	}
	ct, err := tx.Exec(ctx, `delete from inspection_package where id=$1 and status='draft'`, in.PackageID)
	if err == nil && ct.RowsAffected() == 0 {
		return httpx.Conflict("only a draft package can be reverted; void a released one")
	}
	return err
}

// --- endpoints ---

func (m *Module) inspectionRoutes(r chi.Router) {
	w := auth.Require(writerRoles...)
	signer := auth.Require(auth.RoleAdmin, auth.RoleCompliance)
	r.With(w).Post("/agents/inspection/assemble", httpx.Handler(m.assemblePackage))
	r.Get("/agents/inspection/packages", httpx.Handler(m.listPackages))
	r.Get("/agents/inspection/packages/{id}", httpx.Handler(m.getPackage))
	r.Get("/agents/inspection/packages/{id}/content", httpx.Handler(m.packageContent))
	r.With(w).Post("/agents/inspection/packages/{id}/checklist", httpx.Handler(m.confirmChecklist))
	r.With(signer).Post("/agents/inspection/packages/{id}/release", httpx.Handler(m.releasePackage))
	r.With(signer).Post("/agents/inspection/packages/{id}/void", httpx.Handler(m.voidPackage))
}

// assemblePackage records the request as an agent action decided by the requester (the request is
// the human's approval), runs the typed tool, and returns the draft. Auditing is the framework's.
func (m *Module) assemblePackage(w http.ResponseWriter, r *http.Request) error {
	var in inspectionInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	if err := m.Tx(ctx, func(tx pgx.Tx) error { return in.validate(ctx, tx) }); err != nil {
		return err
	}
	p := auth.MustFrom(ctx)
	tool := m.tools["inspection.assemble"]
	var mode string
	if err := m.Tx(ctx, func(tx pgx.Tx) error {
		modes, err := m.toolModes(ctx, tx)
		mode = modes[tool.Name()]
		return err
	}); err != nil {
		return err
	}
	if mode == ModeSuggest {
		return httpx.Conflict("inspection.assemble is set to suggest only")
	}
	in.PackageID = ids.New()
	actionID := ids.New()
	title := "Assemble inspection package (" + in.Kind + ")"
	if err := m.Tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into agent_action (id, tenant_id, agent, tool, title, summary, reasoning, input, after, risk, confidence, entity, entity_id)
			values ($1, app_tenant(), 'inspection-agent', $2,$3,$4,$5,$6,$7,$8,1,'inspection_package',$9)`,
			actionID, tool.Name(), title, "Requested by "+p.Actor()+".", []string{"Requested by a person; the package is a draft until a person signs it.", "Nothing is sent to any authority or partner."},
			in, map[string]any{"packageId": in.PackageID}, tool.Risk(), in.PackageID.String()); err != nil {
			return err
		}
		_, err := audit.Write(ctx, tx, audit.Record{Action: "agent.propose", Entity: "agent_action", EntityID: actionID.String(), After: map[string]any{"tool": tool.Name(), "input": in}})
		return err
	}); err != nil {
		return err
	}
	a, err := m.execute(ctx, p.TenantID, actionID, p.Actor())
	if err != nil {
		return err
	}
	if a.Status != "executed" {
		return httpx.Unprocessable("the package could not be assembled", a.Result)
	}
	return m.Tx(ctx, func(tx pgx.Tx) error {
		pkg, err := scanPackage(tx.QueryRow(ctx, `select `+pkgCols+` from inspection_package where id=$1`, in.PackageID))
		if err != nil {
			return err
		}
		pkg.ActionID = &actionID
		httpx.JSON(w, http.StatusCreated, pkg)
		return nil
	})
}

func (m *Module) listPackages(w http.ResponseWriter, r *http.Request) error {
	status := r.URL.Query().Get("status")
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `select `+pkgCols+` from inspection_package where ($1='' or status=$1) order by created_at desc limit 200`, status)
		if err != nil {
			return err
		}
		defer rows.Close()
		items := []Package{}
		for rows.Next() {
			p, err := scanPackage(rows)
			if err != nil {
				return err
			}
			items = append(items, p)
		}
		httpx.JSON(w, http.StatusOK, httpx.Page[Package]{Items: items, Total: len(items)})
		return rows.Err()
	})
}

func (m *Module) getPackage(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		p, err := scanPackage(tx.QueryRow(r.Context(), `select `+pkgCols+` from inspection_package where id=$1`, id))
		if err == nil {
			httpx.JSON(w, http.StatusOK, p)
		}
		return err
	})
}

func (m *Module) packageContent(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		var ctype, content, status, kind string
		if err := tx.QueryRow(r.Context(), `select content_type, content, status, kind from inspection_package where id=$1`, id).Scan(&ctype, &content, &status, &kind); err != nil {
			return db.ErrNotFound
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "inspection.package.download", Entity: "inspection_package", EntityID: id.String(), After: map[string]any{"status": status}}); err != nil {
			return err
		}
		ext := map[string]string{"fsma204": "csv", "tracing": "json"}[kind]
		name := fmt.Sprintf("%s-%s-%s.%s", kind, status, id.String()[:8], ext)
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		_, _ = w.Write([]byte(content)) //nolint:gosec // G705: server-built CSV/JSON served as a download with nosniff
		return nil
	})
}

func (m *Module) confirmChecklist(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Key       string `json:"key"`
		Confirmed bool   `json:"confirmed"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := auth.MustFrom(r.Context())
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		pkg, err := scanPackage(tx.QueryRow(r.Context(), `select `+pkgCols+` from inspection_package where id=$1 for update`, id))
		if err != nil {
			return err
		}
		if pkg.Status != "draft" {
			return httpx.Conflict("only a draft package can be changed")
		}
		found := false
		for i := range pkg.Checklist {
			it := &pkg.Checklist[i]
			if it.Key != in.Key {
				continue
			}
			found = true
			if it.Kind == "auto" && it.Status == "ok" {
				return httpx.Conflict("this item was checked by the agent and needs no confirmation")
			}
			if in.Confirmed {
				now := time.Now().UTC()
				it.ConfirmedBy, it.ConfirmedAt = p.Actor(), &now
			} else {
				it.ConfirmedBy, it.ConfirmedAt = "", nil
			}
		}
		if !found {
			return httpx.BadRequest("unknown checklist item")
		}
		pkg, err = scanPackage(tx.QueryRow(r.Context(), `update inspection_package set checklist=$2, updated_at=now() where id=$1 returning `+pkgCols, id, pkg.Checklist))
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "inspection.package.checklist", Entity: "inspection_package", EntityID: id.String(),
			After: map[string]any{"key": in.Key, "confirmed": in.Confirmed}}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, pkg)
		return nil
	})
}

// releasePackage marks a draft released. It needs every checklist item satisfied and the caller's
// electronic signature on this package, given after its last change. It sends nothing anywhere.
func (m *Module) releasePackage(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		SignatureID *uuid.UUID `json:"signatureId"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := auth.MustFrom(r.Context())
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		pkg, err := scanPackage(tx.QueryRow(r.Context(), `select `+pkgCols+` from inspection_package where id=$1 for update`, id))
		if err != nil {
			return err
		}
		if pkg.Status != "draft" {
			return httpx.Conflict("only a draft package can be released")
		}
		var open []string
		for _, it := range pkg.Checklist {
			if !it.satisfied() {
				open = append(open, it.Key)
			}
		}
		if len(open) > 0 {
			return httpx.Conflict("checklist items are not satisfied: " + strings.Join(open, ", "))
		}
		if in.SignatureID == nil {
			return &httpx.Error{Status: http.StatusPreconditionRequired, Msg: "releasing a package requires your electronic signature (POST /esignatures with entity inspection_package) and its id as signatureId"}
		}
		var meaning string
		if err := tx.QueryRow(r.Context(), `select meaning from esignature where id=$1 and entity='inspection_package' and entity_id=$2 and user_id=$3 and signed_at >= $4`,
			*in.SignatureID, id.String(), p.UserID, pkg.UpdatedAt).Scan(&meaning); err != nil {
			return &httpx.Error{Status: http.StatusPreconditionRequired, Msg: "signature not found: sign this package after its last change (POST /esignatures with entity inspection_package) and pass its id"}
		}
		pkg, err = scanPackage(tx.QueryRow(r.Context(), `update inspection_package set status='released', signature_id=$2, released_by=$3, released_at=now(), updated_at=now()
			where id=$1 returning `+pkgCols, id, *in.SignatureID, p.Actor()))
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "inspection.package.release", Entity: "inspection_package", EntityID: id.String(),
			After: map[string]any{"signatureId": in.SignatureID, "meaning": meaning, "sha256": pkg.SHA256}}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, pkg)
		return nil
	})
}

func (m *Module) voidPackage(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if strings.TrimSpace(in.Reason) == "" {
		return httpx.BadRequest("reason is required")
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		pkg, err := scanPackage(tx.QueryRow(r.Context(), `update inspection_package set status='voided', updated_at=now() where id=$1 and status in ('draft','released') returning `+pkgCols, id))
		if err != nil {
			return httpx.Conflict("this package cannot be voided")
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "inspection.package.void", Entity: "inspection_package", EntityID: id.String(), Reason: in.Reason}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, pkg)
		return nil
	})
}
