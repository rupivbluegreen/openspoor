package link

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openspoor/openspoor/pkg/edi"
	lib "github.com/openspoor/openspoor/pkg/epcis"
)

// Mapping studio engine: turns a partner's CSV, JSON, XML, X12, EDIFACT or fixed-width file into
// canonical B2B documents (pkg/edi) or EPCIS events, field by field, with simple transforms.

// Transform is one step applied to a field value.
type Transform struct {
	// Op is trim, upper, lower, date, lookup, constant or concat.
	Op string `json:"op"`
	// From and To are date patterns using the tokens YYYY, YY, MM, DD, HH, mm, ss (op date).
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// Table maps input values to output values (op lookup).
	Table map[string]string `json:"table,omitempty"`
	// Value is the constant (op constant) or the fallback for a lookup miss (op lookup).
	Value string `json:"value,omitempty"`
	// Sources are further columns appended by op concat, joined with Separator.
	Sources   []string `json:"sources,omitempty"`
	Separator string   `json:"separator,omitempty"`
}

// MappingField maps one source column (CSV header, JSON path, fixed-width column) to a target field.
type MappingField struct {
	Target     string      `json:"target"`
	Source     string      `json:"source,omitempty"`
	Transforms []Transform `json:"transforms,omitempty"`
}

// FlatColumn is a named fixed-width column (Start is 1-based).
type FlatColumn struct {
	Name   string `json:"name"`
	Start  int    `json:"start"`
	Length int    `json:"length"`
}

// MappingOptions tune how the input file is read.
type MappingOptions struct {
	// Delimiter for CSV (default ","; "\t" or "tab" for tab-separated).
	Delimiter string `json:"delimiter,omitempty"`
	// NoHeader: the CSV has no header row; columns are named col1, col2, ...
	NoHeader bool `json:"noHeader,omitempty"`
	// RecordsPath is the dotted path to the array of records in a JSON or XML file (default: the root).
	RecordsPath string `json:"recordsPath,omitempty"`
	// LinesPath is the dotted path, inside each JSON or XML record, to its array of lines. For X12
	// and EDIFACT it is the ID of the segment that opens a line (PO1, LIN ...); empty detects it.
	LinesPath string `json:"linesPath,omitempty"`
	// Columns define a fixed-width (flat) file.
	Columns []FlatColumn `json:"columns,omitempty"`
	// SkipLines skips leading lines of a flat file (headers).
	SkipLines int `json:"skipLines,omitempty"`
}

// MappingSpec is the definition applied to a file.
type MappingSpec struct {
	DocType string         `json:"docType"`
	Format  string         `json:"format"`
	Options MappingOptions `json:"options"`
	Fields  []MappingField `json:"fields"`
}

// RowError is a problem with one input row (1-based, header excluded).
type RowError struct {
	Row     int    `json:"row"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

// MapResult is the outcome of applying a mapping.
type MapResult struct {
	Columns   []string            `json:"columns"`
	Rows      int                 `json:"rows"`
	Sample    []map[string]string `json:"sample"`
	Documents []edi.Document      `json:"documents,omitempty"`
	Events    []lib.Event         `json:"events,omitempty"`
	Errors    []RowError          `json:"errors"`
}

// epcisDocType is the mapping target that produces EPCIS events instead of B2B documents.
const epcisDocType = "epcis"

var epcisTargets = []string{"type", "action", "eventTime", "eventTimeZoneOffset", "bizStep", "disposition", "readPoint", "bizLocation", "epcList", "parentID", "childEPCs",
	"quantity.epcClass", "quantity.quantity", "quantity.uom", "bizTransaction.type", "bizTransaction.id"}

// MappingTargets lists the target fields for a document type: for B2B types they come from
// the registry type (header fields, then line fields with a "lines." prefix).
// references.<key> is open-ended.
func MappingTargets(docType string) []string {
	if docType == epcisDocType {
		return epcisTargets
	}
	if t, ok := edi.Lookup(docType); ok {
		return t.Targets()
	}
	return nil
}

func validTarget(docType, t string) bool {
	if docType != epcisDocType && strings.HasPrefix(t, "references.") && len(t) > len("references.") {
		return true
	}
	for _, x := range MappingTargets(docType) {
		if x == t {
			return true
		}
	}
	return false
}

func validDocType(t string) bool {
	if t == epcisDocType {
		return true
	}
	_, ok := edi.Lookup(t)
	return ok
}

// Validate checks the spec's shape (not the input).
func (s *MappingSpec) Validate() error {
	if !validDocType(s.DocType) {
		return fmt.Errorf("docType must be epcis or a B2B transaction type (GET /b2b/types), got %q", s.DocType)
	}
	switch s.Format {
	case "csv", "json", "xml":
	case "x12", "edifact":
		if p := s.Options.LinesPath; p != "" && !segmentIDRE.MatchString(p) {
			return fmt.Errorf("linesPath for %s is a segment ID such as PO1 or LIN, got %q", s.Format, p)
		}
	case "flat":
		if len(s.Options.Columns) == 0 {
			return errors.New("a flat (fixed-width) mapping needs options.columns")
		}
		for _, c := range s.Options.Columns {
			if c.Name == "" || c.Start < 1 || c.Length < 1 {
				return fmt.Errorf("flat column %q needs a name, start >= 1 and length >= 1", c.Name)
			}
		}
	default:
		return errors.New("format must be " + formatList)
	}
	if d := s.Options.Delimiter; d != "" && d != "tab" && d != `\t` && len([]rune(d)) != 1 {
		return errors.New("delimiter must be a single character or tab")
	}
	if len(s.Fields) == 0 {
		return errors.New("at least one field mapping is required")
	}
	seen := map[string]bool{}
	for _, f := range s.Fields {
		if !validTarget(s.DocType, f.Target) {
			return fmt.Errorf("unknown target field %q for %s", f.Target, s.DocType)
		}
		if seen[f.Target] {
			return fmt.Errorf("target field %q is mapped twice", f.Target)
		}
		seen[f.Target] = true
		constant := false
		for _, t := range f.Transforms {
			switch t.Op {
			case "trim", "upper", "lower":
			case "date":
				if t.From == "" {
					return fmt.Errorf("%s: date transform needs a from pattern", f.Target)
				}
			case "lookup":
				if len(t.Table) == 0 {
					return fmt.Errorf("%s: lookup transform needs a table", f.Target)
				}
			case "constant":
				constant = true
			case "concat":
				if len(t.Sources) == 0 {
					return fmt.Errorf("%s: concat transform needs sources", f.Target)
				}
			default:
				return fmt.Errorf("%s: unknown transform %q (trim, upper, lower, date, lookup, constant, concat)", f.Target, t.Op)
			}
		}
		if f.Source == "" && !constant && !hasConcat(f) {
			return fmt.Errorf("%s: a source column or a constant is required", f.Target)
		}
	}
	if s.DocType != "epcis" && !seen["number"] {
		return errors.New("the document number must be mapped (it also groups rows into documents)")
	}
	return nil
}

func hasConcat(f MappingField) bool {
	for _, t := range f.Transforms {
		if t.Op == "concat" {
			return true
		}
	}
	return false
}

// record is one input record: header-level values and its lines (relative values).
type record struct {
	row    int
	values map[string]string
	lines  []map[string]string
}

const maxRows = 50_000

func readRecords(s MappingSpec, content []byte) ([]record, []string, error) {
	switch s.Format {
	case "csv":
		return readCSV(s.Options, content)
	case "flat":
		return readFlat(s.Options, content)
	case "x12", "edifact":
		return readEDI(s.Format, s.Options, content)
	case "xml":
		return readXML(s.Options, content)
	default:
		return readJSON(s.Options, content)
	}
}

func readCSV(o MappingOptions, content []byte) ([]record, []string, error) {
	r := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))))
	switch o.Delimiter {
	case "":
	case "tab", `\t`:
		r.Comma = '\t'
	default:
		r.Comma = []rune(o.Delimiter)[0]
	}
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	var header []string
	var out []record
	for {
		row, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("cannot read CSV: %w", err)
		}
		if header == nil && !o.NoHeader {
			header = make([]string, len(row))
			for i, h := range row {
				header[i] = strings.TrimSpace(h)
			}
			continue
		}
		if len(row) == 1 && strings.TrimSpace(row[0]) == "" {
			continue
		}
		vals := map[string]string{}
		for i, v := range row {
			name := ""
			if i < len(header) {
				name = header[i]
			}
			if name == "" {
				name = "col" + strconv.Itoa(i+1)
			}
			vals[name] = v
		}
		if o.NoHeader && len(row) > len(header) {
			header = header[:0]
			for i := range row {
				header = append(header, "col"+strconv.Itoa(i+1))
			}
		}
		out = append(out, record{row: len(out) + 1, values: vals, lines: []map[string]string{vals}})
		if len(out) > maxRows {
			return nil, nil, fmt.Errorf("files are limited to %d rows", maxRows)
		}
	}
	return out, header, nil
}

func readFlat(o MappingOptions, content []byte) ([]record, []string, error) {
	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	var out []record
	cols := make([]string, len(o.Columns))
	for i, c := range o.Columns {
		cols[i] = c.Name
	}
	for i, l := range lines {
		if i < o.SkipLines || strings.TrimSpace(l) == "" {
			continue
		}
		rs := []rune(l)
		vals := map[string]string{}
		for _, c := range o.Columns {
			start := c.Start - 1
			end := start + c.Length
			if start >= len(rs) {
				vals[c.Name] = ""
				continue
			}
			if end > len(rs) {
				end = len(rs)
			}
			vals[c.Name] = string(rs[start:end])
		}
		out = append(out, record{row: len(out) + 1, values: vals, lines: []map[string]string{vals}})
		if len(out) > maxRows {
			return nil, nil, fmt.Errorf("files are limited to %d rows", maxRows)
		}
	}
	return out, cols, nil
}

func readJSON(o MappingOptions, content []byte) ([]record, []string, error) {
	dec := json.NewDecoder(bytes.NewReader(content))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, nil, fmt.Errorf("cannot read JSON: %w", err)
	}
	return readTree(o, root)
}

// readTree turns a decoded JSON or XML tree into records: RecordsPath selects the records, LinesPath
// the lines inside each (a single object counts as one line).
func readTree(o MappingOptions, root any) ([]record, []string, error) {
	node := root
	if o.RecordsPath != "" {
		var ok bool
		if node, ok = lookupPath(root, o.RecordsPath); !ok {
			return nil, nil, fmt.Errorf("recordsPath %q not found", o.RecordsPath)
		}
	}
	items, ok := node.([]any)
	if !ok {
		items = []any{node}
	}
	if len(items) > maxRows {
		return nil, nil, fmt.Errorf("files are limited to %d rows", maxRows)
	}
	colSet := map[string]bool{}
	var out []record
	for i, it := range items {
		var lineNodes []any
		recNode := it
		if o.LinesPath != "" {
			if ln, ok := lookupPath(it, o.LinesPath); ok {
				switch x := ln.(type) {
				case []any:
					lineNodes = x
				case map[string]any:
					lineNodes = []any{x}
				}
			}
			recNode = withoutPath(it, o.LinesPath)
		}
		vals := map[string]string{}
		flatten("", recNode, vals)
		rec := record{row: i + 1, values: vals}
		if o.LinesPath == "" {
			rec.lines = []map[string]string{vals}
		}
		for _, ln := range lineNodes {
			lv := map[string]string{}
			flatten("", ln, lv)
			rec.lines = append(rec.lines, lv)
			for k := range lv {
				colSet[k] = true
			}
		}
		for k := range vals {
			colSet[k] = true
		}
		out = append(out, rec)
	}
	cols := make([]string, 0, len(colSet))
	for k := range colSet {
		cols = append(cols, k)
	}
	sort.Strings(cols)
	return out, cols, nil
}

func lookupPath(v any, path string) (any, bool) {
	for _, p := range strings.Split(path, ".") {
		switch x := v.(type) {
		case map[string]any:
			var ok bool
			if v, ok = x[p]; !ok {
				return nil, false
			}
		case []any:
			i, err := strconv.Atoi(p)
			if err != nil || i < 0 || i >= len(x) {
				return nil, false
			}
			v = x[i]
		default:
			return nil, false
		}
	}
	return v, true
}

// withoutPath returns a shallow copy of v without the (top-level or nested) key at path.
func withoutPath(v any, path string) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	head, rest, nested := strings.Cut(path, ".")
	cp := make(map[string]any, len(m))
	for k, x := range m {
		cp[k] = x
	}
	if !nested {
		delete(cp, head)
	} else if child, ok := cp[head]; ok {
		cp[head] = withoutPath(child, rest)
	}
	return cp
}

func flatten(prefix string, v any, out map[string]string) {
	join := func(k string) string {
		if prefix == "" {
			return k
		}
		return prefix + "." + k
	}
	switch x := v.(type) {
	case map[string]any:
		for k, c := range x {
			flatten(join(k), c, out)
		}
	case []any:
		for i, c := range x {
			flatten(join(strconv.Itoa(i)), c, out)
		}
	case nil:
		if prefix != "" {
			out[prefix] = ""
		}
	case string:
		out[prefix] = x
	case json.Number:
		out[prefix] = x.String()
	case bool:
		out[prefix] = strconv.FormatBool(x)
	default:
		out[prefix] = fmt.Sprint(x)
	}
}

// layout converts a YYYY/MM/DD/HH/mm/ss pattern to a Go time layout.
func layout(p string) string {
	repl := strings.NewReplacer("YYYY", "2006", "YY", "06", "MM", "01", "DD", "02", "HH", "15", "mm", "04", "ss", "05")
	return repl.Replace(p)
}

// value resolves a field for one line, falling back between line and record values.
func (f MappingField) value(line, rec map[string]string) (string, error) {
	get := func(col string) string {
		if v, ok := line[col]; ok {
			return v
		}
		return rec[col]
	}
	v := ""
	if f.Source != "" {
		v = get(f.Source)
	}
	for _, t := range f.Transforms {
		switch t.Op {
		case "trim":
			v = strings.TrimSpace(v)
		case "upper":
			v = strings.ToUpper(v)
		case "lower":
			v = strings.ToLower(v)
		case "constant":
			v = t.Value
		case "concat":
			parts := []string{}
			if v != "" {
				parts = append(parts, v)
			}
			for _, s := range t.Sources {
				if x := get(s); x != "" {
					parts = append(parts, x)
				}
			}
			v = strings.Join(parts, t.Separator)
		case "lookup":
			if x, ok := t.Table[v]; ok {
				v = x
			} else if t.Value != "" {
				v = t.Value
			} else {
				return "", fmt.Errorf("no lookup entry for %q", v)
			}
		case "date":
			if strings.TrimSpace(v) == "" {
				continue
			}
			tm, err := time.Parse(layout(t.From), strings.TrimSpace(v))
			if err != nil {
				return "", fmt.Errorf("%q does not match date pattern %s", v, t.From)
			}
			to := t.To
			if to == "" {
				to = "YYYY-MM-DD"
			}
			v = tm.Format(layout(to))
		}
	}
	return v, nil
}

var listSep = regexp.MustCompile(`[\s;,|]+`)

func splitList(s string) []string {
	var out []string
	for _, p := range listSep.Split(strings.TrimSpace(s), -1) {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

var timeLayouts = []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"}

func parseTime(s string) (time.Time, error) {
	for _, l := range timeLayouts {
		if t, err := time.Parse(l, strings.TrimSpace(s)); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not a date (YYYY-MM-DD or RFC 3339); add a date transform", s)
}

func parseNum(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", s)
	}
	return f, nil
}

// ApplyMapping reads content and maps it. The error is returned only when the file cannot be
// read at all; problems with individual rows are reported in MapResult.Errors.
func ApplyMapping(s MappingSpec, content []byte) (MapResult, error) {
	recs, cols, err := readRecords(s, content)
	if err != nil {
		return MapResult{}, err
	}
	res := MapResult{Columns: cols, Errors: []RowError{}, Sample: []map[string]string{}}
	if res.Columns == nil {
		res.Columns = []string{}
	}
	for _, r := range recs {
		res.Rows += max(1, len(r.lines))
		if len(res.Sample) < 20 {
			for _, l := range r.lines {
				if len(res.Sample) >= 20 {
					break
				}
				merged := map[string]string{}
				for k, v := range r.values {
					merged[k] = v
				}
				for k, v := range l {
					merged[k] = v
				}
				res.Sample = append(res.Sample, merged)
			}
			if len(r.lines) == 0 {
				res.Sample = append(res.Sample, r.values)
			}
		}
	}
	switch {
	case len(s.Fields) == 0: // nothing mapped yet: columns and sample rows only
	case s.DocType == "epcis":
		res.Events = mapEvents(s, recs, &res.Errors)
	default:
		res.Documents = mapDocs(s, recs, &res.Errors)
	}
	return res, nil
}

func mapDocs(s MappingSpec, recs []record, errs *[]RowError) []edi.Document {
	type group struct {
		row int
		doc edi.Document
	}
	var order []string
	groups := map[string]*group{}
	for _, r := range recs {
		lines := r.lines
		if len(lines) == 0 {
			lines = []map[string]string{{}}
		}
		for _, ln := range lines {
			vals := map[string]string{}
			bad := false
			for _, f := range s.Fields {
				v, err := f.value(ln, r.values)
				if err != nil {
					*errs = append(*errs, RowError{Row: r.row, Field: f.Target, Message: err.Error()})
					bad = true
					continue
				}
				vals[f.Target] = v
			}
			if bad {
				continue
			}
			num := vals["number"]
			if num == "" {
				*errs = append(*errs, RowError{Row: r.row, Field: "number", Message: "document number is empty"})
				continue
			}
			g, ok := groups[num]
			if !ok {
				g = &group{row: r.row, doc: edi.Document{Type: s.DocType, Number: num}}
				if err := setHeader(&g.doc, vals); err != nil {
					*errs = append(*errs, RowError{Row: r.row, Field: err.field, Message: err.msg})
					continue
				}
				groups[num] = g
				order = append(order, num)
			}
			if hasLine(vals) {
				l, ferr := makeLine(vals)
				if ferr != nil {
					*errs = append(*errs, RowError{Row: r.row, Field: ferr.field, Message: ferr.msg})
					continue
				}
				g.doc.Lines = append(g.doc.Lines, l)
			}
		}
	}
	out := make([]edi.Document, 0, len(order))
	for _, n := range order {
		g := groups[n]
		if err := g.doc.Validate(); err != nil {
			*errs = append(*errs, RowError{Row: g.row, Message: "document " + n + ": " + err.Error()})
		}
		out = append(out, g.doc)
	}
	return out
}

type fieldErr struct{ field, msg string }

func setHeader(d *edi.Document, v map[string]string) *fieldErr {
	if s := v["date"]; s != "" {
		t, err := parseTime(s)
		if err != nil {
			return &fieldErr{"date", err.Error()}
		}
		d.Date = t
	}
	d.Currency = v["currency"]
	d.Buyer = edi.Party{Name: v["buyer.name"], GLN: v["buyer.gln"]}
	d.Seller = edi.Party{Name: v["seller.name"], GLN: v["seller.gln"]}
	d.ShipTo = edi.Party{Name: v["shipTo.name"], GLN: v["shipTo.gln"]}
	d.SSCCs = splitList(v["ssccs"])
	d.Status, d.Note = v["status"], v["note"]
	for _, k := range sortedKeys(v) {
		x := v[k]
		if x == "" {
			continue
		}
		switch {
		case strings.HasPrefix(k, "references."):
			if d.References == nil {
				d.References = map[string]string{}
			}
			d.References[strings.TrimPrefix(k, "references.")] = x
		case strings.HasPrefix(k, "dates."):
			if _, err := time.Parse("2006-01-02", x); err != nil {
				return &fieldErr{k, fmt.Sprintf("%q is not YYYY-MM-DD; add a date transform", x)}
			}
			if d.Dates == nil {
				d.Dates = map[string]string{}
			}
			d.Dates[strings.TrimPrefix(k, "dates.")] = x
		case strings.HasPrefix(k, "parties."):
			key, field, _ := strings.Cut(strings.TrimPrefix(k, "parties."), ".")
			if d.Parties == nil {
				d.Parties = map[string]edi.Party{}
			}
			p := d.Parties[key]
			if field == "gln" {
				p.GLN = x
			} else {
				p.Name = x
			}
			d.Parties[key] = p
		case strings.HasPrefix(k, "amounts."):
			n, err := parseNum(x)
			if err != nil {
				return &fieldErr{k, err.Error()}
			}
			if d.Amounts == nil {
				d.Amounts = map[string]float64{}
			}
			d.Amounts[strings.TrimPrefix(k, "amounts.")] = n
		}
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func hasLine(v map[string]string) bool {
	for k, x := range v {
		if strings.HasPrefix(k, "lines.") && x != "" {
			return true
		}
	}
	return false
}

func makeLine(v map[string]string) (edi.Line, *fieldErr) {
	l := edi.Line{GTIN: v["lines.gtin"], Description: v["lines.description"], UOM: v["lines.uom"], Lot: v["lines.lot"], Reason: v["lines.reason"],
		Serials: splitList(v["lines.serials"]), Status: v["lines.status"], Ref: v["lines.ref"], RefType: v["lines.refType"]}
	var err error
	if l.Amount, err = parseNum(v["lines.amount"]); err != nil {
		return l, &fieldErr{"lines.amount", err.Error()}
	}
	for _, k := range sortedKeys(v) {
		if key, ok := strings.CutPrefix(k, "lines.dates."); ok && v[k] != "" {
			if _, err := time.Parse("2006-01-02", v[k]); err != nil {
				return l, &fieldErr{k, fmt.Sprintf("%q is not YYYY-MM-DD; add a date transform", v[k])}
			}
			if l.Dates == nil {
				l.Dates = map[string]string{}
			}
			l.Dates[key] = v[k]
		}
	}
	if l.Quantity, err = parseNum(v["lines.quantity"]); err != nil {
		return l, &fieldErr{"lines.quantity", err.Error()}
	}
	if l.UnitPrice, err = parseNum(v["lines.unitPrice"]); err != nil {
		return l, &fieldErr{"lines.unitPrice", err.Error()}
	}
	if e := v["lines.expiry"]; e != "" {
		if _, err := time.Parse("2006-01-02", e); err != nil {
			return l, &fieldErr{"lines.expiry", fmt.Sprintf("%q is not YYYY-MM-DD; add a date transform", e)}
		}
		l.Expiry = e
	}
	return l, nil
}

func mapEvents(s MappingSpec, recs []record, errs *[]RowError) []lib.Event {
	var out []lib.Event
	for _, r := range recs {
		lines := r.lines
		if len(lines) == 0 {
			lines = []map[string]string{{}}
		}
		for _, ln := range lines {
			vals := map[string]string{}
			bad := false
			for _, f := range s.Fields {
				v, err := f.value(ln, r.values)
				if err != nil {
					*errs = append(*errs, RowError{Row: r.row, Field: f.Target, Message: err.Error()})
					bad = true
					continue
				}
				vals[f.Target] = v
			}
			if bad {
				continue
			}
			ev, ferr := makeEvent(vals)
			if ferr != nil {
				*errs = append(*errs, RowError{Row: r.row, Field: ferr.field, Message: ferr.msg})
				continue
			}
			for _, fe := range lib.Validate(len(out), &ev) {
				*errs = append(*errs, RowError{Row: r.row, Field: fe.Field, Message: fe.Message})
			}
			out = append(out, ev)
		}
	}
	return out
}

func makeEvent(v map[string]string) (lib.Event, *fieldErr) {
	ev := lib.Event{Type: v["type"], Action: v["action"], BizStep: v["bizStep"], Disposition: v["disposition"], ParentID: v["parentID"],
		EPCList: splitList(v["epcList"]), ChildEPCs: splitList(v["childEPCs"]), EventTimeZoneOffset: v["eventTimeZoneOffset"]}
	if ev.Type == "" {
		ev.Type = lib.ObjectEvent
	}
	if s := v["eventTime"]; s != "" {
		t, err := parseTime(s)
		if err != nil {
			return ev, &fieldErr{"eventTime", err.Error()}
		}
		ev.EventTime = t
		if ev.EventTimeZoneOffset == "" {
			ev.EventTimeZoneOffset = t.Format("-07:00")
		}
	}
	if s := v["readPoint"]; s != "" {
		ev.ReadPoint = &lib.Location{ID: s}
	}
	if s := v["bizLocation"]; s != "" {
		ev.BizLocation = &lib.Location{ID: s}
	}
	if c := v["quantity.epcClass"]; c != "" {
		q := lib.QuantityElement{EPCClass: c, UOM: v["quantity.uom"]}
		if s := v["quantity.quantity"]; s != "" {
			f, err := parseNum(s)
			if err != nil {
				return ev, &fieldErr{"quantity.quantity", err.Error()}
			}
			q.Quantity = &f
		}
		ev.QuantityList = []lib.QuantityElement{q}
	}
	if id := v["bizTransaction.id"]; id != "" {
		ev.BizTransactionList = []lib.BizTransaction{{Type: v["bizTransaction.type"], BizTransaction: id}}
	}
	return ev, nil
}

// synonyms drive the "suggest mapping" heuristic (normalised header names).
var synonyms = map[string][]string{
	"number":            {"number", "po", "ponumber", "pono", "ordernumber", "orderno", "orderid", "purchaseorder", "docnumber", "documentnumber", "invoicenumber", "invoiceno", "asnnumber", "despatchnumber"},
	"date":              {"date", "orderdate", "docdate", "documentdate", "invoicedate", "shipdate", "despatchdate"},
	"currency":          {"currency", "cur", "ccy", "currencycode"},
	"buyer.name":        {"buyer", "buyername", "customer", "customername", "soldto", "soldtoname"},
	"buyer.gln":         {"buyergln", "customergln", "soldtogln"},
	"seller.name":       {"seller", "sellername", "supplier", "suppliername", "vendor", "vendorname"},
	"seller.gln":        {"sellergln", "suppliergln", "vendorgln"},
	"shipTo.name":       {"shipto", "shiptoname", "deliverto", "delivertoname"},
	"shipTo.gln":        {"shiptogln", "delivertogln", "deliverygln"},
	"lines.gtin":        {"gtin", "gtin14", "ean", "upc", "barcode", "itemgtin", "productgtin"},
	"lines.description": {"description", "desc", "itemdescription", "productname", "productdescription", "itemname"},
	"lines.quantity":    {"qty", "quantity", "orderqty", "orderedqty", "quantityordered", "units", "shippedqty"},
	"lines.uom":         {"uom", "unit", "unitofmeasure", "uomcode"},
	"lines.unitPrice":   {"price", "unitprice", "netprice", "priceperunit", "unitcost"},
	"lines.lot":         {"lot", "batch", "lotnumber", "batchnumber", "lotno", "batchno"},
	"lines.expiry":      {"expiry", "exp", "expirydate", "expirationdate", "expdate", "bestbefore"},
	"lines.serials":     {"serial", "serials", "serialnumber", "serialnumbers", "sn"},
	"lines.reason":      {"reason", "reasoncode", "returnreason"},
	"type":              {"eventtype", "type"},
	"action":            {"action"},
	"eventTime":         {"eventtime", "time", "timestamp", "datetime", "date"},
	"bizStep":           {"bizstep", "businessstep", "step"},
	"disposition":       {"disposition", "status"},
	"readPoint":         {"readpoint", "location", "locationgln"},
	"bizLocation":       {"bizlocation", "businesslocation"},
	"epcList":           {"epc", "epcs", "epclist", "sgtin", "sscc"},
	"parentID":          {"parentid", "parent", "parentsscc"},
	"childEPCs":         {"childepcs", "children"},
	"quantity.epcClass": {"epcclass", "lgtin", "class"},
	"quantity.quantity": {"qty", "quantity"},
	"quantity.uom":      {"uom", "unit"},
	"bizTransaction.id": {"biztransaction", "transaction", "po", "ponumber"},
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]`)

func normHeader(h string) string {
	if i := strings.LastIndex(h, "."); i >= 0 {
		if _, err := strconv.Atoi(h[i+1:]); err != nil {
			h = h[i+1:]
		}
	}
	return nonAlnum.ReplaceAllString(strings.ToLower(h), "")
}

// ediHints lists, per target, the source paths (see readEDI) where X12 and EDIFACT messages
// usually carry the value, in order of preference. They follow the segment layouts pkg/edi writes
// and reads; a suggestion is only made when the path occurs in the sample.
var ediHints = map[string][]string{
	"number":   {"BGM.02", "BEG.03", "BIG.02", "BSN.02", "BIA.03", "BGN.02", "BCP.02", "BFR.02", "BRA.01", "BPT.02"},
	"date":     {"DTM(137).01.02", "BEG.05", "BIG.01", "BSN.03", "BIA.04", "BGN.03", "BCP.03", "BRA.02", "BPT.03"},
	"currency": {"CUX.01.02", "CUR.02"},

	"buyer.name":  {"NAD(BY).04.01", "NAD(BY).04", "N1(BY).02"},
	"buyer.gln":   {"NAD(BY).02.01", "NAD(BY).02", "N1(BY).04"},
	"seller.name": {"NAD(SU).04.01", "NAD(SU).04", "N1(SE).02", "N1(SF).02", "N1(RE).02"},
	"seller.gln":  {"NAD(SU).02.01", "NAD(SU).02", "N1(SE).04", "N1(SF).04", "N1(RE).04"},
	"shipTo.name": {"NAD(DP).04.01", "NAD(DP).04", "N1(ST).02"},
	"shipTo.gln":  {"NAD(DP).02.01", "NAD(DP).02", "N1(ST).04"},

	"references.po":       {"RFF(ON).01.02", "REF(PO).02", "PRF.01", "BIG.04"},
	"references.asn":      {"RFF(AAK).01.02"},
	"references.invoice":  {"RFF(IV).01.02", "REF(IV).02"},
	"references.contract": {"RFF(CT).01.02", "REF(CT).02", "BCP.05"},

	"dates.delivery_requested": {"DTM(2).01.02", "DTM(002).02"},
	"dates.ship":               {"DTM(11).01.02", "DTM(011).02"},
	"dates.delivery_estimated": {"DTM(17).01.02", "DTM(017).02"},
	"dates.delivered":          {"DTM(35).01.02", "DTM(035).02"},
	"dates.received":           {"DTM(50).01.02", "DTM(050).02"},
	"dates.effective":          {"DTM(7).01.02", "DTM(007).02"},
	"dates.expiry":             {"DTM(36).01.02", "DTM(036).02"},
	"dates.period_start":       {"DTM(194).01.02", "DTM(150).02"},
	"dates.period_end":         {"DTM(206).01.02", "DTM(151).02"},

	"lines.gtin":        {"LIN.03.01", "PO1.07", "IT1.07", "LIN.03", "POC.09", "BLI.02"},
	"lines.description": {"IMD.03.04", "PID.05"},
	"lines.quantity":    {"QTY.01.02", "PO1.02", "IT1.02", "POC.03", "SN1.02", "BLI.03", "FST.01", "RCD.02", "ZA.02", "QTY.02"},
	"lines.uom":         {"QTY.01.03", "PO1.03", "IT1.03", "POC.05", "SN1.03", "BLI.05", "UIT.01", "RCD.03", "ZA.03", "QTY.03"},
	"lines.unitPrice":   {"PRI.01.02", "PO1.04", "IT1.04", "POC.06"},
	"lines.lot":         {"PIA.02.01", "REF(LT).02", "LIN.05"},
	"lines.expiry":      {"DTM(361).01.02", "DTM(036).02"},
	"lines.serials":     {"GIN(BN).02.01", "REF(SE).02"},
	"lines.reason":      {"RDR.03", "NTE(ADD).02", "FTX(AAI).04.01"},
}

// isDateTarget reports targets whose EDI value is a CCYYMMDD date.
func isDateTarget(t string) bool {
	return t == "date" || strings.HasPrefix(t, "dates.") || t == "lines.expiry"
}

// SuggestMapping proposes field mappings from column names. A person reviews and saves them.
// Columns that look like X12 or EDIFACT paths (BEG.03, DTM(137).01.02) are matched against the
// usual segment positions; other columns, including XML and JSON paths, by their last name part.
func SuggestMapping(docType string, columns []string) []MappingField {
	out := []MappingField{}
	used := map[string]bool{}
	present := make(map[string]bool, len(columns))
	for _, c := range columns {
		present[c] = true
	}
	for _, target := range MappingTargets(docType) {
		if docType != epcisDocType {
			if src := firstPresent(ediHints[target], present, used); src != "" {
				used[src] = true
				f := MappingField{Target: target, Source: src, Transforms: []Transform{{Op: "trim"}}}
				if isDateTarget(target) {
					f.Transforms = append(f.Transforms, Transform{Op: "date", From: "YYYYMMDD"})
				}
				out = append(out, f)
				continue
			}
		}
		syn := synonyms[target]
		for _, c := range columns {
			if used[c] {
				continue
			}
			n := normHeader(c)
			match := false
			for _, s := range syn {
				match = match || n == s
			}
			if match {
				used[c] = true
				out = append(out, MappingField{Target: target, Source: c, Transforms: []Transform{{Op: "trim"}}})
				break
			}
		}
	}
	return out
}

func firstPresent(cands []string, present, used map[string]bool) string {
	for _, c := range cands {
		if present[c] && !used[c] {
			return c
		}
	}
	return ""
}
