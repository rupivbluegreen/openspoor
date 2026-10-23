package act

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/audit"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/core/db"
	"github.com/openspoor/openspoor/internal/core/httpx"
	"github.com/openspoor/openspoor/internal/core/ids"
)

// mapping-agent reads sample partner files (uploaded, or kept from a failed import), proposes a
// saved mapping to the canonical document or EPCIS with a confidence and reasoning per field,
// and generates regression cases (sample row to expected canonical value) that are stored with the
// mapping. The proposal is the mapping studio's own header-based suggestion; nothing is saved
// until a person approves, and approval re-runs every case against the mapping first.
//
// TODO(spec): the mapping studio reads CSV, JSON and fixed-width files. EDI interchanges and XML
// are not converted yet, so samples in those formats become a case for a person instead of a mapping.

const (
	maxSampleBytes   = 1 << 20
	maxSampleCases   = 5
	maxSamplesPerRun = 20
	maxEvalRows      = 20
)

// MappingCase is one generated regression case.
type MappingCase struct {
	Row      map[string]string `json:"row"`
	Expected map[string]string `json:"expected"`
}

type mappingSaveInput struct {
	SampleID uuid.UUID     `json:"sampleId"`
	Mapping  MappingDef    `json:"mapping"`
	Cases    []MappingCase `json:"cases"`
}

type mappingAgent struct{ m *Module }

func (mappingAgent) polished()    {}
func (mappingAgent) Name() string { return "mapping-agent" }
func (mappingAgent) Description() string {
	return "Reads sample partner files (CSV, JSON) that were uploaded or failed to import, proposes the mapping to the canonical document or EPCIS with a confidence and reasoning per field, and generates regression cases. A person approves."
}

type sampleRow struct {
	id        uuid.UUID
	name      string
	partnerID *uuid.UUID
	docType   string
	format    string
	options   json.RawMessage
	content   string
	source    string
	errText   string
}

// detectFormat classifies a sample file by its content and, for delimited files, finds the delimiter.
func detectFormat(content string) (string, json.RawMessage) {
	t := strings.TrimSpace(strings.TrimPrefix(content, "\xef\xbb\xbf"))
	switch {
	case strings.HasPrefix(t, "<"):
		return "xml", nil
	case strings.HasPrefix(t, "ISA") || strings.HasPrefix(t, "UNA") || strings.HasPrefix(t, "UNB"):
		return "edi", nil
	case (strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")) && json.Valid([]byte(t)):
		return "json", nil
	}
	first, _, _ := strings.Cut(t, "\n")
	best, bestN := "", 0
	for _, d := range []string{",", ";", "\t", "|"} {
		if n := strings.Count(first, d); n > bestN {
			best, bestN = d, n
		}
	}
	if best == "" {
		return "unknown", nil
	}
	if best == "\t" {
		best = "tab"
	}
	b, _ := json.Marshal(map[string]string{"delimiter": best})
	return "csv", b
}

func (a mappingAgent) Observe(ctx context.Context, tx pgx.Tx) ([]Proposal, error) {
	if a.m.Mapping == nil {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `select id, name, partner_id, doc_type, format, options, content, source, error from mapping_sample
		where status='new' order by created_at limit $1`, maxSamplesPerRun)
	if err != nil {
		return nil, err
	}
	var samples []sampleRow
	for rows.Next() {
		var s sampleRow
		if err := rows.Scan(&s.id, &s.name, &s.partnerID, &s.docType, &s.format, &s.options, &s.content, &s.source, &s.errText); err != nil {
			rows.Close()
			return nil, err
		}
		samples = append(samples, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []Proposal
	for _, s := range samples {
		p, err := a.propose(ctx, tx, s)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// manualCase is the fallback: a case for a person when no mapping can be proposed.
func manualCase(s sampleRow, why string, reasoning []string) Proposal {
	title := "Mapping sample needs a person: " + s.name
	reasoning = append([]string{why}, reasoning...)
	return Proposal{Tool: "case.open", Title: title, Summary: "The mapping agent could not build a saveable mapping from this sample.", Reasoning: reasoning,
		Input:   caseInput{Kind: "mapping.manual", Title: title, Priority: 3, Entity: "mapping_sample", EntityID: s.id.String(), Source: "mapping-agent", SourceID: s.id.String(), Notes: reasoning},
		Preview: map[string]any{"sample": s.name, "docType": s.docType}, Confidence: 0.3, Entity: "mapping_sample", EntityID: s.id.String(), DedupeKey: "mapping-manual:" + s.id.String()}
}

func (a mappingAgent) propose(ctx context.Context, tx pgx.Tx, s sampleRow) (Proposal, error) {
	eng := a.m.Mapping
	format, opts := s.format, s.options
	if format == "unknown" || format == "" {
		format, opts = detectFormat(s.content)
	}
	var tail []string
	if s.errText != "" {
		tail = append(tail, "The failed import reported: "+s.errText)
	}
	if format != "csv" && format != "json" && format != "flat" {
		return manualCase(s, fmt.Sprintf("The sample looks like %s, which the mapping studio does not read (csv, json and fixed-width only).", format), tail), nil
	}
	def := MappingDef{Name: s.name, PartnerID: s.partnerID, DocType: s.docType, Format: format, Options: opts}
	prof, err := eng.Profile(def, []byte(s.content))
	if err != nil {
		return manualCase(s, "The sample file could not be read: "+err.Error(), tail), nil
	}
	if len(prof.Columns) < 2 || len(prof.Rows) == 0 {
		return manualCase(s, "The sample has fewer than two columns or no data rows; check the delimiter and header.", tail), nil
	}
	def.Fields = eng.Suggest(s.docType, prof.Columns)
	if err := eng.Check(def); err != nil {
		rs := []string{}
		for _, f := range def.Fields {
			rs = append(rs, fmt.Sprintf("Header match: column %q looks like %s.", f.Source, f.Target))
		}
		return manualCase(s, "The header-based suggestion cannot be saved: "+err.Error(), append(rs, tail...)), nil
	}
	name, err := freeMappingName(ctx, tx, s.name)
	if err != nil {
		return Proposal{}, err
	}
	def.Name = name

	okRows := 0
	var cases []MappingCase
	for i, row := range prof.Rows {
		if i >= maxEvalRows {
			break
		}
		vals, bad := eng.EvalRow(def, row)
		if len(bad) > 0 {
			continue
		}
		okRows++
		if len(cases) < maxSampleCases {
			cases = append(cases, MappingCase{Row: row, Expected: vals})
		}
	}
	evaluated := min(len(prof.Rows), maxEvalRows)
	mapped := map[string]bool{}
	reasoning := []string{fmt.Sprintf("Read %d sample rows with columns: %s.", prof.Total, strings.Join(prof.Columns, ", "))}
	for _, f := range def.Fields {
		mapped[f.Source] = true
		reasoning = append(reasoning, fmt.Sprintf("%s <- column %q: the column name matches a known name for this target (header-based suggestion). Sample values: %s.",
			f.Target, f.Source, sampleValues(prof.Rows, f.Source)))
	}
	var unmapped []string
	for _, c := range prof.Columns {
		if !mapped[c] {
			unmapped = append(unmapped, c)
		}
	}
	if len(unmapped) > 0 {
		reasoning = append(reasoning, "Not mapped (a person decides): "+strings.Join(unmapped, ", ")+".")
	}
	reasoning = append(reasoning, fmt.Sprintf("%d of %d sample rows map without problems; %d regression cases generated.", okRows, evaluated, len(cases)))
	reasoning = append(reasoning, tail...)
	// Confidence: half the share of columns mapped, half the share of sample rows that map cleanly (at most 0.95).
	conf := 0.5*float64(len(def.Fields))/float64(len(prof.Columns)) + 0.5*float64(okRows)/float64(max(evaluated, 1))
	conf = min(0.95, float64(int(conf*100))/100)

	fieldsPreview := make([]map[string]string, 0, len(def.Fields))
	for _, f := range def.Fields {
		fieldsPreview = append(fieldsPreview, map[string]string{"target": f.Target, "source": f.Source})
	}
	return Proposal{Tool: "mapping.save", Title: fmt.Sprintf("Save mapping %q (%s, %d of %d columns)", name, s.docType, len(def.Fields), len(prof.Columns)),
		Summary:    "Save this mapping for the partner's " + s.docType + " files and keep the generated regression cases with it.",
		Reasoning:  reasoning,
		Input:      mappingSaveInput{SampleID: s.id, Mapping: def, Cases: cases},
		Preview:    map[string]any{"name": name, "docType": s.docType, "format": format, "fields": fieldsPreview, "regressionCases": len(cases)},
		Confidence: conf, Entity: "mapping_sample", EntityID: s.id.String(), DedupeKey: "mapping:" + s.id.String()}, nil
}

func sampleValues(rows []map[string]string, col string) string {
	seen := map[string]bool{}
	var vals []string
	for _, r := range rows {
		if v := strings.TrimSpace(r[col]); v != "" && !seen[v] {
			seen[v] = true
			vals = append(vals, fmt.Sprintf("%q", v))
			if len(vals) == 3 {
				break
			}
		}
	}
	if len(vals) == 0 {
		return "(empty)"
	}
	return strings.Join(vals, ", ")
}

// freeMappingName avoids the studio's unique-name rule.
func freeMappingName(ctx context.Context, tx pgx.Tx, base string) (string, error) {
	for i := 1; i < 50; i++ {
		name := base
		if i > 1 {
			name = fmt.Sprintf("%s (%d)", base, i)
		}
		var n int
		if err := tx.QueryRow(ctx, `select count(*) from b2b_mapping where name=$1`, name).Scan(&n); err != nil {
			return "", err
		}
		if n == 0 {
			return name, nil
		}
	}
	return base + " " + ids.New().String()[:8], nil
}

// --- tool ---

type mappingSaveTool struct{ m *Module }

func (mappingSaveTool) Name() string     { return "mapping.save" }
func (mappingSaveTool) Scope() []string  { return []string{auth.RoleIntegration, auth.RoleOps} }
func (mappingSaveTool) Risk() string     { return "low" }
func (mappingSaveTool) Reversible() bool { return true }

// runCases evaluates stored regression cases against a definition and reports the failures.
func (m *Module) runCases(def MappingDef, cases []MappingCase) []map[string]any {
	var fails []map[string]any
	for i, c := range cases {
		vals, bad := m.Mapping.EvalRow(def, c.Row)
		if len(bad) == 0 && reflect.DeepEqual(vals, c.Expected) {
			continue
		}
		fails = append(fails, map[string]any{"case": i + 1, "expected": c.Expected, "got": vals, "problems": bad})
	}
	return fails
}

func (t mappingSaveTool) Execute(ctx context.Context, tx pgx.Tx, input json.RawMessage) (any, any, error) {
	var in mappingSaveInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, nil, err
	}
	if t.m.Mapping == nil {
		return nil, nil, errors.New("the mapping studio is not available")
	}
	var status string
	if err := tx.QueryRow(ctx, `select status from mapping_sample where id=$1 for update`, in.SampleID).Scan(&status); err != nil {
		return nil, nil, httpx.NotFound("the sample no longer exists")
	}
	if status != "new" && status != "proposed" {
		return nil, nil, fmt.Errorf("the sample is already %s", status)
	}
	// The same deterministic validators as the studio: shape first, then every regression case.
	if err := t.m.Mapping.Check(in.Mapping); err != nil {
		return nil, nil, err
	}
	if fails := t.m.runCases(in.Mapping, in.Cases); len(fails) > 0 {
		b, _ := json.Marshal(fails)
		return nil, nil, fmt.Errorf("regression cases fail against the mapping: %s", b)
	}
	id, err := t.m.Mapping.Save(ctx, tx, in.Mapping)
	if err != nil {
		return nil, nil, err
	}
	for i, c := range in.Cases {
		if _, err := tx.Exec(ctx, `insert into mapping_case (id, tenant_id, mapping_id, sample_id, position, row, expected) values ($1, app_tenant(), $2,$3,$4,$5,$6)`,
			ids.New(), id, in.SampleID, i, c.Row, c.Expected); err != nil {
			return nil, nil, err
		}
	}
	if _, err := tx.Exec(ctx, `update mapping_sample set status='mapped', mapping_id=$2 where id=$1`, in.SampleID, id); err != nil {
		return nil, nil, err
	}
	return map[string]string{"sampleStatus": status}, map[string]any{"mappingId": id, "name": in.Mapping.Name, "regressionCases": len(in.Cases)}, nil
}

func (t mappingSaveTool) Revert(ctx context.Context, tx pgx.Tx, input, _ json.RawMessage) error {
	var in mappingSaveInput
	if err := json.Unmarshal(input, &in); err != nil {
		return err
	}
	var mid *uuid.UUID
	if err := tx.QueryRow(ctx, `select mapping_id from mapping_sample where id=$1`, in.SampleID).Scan(&mid); err != nil || mid == nil {
		return httpx.Conflict("the saved mapping is gone; nothing to revert")
	}
	if err := t.m.Mapping.Remove(ctx, tx, *mid); err != nil { // regression cases cascade
		return err
	}
	_, err := tx.Exec(ctx, `update mapping_sample set status='new', mapping_id=null where id=$1`, in.SampleID)
	return err
}

// --- sample endpoints ---

// MappingSample is a stored sample file (without its content).
type MappingSample struct {
	ID        uuid.UUID  `json:"id"`
	Name      string     `json:"name"`
	PartnerID *uuid.UUID `json:"partnerId,omitempty"`
	DocType   string     `json:"docType"`
	Format    string     `json:"format"`
	Source    string     `json:"source"`
	Error     string     `json:"error"`
	Status    string     `json:"status"`
	MappingID *uuid.UUID `json:"mappingId,omitempty"`
	Size      int        `json:"size"`
	CreatedAt time.Time  `json:"createdAt"`
}

const sampleCols = `id, name, partner_id, doc_type, format, source, error, status, mapping_id, length(content), created_at`

func scanSample(row pgx.Row) (MappingSample, error) {
	var s MappingSample
	err := row.Scan(&s.ID, &s.Name, &s.PartnerID, &s.DocType, &s.Format, &s.Source, &s.Error, &s.Status, &s.MappingID, &s.Size, &s.CreatedAt)
	return s, err
}

func (m *Module) mappingRoutes(r chi.Router) {
	w := auth.Require(writerRoles...)
	r.Get("/agents/mapping/samples", httpx.Handler(m.listSamples))
	r.With(w).Post("/agents/mapping/samples", httpx.Handler(m.createSample))
	r.With(w).Post("/agents/mapping/samples/{id}/dismiss", httpx.Handler(m.dismissSample))
	r.Get("/agents/mapping/cases", httpx.Handler(m.listMappingCases))
	r.With(w).Post("/agents/mapping/regression", httpx.Handler(m.runRegression))
}

func (m *Module) listSamples(w http.ResponseWriter, r *http.Request) error {
	status := r.URL.Query().Get("status")
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `select `+sampleCols+` from mapping_sample where ($1='' or status=$1) order by created_at desc limit 200`, status)
		if err != nil {
			return err
		}
		defer rows.Close()
		items := []MappingSample{}
		for rows.Next() {
			s, err := scanSample(rows)
			if err != nil {
				return err
			}
			items = append(items, s)
		}
		httpx.JSON(w, http.StatusOK, httpx.Page[MappingSample]{Items: items, Total: len(items)})
		return rows.Err()
	})
}

func (m *Module) createSample(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name          string          `json:"name"`
		PartnerID     *uuid.UUID      `json:"partnerId"`
		DocType       string          `json:"docType"`
		Format        string          `json:"format"`
		Options       json.RawMessage `json:"options"`
		Content       string          `json:"content"`
		ContentBase64 string          `json:"contentBase64"`
		Source        string          `json:"source"`
		Error         string          `json:"error"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return httpx.BadRequest("name is required")
	}
	if m.Mapping == nil || !m.Mapping.ValidDocType(in.DocType) {
		return httpx.BadRequest("docType must be order, asn, invoice, inventory, return, chargeback or epcis")
	}
	switch in.Format {
	case "":
		in.Format = "unknown"
	case "csv", "json", "flat", "xml", "edi", "unknown":
	default:
		return httpx.BadRequest("format must be csv, json, flat, xml, edi or unknown (detected)")
	}
	switch in.Source {
	case "":
		in.Source = "upload"
	case "upload", "failed_import":
	default:
		return httpx.BadRequest("source must be upload or failed_import")
	}
	content := []byte(in.Content)
	if in.ContentBase64 != "" {
		var err error
		if content, err = base64.StdEncoding.DecodeString(in.ContentBase64); err != nil {
			return httpx.BadRequest("contentBase64 must be base64")
		}
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return httpx.BadRequest("content is required")
	}
	if len(content) > maxSampleBytes || !utf8.Valid(content) {
		return httpx.BadRequest("a sample must be UTF-8 text of at most 1 MiB")
	}
	if len(in.Options) == 0 {
		in.Options = json.RawMessage(`{}`)
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		if in.PartnerID != nil {
			var n int
			if err := tx.QueryRow(r.Context(), `select count(*) from partner where id=$1`, *in.PartnerID).Scan(&n); err != nil || n == 0 {
				return httpx.BadRequest("unknown partnerId")
			}
		}
		s, err := scanSample(tx.QueryRow(r.Context(), `insert into mapping_sample (id, tenant_id, name, partner_id, doc_type, format, options, content, source, error, created_by)
			values ($1, app_tenant(), $2,$3,$4,$5,$6,$7,$8,$9,$10) returning `+sampleCols,
			ids.New(), in.Name, in.PartnerID, in.DocType, in.Format, in.Options, string(content), in.Source, in.Error, auth.MustFrom(r.Context()).Actor()))
		if err != nil {
			return err
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "mapping.sample.create", Entity: "mapping_sample", EntityID: s.ID.String(), After: s}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusCreated, s)
		return nil
	})
}

func (m *Module) dismissSample(w http.ResponseWriter, r *http.Request) error {
	id, err := ids.Param(r, "id")
	if err != nil {
		return err
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		s, err := scanSample(tx.QueryRow(r.Context(), `update mapping_sample set status='dismissed' where id=$1 and status in ('new','unsupported') returning `+sampleCols, id))
		if err != nil {
			return httpx.Conflict("only samples that are not yet mapped can be dismissed")
		}
		if _, err := audit.Write(r.Context(), tx, audit.Record{Action: "mapping.sample.dismiss", Entity: "mapping_sample", EntityID: id.String()}); err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, s)
		return nil
	})
}

type storedCase struct {
	ID       uuid.UUID         `json:"id"`
	Position int               `json:"position"`
	Row      map[string]string `json:"row"`
	Expected map[string]string `json:"expected"`
}

func loadCases(ctx context.Context, tx pgx.Tx, mappingID uuid.UUID) ([]storedCase, error) {
	rows, err := tx.Query(ctx, `select id, position, row, expected from mapping_case where mapping_id=$1 order by position`, mappingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []storedCase{}
	for rows.Next() {
		var c storedCase
		if err := rows.Scan(&c.ID, &c.Position, &c.Row, &c.Expected); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (m *Module) listMappingCases(w http.ResponseWriter, r *http.Request) error {
	id, err := uuid.Parse(r.URL.Query().Get("mappingId"))
	if err != nil {
		return httpx.BadRequest("mappingId is required")
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		cs, err := loadCases(r.Context(), tx, id)
		if err == nil {
			httpx.JSON(w, http.StatusOK, httpx.Page[storedCase]{Items: cs, Total: len(cs)})
		}
		return err
	})
}

// runRegression re-runs a mapping's stored cases against the mapping as it is saved now, so a
// later edit that changes a canonical value is noticed.
func (m *Module) runRegression(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		MappingID uuid.UUID `json:"mappingId"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if m.Mapping == nil {
		return httpx.Conflict("the mapping studio is not available")
	}
	return m.Tx(r.Context(), func(tx pgx.Tx) error {
		def, err := m.Mapping.Load(r.Context(), tx, in.MappingID)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return httpx.NotFound("unknown mapping")
			}
			return err
		}
		cs, err := loadCases(r.Context(), tx, in.MappingID)
		if err != nil {
			return err
		}
		mc := make([]MappingCase, len(cs))
		for i, c := range cs {
			mc[i] = MappingCase{Row: c.Row, Expected: c.Expected}
		}
		fails := m.runCases(def, mc)
		if fails == nil {
			fails = []map[string]any{}
		}
		sort.Slice(fails, func(i, j int) bool { return fails[i]["case"].(int) < fails[j]["case"].(int) })
		httpx.JSON(w, http.StatusOK, map[string]any{"mappingId": in.MappingID, "cases": len(cs), "passed": len(cs) - len(fails), "failures": fails})
		return nil
	})
}
