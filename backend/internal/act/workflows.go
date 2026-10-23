package act

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
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

func idsParam(r *http.Request) (uuid.UUID, error) { return ids.Param(r, "id") }

func (m *Module) workflowRoutes(r chi.Router) {
	r.Get("/forms", httpx.Handler(m.listForms))
	r.With(auth.Require(auth.RoleAdmin)).Post("/forms", httpx.Handler(m.createForm))
	// Viewer is strictly read-only: submitting forms and running workflows need a working role.
	writer := auth.Require(auth.RoleAdmin, auth.RoleSerialization, auth.RoleCompliance, auth.RoleOps, auth.RoleIntegration)
	r.With(writer).Post("/forms/{id}/submit", httpx.Handler(m.submitForm))
	r.Get("/forms/{id}/submissions", httpx.Handler(m.listSubmissions))
	r.Get("/workflows", httpx.Handler(m.listWorkflows))
	r.With(auth.Require(auth.RoleAdmin)).Post("/workflows", httpx.Handler(m.createWorkflow))
	r.Get("/processes", httpx.Handler(m.listProcesses))
	r.With(writer).Post("/workflows/{key}/start", httpx.Handler(m.startRun))
	r.With(writer).Post("/processes/{key}/start", httpx.Handler(m.startRun))
	r.Get("/workflow-runs", httpx.Handler(m.listRuns))
	r.Get("/workflow-runs/{id}", httpx.Handler(m.getRun))
	r.With(writer).Post("/workflow-runs/{id}/advance", httpx.Handler(m.advanceRun))
}

// --- forms (JSON Schema) ---

// FormDef is a no-code form.
type FormDef struct {
	ID        uuid.UUID      `json:"id" db:"id"`
	Name      string         `json:"name" db:"name"`
	Schema    map[string]any `json:"schema" db:"schema"`
	CreatedAt time.Time      `json:"createdAt" db:"created_at"`
}

func (m *Module) listForms(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[FormDef](r.Context(), tx, `select id, name, schema, created_at from form_def order by name`)
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[FormDef]{Items: items, Total: len(items)})
		}
		return err
	})
}

var allowedTypes = map[string]bool{"string": true, "number": true, "integer": true, "boolean": true, "array": true, "object": true}

// checkSchema accepts the JSON Schema subset the designer produces: an object with typed properties.
func checkSchema(s map[string]any) error {
	if t, _ := s["type"].(string); t != "object" {
		return httpx.BadRequest(`schema.type must be "object"`)
	}
	props, ok := s["properties"].(map[string]any)
	if !ok || len(props) == 0 {
		return httpx.BadRequest("schema.properties must define at least one field")
	}
	for k, v := range props {
		p, ok := v.(map[string]any)
		if !ok || !allowedTypes[fmt.Sprint(p["type"])] {
			return httpx.BadRequest("field " + k + " needs a type (string, number, integer, boolean, array, object)")
		}
	}
	return nil
}

// ValidateData checks required fields, types, enums and string patterns.
func ValidateData(schema map[string]any, data map[string]any) []string {
	var errs []string
	props, _ := schema["properties"].(map[string]any)
	if req, ok := schema["required"].([]any); ok {
		for _, k := range req {
			if v, present := data[fmt.Sprint(k)]; !present || v == nil || v == "" {
				errs = append(errs, fmt.Sprintf("%v is required", k))
			}
		}
	}
	for k, v := range data {
		p, ok := props[k].(map[string]any)
		if !ok {
			errs = append(errs, k+" is not a field of this form")
			continue
		}
		t := fmt.Sprint(p["type"])
		switch v := v.(type) {
		case string:
			if t != "string" {
				errs = append(errs, k+" must be "+t)
			} else if pat, ok := p["pattern"].(string); ok {
				if re, err := regexp.Compile(pat); err == nil && !re.MatchString(v) {
					errs = append(errs, k+" does not match the required format")
				}
			}
		case float64:
			if t != "number" && t != "integer" {
				errs = append(errs, k+" must be "+t)
			} else if t == "integer" && v != float64(int64(v)) {
				errs = append(errs, k+" must be an integer")
			}
		case bool:
			if t != "boolean" {
				errs = append(errs, k+" must be "+t)
			}
		}
		if enum, ok := p["enum"].([]any); ok {
			found := false
			for _, e := range enum {
				if fmt.Sprint(e) == fmt.Sprint(v) {
					found = true
				}
			}
			if !found {
				errs = append(errs, k+" must be one of the listed values")
			}
		}
	}
	return errs
}

func (m *Module) createForm(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name   string         `json:"name"`
		Schema map[string]any `json:"schema"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.Name == "" {
		return httpx.BadRequest("name is required")
	}
	if err := checkSchema(in.Schema); err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		f, err := db.One[FormDef](r.Context(), tx, `insert into form_def (id, tenant_id, name, schema) values ($1, app_tenant(), $2, $3)
			on conflict (tenant_id, name) do update set schema=excluded.schema returning id, name, schema, created_at`, ids.New(), in.Name, in.Schema)
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "form.save", Entity: "form_def", EntityID: f.ID.String(), After: f}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, f)
		return nil
	})
}

func (m *Module) submitForm(w http.ResponseWriter, r *http.Request) error {
	id, err := idsParam(r)
	if err != nil {
		return err
	}
	var in struct {
		Data map[string]any `json:"data"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		f, err := db.One[FormDef](r.Context(), tx, `select id, name, schema, created_at from form_def where id=$1`, id)
		if err != nil {
			return err
		}
		if errs := ValidateData(f.Schema, in.Data); len(errs) > 0 {
			return httpx.Unprocessable("form data is invalid", errs)
		}
		sid := ids.New()
		if _, err := tx.Exec(r.Context(), `insert into form_submission (id, tenant_id, form_id, data, submitted_by) values ($1, app_tenant(), $2, $3, $4)`,
			sid, id, in.Data, auth.MustFrom(r.Context()).Actor()); err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "form.submit", Entity: "form_def", EntityID: id.String(), After: in.Data}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, map[string]any{"id": sid})
		return nil
	})
}

func (m *Module) listSubmissions(w http.ResponseWriter, r *http.Request) error {
	id, err := idsParam(r)
	if err != nil {
		return err
	}
	type sub struct {
		ID          uuid.UUID      `json:"id" db:"id"`
		Data        map[string]any `json:"data" db:"data"`
		SubmittedBy string         `json:"submittedBy" db:"submitted_by"`
		CreatedAt   time.Time      `json:"createdAt" db:"created_at"`
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[sub](r.Context(), tx, `select id, data, submitted_by, created_at from form_submission where form_id=$1 order by created_at desc limit 500`, id)
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[sub]{Items: items, Total: len(items)})
		}
		return err
	})
}

// --- workflows and the process library ---

// Step is one workflow step.
type Step struct {
	Key               string         `json:"key"`
	Name              string         `json:"name"`
	Role              string         `json:"role,omitempty"`
	RequiresSignature bool           `json:"requiresSignature,omitempty"`
	Form              map[string]any `json:"form,omitempty"` // JSON Schema for the step's data
	Description       string         `json:"description,omitempty"`
}

// Definition is a workflow definition.
type Definition struct {
	Description string `json:"description,omitempty"`
	Steps       []Step `json:"steps"`
}

func schema(required []string, props map[string]string) map[string]any {
	p := map[string]any{}
	for k, t := range props {
		p[k] = map[string]any{"type": t}
	}
	req := make([]any, len(required))
	for i, r := range required {
		req[i] = r
	}
	return map[string]any{"type": "object", "properties": p, "required": req}
}

// Processes is the built-in process library.
var Processes = map[string]struct {
	Name string
	Def  Definition
}{
	"po-to-receipt": {"Purchase order to receipt", Definition{Description: "Raise a PO, confirm it with the supplier, receive and reconcile the shipment against the ASN and TI/TS.", Steps: []Step{
		{Key: "raise_po", Name: "Raise purchase order", Role: auth.RoleOps, Form: schema([]string{"poNumber", "supplier"}, map[string]string{"poNumber": "string", "supplier": "string", "amount": "number"})},
		{Key: "supplier_confirmation", Name: "Supplier confirmation", Role: auth.RoleOps, Form: schema([]string{"confirmed"}, map[string]string{"confirmed": "boolean", "expectedDate": "string"})},
		{Key: "receive", Name: "Receive shipment", Role: auth.RoleOps, Form: schema([]string{"shipmentId"}, map[string]string{"shipmentId": "string"})},
		{Key: "reconcile", Name: "Reconcile TI/TS with ASN", Role: auth.RoleCompliance, Form: schema([]string{"matched"}, map[string]string{"matched": "boolean", "note": "string"})},
	}}},
	"batch-release": {"Batch release", Definition{Description: "Batch record review, QC results and Qualified Person certification before the batch is released to market.", Steps: []Step{
		{Key: "batch_record_review", Name: "Batch record review", Role: auth.RoleCompliance, Form: schema([]string{"batch", "deviations"}, map[string]string{"batch": "string", "deviations": "integer"})},
		{Key: "qc_results", Name: "QC results", Role: auth.RoleCompliance, Form: schema([]string{"assayPct", "micro"}, map[string]string{"assayPct": "number", "micro": "string"})},
		{Key: "qp_certification", Name: "Qualified Person certification", Role: auth.RoleCompliance, RequiresSignature: true, Description: "Electronic signature required (21 CFR Part 11 / EU GMP Annex 16)."},
		{Key: "release", Name: "Release to market", Role: auth.RoleSerialization},
	}}},
	"order-to-cash": {"Order to cash", Definition{Description: "Customer order through shipment with TI/TS, invoice and payment.", Steps: []Step{
		{Key: "order_received", Name: "Order received", Role: auth.RoleOps, Form: schema([]string{"orderNumber"}, map[string]string{"orderNumber": "string"})},
		{Key: "pick_pack_ship", Name: "Pick, pack and ship", Role: auth.RoleOps, Form: schema([]string{"shipmentId"}, map[string]string{"shipmentId": "string"})},
		{Key: "tits_sent", Name: "TI/TS sent", Role: auth.RoleCompliance},
		{Key: "invoice", Name: "Invoice", Role: auth.RoleAdmin, Form: schema([]string{"invoiceNumber", "amount"}, map[string]string{"invoiceNumber": "string", "amount": "number"})},
		{Key: "payment", Name: "Payment received", Role: auth.RoleAdmin},
	}}},
}

type workflow struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	Key        string     `json:"key" db:"key"`
	Name       string     `json:"name" db:"name"`
	Definition Definition `json:"definition" db:"definition"`
	Builtin    bool       `json:"builtin" db:"builtin"`
	CreatedAt  time.Time  `json:"createdAt" db:"created_at"`
	Steps      []Step     `json:"steps" db:"-"`
}

const wfCols = `id, key, name, definition, builtin, created_at`

// ensureBuiltins installs the process library for the tenant on first use.
func ensureBuiltins(r *http.Request, tx pgx.Tx) error {
	for key, p := range Processes {
		if _, err := tx.Exec(r.Context(), `insert into workflow_def (id, tenant_id, key, name, definition, builtin) values ($1, app_tenant(), $2,$3,$4,true)
			on conflict (tenant_id, key) do update set definition=excluded.definition, name=excluded.name where workflow_def.builtin`, ids.New(), key, p.Name, p.Def); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) listWorkflowsWhere(w http.ResponseWriter, r *http.Request, builtinOnly bool) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		if err := ensureBuiltins(r, tx); err != nil {
			return err
		}
		items, err := db.List[workflow](r.Context(), tx, `select `+wfCols+` from workflow_def where (not $1 or builtin) order by builtin desc, name`, builtinOnly)
		for i := range items {
			items[i].Steps = items[i].Definition.Steps
		}
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[workflow]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) listWorkflows(w http.ResponseWriter, r *http.Request) error {
	return m.listWorkflowsWhere(w, r, false)
}

func (m *Module) listProcesses(w http.ResponseWriter, r *http.Request) error {
	return m.listWorkflowsWhere(w, r, true)
}

var keyRe = regexp.MustCompile(`[^a-z0-9]+`)

// createWorkflow accepts {name, description?, steps[]} (the designer posts {name, ...definition}).
func (m *Module) createWorkflow(w http.ResponseWriter, r *http.Request) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	var in struct {
		Name string `json:"name"`
		Key  string `json:"key"`
		Definition
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return httpx.BadRequest("invalid JSON")
	}
	if in.Name == "" || len(in.Steps) == 0 {
		return httpx.BadRequest("name and at least one step are required")
	}
	seen := map[string]bool{}
	for i, s := range in.Steps {
		if s.Key == "" || s.Name == "" || seen[s.Key] {
			return httpx.BadRequest(fmt.Sprintf("step %d needs a unique key and a name", i+1))
		}
		seen[s.Key] = true
		if s.Role != "" && !contains(auth.AllRoles, s.Role) {
			return httpx.BadRequest("unknown role " + s.Role)
		}
		if s.Form != nil {
			if err := checkSchema(s.Form); err != nil {
				return err
			}
		}
	}
	if in.Key == "" {
		in.Key = keyRe.ReplaceAllString(lower(in.Name), "-")
	}
	if _, builtin := Processes[in.Key]; builtin {
		return httpx.Conflict("that key belongs to a built-in process")
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		wf, err := db.One[workflow](r.Context(), tx, `insert into workflow_def (id, tenant_id, key, name, definition) values ($1, app_tenant(), $2,$3,$4)
			on conflict (tenant_id, key) do update set name=excluded.name, definition=excluded.definition where not workflow_def.builtin returning `+wfCols,
			ids.New(), in.Key, in.Name, in.Definition)
		if err != nil {
			return err
		}
		wf.Steps = wf.Definition.Steps
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "workflow.save", Entity: "workflow_def", EntityID: wf.ID.String(), After: wf}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, wf)
		return nil
	})
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// Run is a workflow instance.
type Run struct {
	ID         uuid.UUID      `json:"id" db:"id"`
	WorkflowID uuid.UUID      `json:"workflowId" db:"workflow_id"`
	Key        string         `json:"key" db:"key"`
	Name       string         `json:"name" db:"name"`
	Status     string         `json:"status" db:"status"`
	Step       int            `json:"step" db:"step"`
	Context    map[string]any `json:"context" db:"context"`
	History    []any          `json:"history" db:"history"`
	StartedBy  string         `json:"startedBy" db:"started_by"`
	CreatedAt  time.Time      `json:"createdAt" db:"created_at"`
	UpdatedAt  time.Time      `json:"updatedAt" db:"updated_at"`
	Definition Definition     `json:"definition" db:"definition"`
	Current    *Step          `json:"currentStep,omitempty" db:"-"`
}

const runSelect = `select r.id, r.workflow_id, d.key, d.name, r.status, r.step, r.context, r.history, r.started_by, r.created_at, r.updated_at, d.definition
	from workflow_run r join workflow_def d on d.id = r.workflow_id`

func (rn *Run) fill() {
	if rn.Status == "running" && rn.Step < len(rn.Definition.Steps) {
		s := rn.Definition.Steps[rn.Step]
		rn.Current = &s
	}
}

func (m *Module) startRun(w http.ResponseWriter, r *http.Request) error {
	key := chi.URLParam(r, "key")
	var in struct {
		Context map[string]any `json:"context"`
	}
	if r.ContentLength > 0 {
		if err := httpx.Decode(r, &in); err != nil {
			return err
		}
	}
	if in.Context == nil {
		in.Context = map[string]any{}
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		if err := ensureBuiltins(r, tx); err != nil {
			return err
		}
		var wfID uuid.UUID
		if err := tx.QueryRow(r.Context(), `select id from workflow_def where key=$1 or id::text=$1`, key).Scan(&wfID); err != nil {
			return httpx.NotFound("unknown workflow")
		}
		id := ids.New()
		if _, err := tx.Exec(r.Context(), `insert into workflow_run (id, tenant_id, workflow_id, context, started_by) values ($1, app_tenant(), $2, $3, $4)`,
			id, wfID, in.Context, auth.MustFrom(r.Context()).Actor()); err != nil {
			return err
		}
		rn, err := db.One[Run](r.Context(), tx, runSelect+` where r.id=$1`, id)
		if err != nil {
			return err
		}
		rn.fill()
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "workflow.start", Entity: "workflow_run", EntityID: id.String(), After: map[string]any{"workflow": rn.Key}}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, rn)
		return nil
	})
}

func (m *Module) listRuns(w http.ResponseWriter, r *http.Request) error {
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		items, err := db.List[Run](r.Context(), tx, runSelect+` where ($1='' or r.status=$1) order by r.updated_at desc limit 500`, r.URL.Query().Get("status"))
		for i := range items {
			items[i].fill()
		}
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[Run]{Items: items, Total: len(items)})
		}
		return err
	})
}

func (m *Module) getRun(w http.ResponseWriter, r *http.Request) error {
	id, err := idsParam(r)
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		rn, err := db.One[Run](r.Context(), tx, runSelect+` where r.id=$1`, id)
		rn.fill()
		if err == nil {
			httpx.JSON(w, http.StatusOK, rn)
		}
		return err
	})
}

// advanceRun completes the current step: role check, form validation and, where required,
// an e-signature recorded by the same user against this run.
func (m *Module) advanceRun(w http.ResponseWriter, r *http.Request) error {
	id, err := idsParam(r)
	if err != nil {
		return err
	}
	var in struct {
		Data map[string]any `json:"data"`
	}
	if r.ContentLength > 0 {
		if err := httpx.Decode(r, &in); err != nil {
			return err
		}
	}
	if in.Data == nil {
		in.Data = map[string]any{}
	}
	p := auth.MustFrom(r.Context())
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		rn, err := db.One[Run](r.Context(), tx, runSelect+` where r.id=$1 for update of r`, id)
		if err != nil {
			return err
		}
		if rn.Status != "running" {
			return httpx.Conflict("workflow run is " + rn.Status)
		}
		step := rn.Definition.Steps[rn.Step]
		if step.Role != "" && !p.Has(step.Role) {
			return httpx.Forbidden("step " + step.Name + " needs role " + step.Role)
		}
		if step.Form != nil {
			if errs := ValidateData(step.Form, in.Data); len(errs) > 0 {
				return httpx.Unprocessable("step data is invalid", errs)
			}
		}
		entry := map[string]any{"step": step.Key, "by": p.Actor(), "at": time.Now().UTC(), "data": in.Data}
		if step.RequiresSignature {
			var sig struct {
				ID      uuid.UUID
				Meaning string
			}
			if err := tx.QueryRow(r.Context(), `select id, meaning from esignature where entity='workflow_run' and entity_id=$1 and user_id=$2 and signed_at > $3 order by signed_at desc limit 1`,
				id.String(), p.UserID, rn.UpdatedAt).Scan(&sig.ID, &sig.Meaning); err != nil {
				return &httpx.Error{Status: http.StatusPreconditionRequired, Msg: "this step requires your electronic signature (POST /esignatures with entity workflow_run)"}
			}
			entry["signatureId"], entry["signatureMeaning"] = sig.ID, sig.Meaning
		}
		next, status := rn.Step+1, "running"
		if next >= len(rn.Definition.Steps) {
			status = "completed"
		}
		hist, _ := json.Marshal([]any{entry})
		ctxPatch, _ := json.Marshal(in.Data)
		if _, err := tx.Exec(r.Context(), `update workflow_run set step=$2, status=$3, history = history || $4::jsonb, context = context || $5::jsonb, updated_at=now() where id=$1`,
			id, next, status, hist, ctxPatch); err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "workflow.step." + step.Key, Entity: "workflow_run", EntityID: id.String(), After: entry}); err != nil {
			return err
		}
		rn, err = db.One[Run](r.Context(), tx, runSelect+` where r.id=$1`, id)
		rn.fill()
		if err == nil {
			httpx.JSON(w, http.StatusOK, rn)
		}
		return err
	})
}
