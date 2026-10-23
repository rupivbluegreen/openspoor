package edi

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Category groups transaction types.
type Category string

// Categories.
const (
	CatPlanning   Category = "planning"
	CatOrdering   Category = "ordering"
	CatFulfilment Category = "fulfilment"
	CatInvoicing  Category = "invoicing"
	CatInventory  Category = "inventory"
	CatReturns    Category = "returns_claims"
	CatCompliance Category = "compliance"
	CatMasterData Category = "master_data"
)

// Categories lists the categories in display order.
var Categories = []Category{CatPlanning, CatOrdering, CatFulfilment, CatInvoicing, CatInventory, CatReturns, CatCompliance, CatMasterData}

// Flow is the usual direction of a transaction between the two trading parties.
type Flow string

// Flows.
const (
	FlowBuyerToSeller Flow = "buyer_to_seller"
	FlowSellerToBuyer Flow = "seller_to_buyer"
	FlowEither        Flow = "either"
)

// LineKind says what a document's lines describe.
type LineKind string

// Line kinds.
const (
	// LinesItem lines are trade items (GTIN, quantity, price, lot ...).
	LinesItem LineKind = "item"
	// LinesReference lines point at other documents (ref, refType, status, amount).
	LinesReference LineKind = "reference"
)

// Rule is a declarative validation rule. ID selects the check, Args parameterise it.
type Rule struct {
	ID          string   `json:"id"`
	Args        []string `json:"args,omitempty"`
	Description string   `json:"description"`
}

// Type describes one canonical transaction type: what it is called, which public standard
// identifiers it corresponds to, what a valid document must contain, and how the generic
// writers render it. The registry is data: adding a type is adding an entry to Types.
type Type struct {
	Key         string
	Name        string
	Description string
	Category    Category
	Flow        Flow
	// X12 is the ASC X12 transaction set number ("" when none is attributed).
	X12 string
	// EDIFACT is the UN/EDIFACT message type ("" when none is attributed).
	EDIFACT string
	// EDIFACTBGM is the document name code (BGM first element); "" when not attributed (TODO(spec)).
	EDIFACTBGM string
	// EDIFACTQty is the QTY qualifier for line quantities (default "1", discrete quantity).
	EDIFACTQty string
	// EDIFACTWrite is true when the common-subset EDIFACT writer is meaningful for the message.
	EDIFACTWrite bool
	// IDocBasic and IDocMessage name the IDoc types (hand-written mappings only).
	IDocBasic, IDocMessage string

	Lines LineKind
	// LineFields lists the line fields the type uses (mapping targets); empty means the defaults of its LineKind.
	LineFields []string
	// Required lists canonical fields that must be present; see fieldPresent for the vocabulary.
	Required []string
	// Statuses and LineStatuses are the allowed canonical status codes (empty: none allowed).
	Statuses     []string
	LineStatuses []string
	// Dates (header), LineDates, Parties (besides buyer, seller, shipTo) and Amounts are the keys the type accepts.
	Dates, LineDates, Parties, Amounts []string
	Rules                              []Rule
	// TODO lists unverified standard details for this type, each starting with "TODO(spec)".
	TODO string

	x12    *x12Template // generic X12 template (nil: none)
	legacy bool         // hand-written X12 writer (original six types)
}

// Standards names the identifiers of a type in each supported standard.
type Standards struct {
	X12         string `json:"x12,omitempty"`
	EDIFACT     string `json:"edifact,omitempty"`
	EDIFACTBGM  string `json:"edifactBgm,omitempty"`
	IDocBasic   string `json:"idocBasic,omitempty"`
	IDocMessage string `json:"idocMessage,omitempty"`
}

// Formats says which renderings and parsers exist for a type.
type Formats struct {
	X12Write     bool `json:"x12Write"`
	X12Read      bool `json:"x12Read"`
	EDIFACTWrite bool `json:"edifactWrite"`
	EDIFACTRead  bool `json:"edifactRead"`
	IDocWrite    bool `json:"idocWrite"`
}

// Info is the public (JSON) description of a type.
type Info struct {
	Key          string    `json:"key"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	Category     Category  `json:"category"`
	Flow         Flow      `json:"flow"`
	Standards    Standards `json:"standards"`
	Formats      Formats   `json:"formats"`
	Lines        LineKind  `json:"lines"`
	Required     []string  `json:"required"`
	Statuses     []string  `json:"statuses,omitempty"`
	LineStatuses []string  `json:"lineStatuses,omitempty"`
	Dates        []string  `json:"dates,omitempty"`
	LineDates    []string  `json:"lineDates,omitempty"`
	Parties      []string  `json:"parties,omitempty"`
	Amounts      []string  `json:"amounts,omitempty"`
	Rules        []Rule    `json:"rules,omitempty"`
	Fields       []string  `json:"fields"`
	Todo         string    `json:"todo,omitempty"`
}

// CanWriteX12 reports whether X12 can render the type.
func (t Type) CanWriteX12() bool { return t.legacy || t.x12 != nil }

// CanReadX12 reports whether ParseX12 understands the type's transaction set.
func (t Type) CanReadX12() bool { return t.Key == "order" || t.x12 != nil }

// CanWriteEDIFACT reports whether EDIFACT can render the type.
func (t Type) CanWriteEDIFACT() bool { return t.EDIFACT != "" && t.EDIFACTWrite }

// CanReadEDIFACT reports whether ParseEDIFACT maps the message to this type.
func (t Type) CanReadEDIFACT() bool { return t.CanWriteEDIFACT() }

// CanWriteIDoc reports whether an IDoc mapping exists.
func (t Type) CanWriteIDoc() bool { return t.IDocBasic != "" }

// Info returns the public description of the type.
func (t Type) Info() Info {
	req := append([]string{}, t.Required...)
	return Info{Key: t.Key, Name: t.Name, Description: t.Description, Category: t.Category, Flow: t.Flow,
		Standards: Standards{X12: t.X12, EDIFACT: t.EDIFACT, EDIFACTBGM: t.EDIFACTBGM, IDocBasic: t.IDocBasic, IDocMessage: t.IDocMessage},
		Formats:   Formats{X12Write: t.CanWriteX12(), X12Read: t.CanReadX12(), EDIFACTWrite: t.CanWriteEDIFACT(), EDIFACTRead: t.CanReadEDIFACT(), IDocWrite: t.CanWriteIDoc()},
		Lines:     t.Lines, Required: req, Statuses: t.Statuses, LineStatuses: t.LineStatuses, Dates: t.Dates, LineDates: t.LineDates,
		Parties: t.Parties, Amounts: t.Amounts, Rules: t.Rules, Fields: t.Targets(), Todo: t.TODO}
}

var (
	byKey     map[string]*Type
	byX12     map[string]*Type
	byEDIFACT map[string][]*Type
)

func init() {
	byKey = make(map[string]*Type, len(Types))
	byX12 = map[string]*Type{}
	byEDIFACT = map[string][]*Type{}
	for i := range Types {
		t := &Types[i]
		if t.Lines == "" {
			t.Lines = LinesItem
		}
		if t.EDIFACTQty == "" {
			t.EDIFACTQty = "1"
		}
		byKey[t.Key] = t
		if t.legacy {
			x12Sets[t.Key], edifactTypes[t.Key], idocTypes[t.Key] = t.X12, t.EDIFACT, [2]string{t.IDocBasic, t.IDocMessage}
		}
		if t.X12 != "" {
			byX12[t.X12] = t
		}
		if t.EDIFACT != "" {
			byEDIFACT[t.EDIFACT] = append(byEDIFACT[t.EDIFACT], t)
		}
	}
}

// Lookup returns the registry entry for a document type key.
func Lookup(key string) (Type, bool) {
	t, ok := byKey[key]
	if !ok {
		return Type{}, false
	}
	return *t, true
}

// LookupX12 returns the type for an X12 transaction set number.
func LookupX12(set string) (Type, bool) {
	t, ok := byX12[set]
	if !ok {
		return Type{}, false
	}
	return *t, true
}

// LookupEDIFACT returns the type for an EDIFACT message type and document name code. When the
// code is not attributed to any type of the message, the first type of that message is returned.
func LookupEDIFACT(msg, bgm string) (Type, bool) {
	if c := candidatesEDIFACT(msg, bgm); len(c) > 0 {
		return c[0], true
	}
	return Type{}, false
}

// candidatesEDIFACT lists the types a message may be: those with the document name code; if
// there are none, those of the message without an attributed code; if there are none, all
// types of the message.
func candidatesEDIFACT(msg, bgm string) []Type {
	for _, match := range []func(t *Type) bool{
		func(t *Type) bool { return bgm != "" && t.EDIFACTBGM == bgm },
		func(t *Type) bool { return t.EDIFACTBGM == "" },
		func(t *Type) bool { return true },
	} {
		var out []Type
		for _, t := range byEDIFACT[msg] {
			if match(t) {
				out = append(out, *t)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

// Keys lists the registered type keys in registry order.
func Keys() []string {
	out := make([]string, len(Types))
	for i, t := range Types {
		out[i] = t.Key
	}
	return out
}

// Infos lists the public description of every type.
func Infos() []Info {
	out := make([]Info, len(Types))
	for i, t := range Types {
		out[i] = t.Info()
	}
	return out
}

// --- shared vocabularies ---

// Code is a canonical key with its identifier in each standard.
type Code struct {
	Key     string
	X12     string // "" when no code is attributed
	EDIFACT string
}

// DateKeys is the vocabulary of named dates (Document.Dates, Line.Dates). Qualifiers are
// X12 DTM01 (data element 374) and EDIFACT DTM 2005 codes.
var DateKeys = []Code{
	{"delivery_requested", "002", "2"},
	{"ship", "011", "11"},
	{"delivery_estimated", "017", "17"},
	{"delivered", "035", "35"},
	{"received", "050", "50"},
	{"effective", "007", "7"},
	{"expiry", "036", "36"},
	{"period_start", "150", "194"},
	{"period_end", "151", "206"},
}

// PartyKeys is the vocabulary of extra parties (Document.Parties); buyer, seller and shipTo
// have their own fields. Codes are X12 N101 and EDIFACT NAD 3035.
var PartyKeys = []Code{
	{"shipFrom", "SF", "SF"},
	{"billTo", "BT", "IV"},
	{"carrier", "CA", "CA"},
	{"manufacturer", "MF", "MF"},
	{"payer", "PR", "PR"},
	{"payee", "PE", "PE"},
}

// ReferenceKeys is the vocabulary of document references (Document.References, Line.RefType).
// Other keys are accepted but not written to X12 or EDIFACT. Codes are X12 REF01 and EDIFACT RFF 1153.
var ReferenceKeys = []Code{
	{"po", "PO", "ON"},
	{"asn", "", "AAK"},
	{"invoice", "IV", "IV"},
	{"contract", "CT", "CT"},
	{"bol", "BM", "BM"},
	{"deliveryNote", "", "DQ"},
	{"shipment", "SI", "SI"},
	{"other", "ZZ", "ZZZ"},
}

// AmountKeys is the vocabulary of header amounts (Document.Amounts). EDIFACT MOA 5025 qualifiers.
var AmountKeys = []Code{
	{"total", "", "86"},
	{"payment", "", "9"},
	{"tax", "", "176"},
}

// StatusKeys is the vocabulary of canonical status codes with the EDIFACT BGM message
// function (1225) used when the status is carried in the BGM segment. Statuses without a
// code travel as free text (FTX+ZZZ).
var StatusKeys = []Code{
	{"cancelled", "", "1"},
	{"changed", "", "4"},
	{"replacement", "", "5"},
	{"confirmed", "", "6"},
	{"accepted", "", "29"},
	{"rejected", "", "27"},
}

func codeByKey(set []Code, key string) (Code, bool) {
	for _, c := range set {
		if c.Key == key {
			return c, true
		}
	}
	return Code{}, false
}

func hasKey(keys []string, k string) bool {
	for _, x := range keys {
		if x == k {
			return true
		}
	}
	return false
}

func codeKeys(set []Code) []string {
	out := make([]string, len(set))
	for i, c := range set {
		out[i] = c.Key
	}
	return out
}

// --- validation ---

func partyPresent(p Party) bool { return p.Name != "" || p.GLN != "" }

// fieldKnown reports whether a Required entry is part of the field vocabulary.
func fieldKnown(f string) bool {
	switch f {
	case "number", "lines", "lines.gtin", "lines.ref", "lines.refType", "lines.status", "lines.reason", "lines.lot", "lines.unitPrice",
		"lines.amount", "lines.description", "buyer", "seller", "shipTo", "status", "note":
		return true
	}
	for prefix, set := range map[string][]Code{"dates.": DateKeys, "lines.dates.": DateKeys, "parties.": PartyKeys, "amounts.": AmountKeys, "references.": ReferenceKeys} {
		if k, ok := strings.CutPrefix(f, prefix); ok {
			_, found := codeByKey(set, k)
			return found
		}
	}
	return false
}

// fieldPresent evaluates one Required entry against the document and returns the failing line (0: header).
func fieldPresent(d *Document, f string) (ok bool, line int) {
	each := func(pred func(l Line) bool) (bool, int) {
		for i, l := range d.Lines {
			if !pred(l) {
				return false, i + 1
			}
		}
		return true, 0
	}
	switch f {
	case "number":
		return d.Number != "", 0
	case "lines":
		return len(d.Lines) > 0, 0
	case "lines.gtin":
		return each(func(l Line) bool { return l.GTIN != "" })
	case "lines.ref":
		return each(func(l Line) bool { return l.Ref != "" })
	case "lines.refType":
		return each(func(l Line) bool { return l.RefType != "" })
	case "lines.status":
		return each(func(l Line) bool { return l.Status != "" })
	case "lines.reason":
		return each(func(l Line) bool { return l.Reason != "" })
	case "lines.lot":
		return each(func(l Line) bool { return l.Lot != "" })
	case "lines.unitPrice":
		return each(func(l Line) bool { return l.UnitPrice != 0 })
	case "lines.amount":
		return each(func(l Line) bool { return l.Amount != 0 })
	case "lines.description":
		return each(func(l Line) bool { return l.Description != "" })
	case "buyer":
		return partyPresent(d.Buyer), 0
	case "seller":
		return partyPresent(d.Seller), 0
	case "shipTo":
		return partyPresent(d.ShipTo), 0
	case "status":
		return d.Status != "", 0
	case "note":
		return d.Note != "", 0
	}
	switch {
	case strings.HasPrefix(f, "lines.dates."):
		k := strings.TrimPrefix(f, "lines.dates.")
		return each(func(l Line) bool { return l.Dates[k] != "" })
	case strings.HasPrefix(f, "dates."):
		return d.Dates[strings.TrimPrefix(f, "dates.")] != "", 0
	case strings.HasPrefix(f, "parties."):
		return partyPresent(d.Parties[strings.TrimPrefix(f, "parties.")]), 0
	case strings.HasPrefix(f, "amounts."):
		_, has := d.Amounts[strings.TrimPrefix(f, "amounts.")]
		return has, 0
	case strings.HasPrefix(f, "references."):
		return d.References[strings.TrimPrefix(f, "references.")] != "", 0
	}
	return false, 0
}

var ruleDescriptions = map[string]string{
	"qty_positive":   "every item line quantity is greater than zero",
	"qty_nonneg":     "no item line quantity is negative",
	"price_nonneg":   "no unit price is negative",
	"currency_code":  "currency is a three-letter upper-case code",
	"gtin_digits":    "every GTIN is 8, 12, 13 or 14 digits",
	"date_not_after": "the first named date is not after the second",
	"one_of_refs":    "at least one of the listed references is present",
	"amount_sum":     "the header amount equals the sum of the line amounts",
	"reftype_known":  "every line refType is a known reference key",
	"x12_set_refs":   "every line refType is a three-digit X12 transaction set number",
}

// RuleKnown reports whether a rule ID is implemented.
func RuleKnown(id string) bool { _, ok := ruleDescriptions[id]; return ok }

func rule(id string, args ...string) Rule {
	return Rule{ID: id, Args: args, Description: ruleDescriptions[id]}
}

func parseDay(s string) (time.Time, error) { return time.Parse("2006-01-02", s) }

func (r Rule) check(d *Document) error {
	switch r.ID {
	case "qty_positive":
		for i, l := range d.Lines {
			if l.Quantity <= 0 {
				return fmt.Errorf("line %d: quantity must be greater than zero", i+1)
			}
		}
	case "qty_nonneg":
		for i, l := range d.Lines {
			if l.Quantity < 0 {
				return fmt.Errorf("line %d: quantity must not be negative", i+1)
			}
		}
	case "price_nonneg":
		for i, l := range d.Lines {
			if l.UnitPrice < 0 {
				return fmt.Errorf("line %d: unit price must not be negative", i+1)
			}
		}
	case "currency_code":
		if len(d.Currency) != 3 || strings.ToUpper(d.Currency) != d.Currency {
			return fmt.Errorf("currency %q must be a three-letter upper-case code", d.Currency)
		}
	case "gtin_digits":
		for i, l := range d.Lines {
			if l.GTIN == "" {
				continue
			}
			if n := len(l.GTIN); !allDigits(l.GTIN) || (n != 8 && n != 12 && n != 13 && n != 14) {
				return fmt.Errorf("line %d: gtin %q must be 8, 12, 13 or 14 digits", i+1, l.GTIN)
			}
		}
	case "date_not_after":
		if len(r.Args) == 2 {
			a, b := d.Dates[r.Args[0]], d.Dates[r.Args[1]]
			if a != "" && b != "" && a > b { // YYYY-MM-DD compares chronologically
				return fmt.Errorf("dates.%s must not be after dates.%s", r.Args[0], r.Args[1])
			}
		}
	case "one_of_refs":
		for _, k := range r.Args {
			if d.References[k] != "" {
				return nil
			}
		}
		return fmt.Errorf("one of the references %s is required", strings.Join(r.Args, ", "))
	case "amount_sum":
		if len(r.Args) == 1 {
			want, has := d.Amounts[r.Args[0]]
			if !has {
				return nil
			}
			sum := 0.0
			for _, l := range d.Lines {
				sum += l.Amount
			}
			if diff := sum - want; diff > 0.005 || diff < -0.005 {
				return fmt.Errorf("amounts.%s (%s) must equal the sum of the line amounts (%s)", r.Args[0], num(want), num(sum))
			}
		}
	case "reftype_known":
		for i, l := range d.Lines {
			if _, ok := codeByKey(ReferenceKeys, l.RefType); l.RefType != "" && !ok {
				return fmt.Errorf("line %d: refType %q is not a known reference key", i+1, l.RefType)
			}
		}
	case "x12_set_refs":
		for i, l := range d.Lines {
			if len(l.RefType) != 3 || !allDigits(l.RefType) {
				return fmt.Errorf("line %d: refType must be a three-digit X12 transaction set number", i+1)
			}
		}
	default:
		return fmt.Errorf("unknown rule %q", r.ID)
	}
	return nil
}

func allDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

// Validate checks the document against its registry type, applying defaults (date, currency,
// line numbers, unit of measure). The first problem found is returned.
func (d *Document) Validate() error {
	t, ok := Lookup(d.Type)
	if !ok {
		return fmt.Errorf("unknown document type %q", d.Type)
	}
	if d.Number == "" {
		return fmt.Errorf("number is required")
	}
	if d.Date.IsZero() {
		d.Date = time.Now().UTC()
	}
	if d.Currency == "" {
		d.Currency = "USD"
	}
	for i := range d.Lines {
		if d.Lines[i].Line == 0 {
			d.Lines[i].Line = i + 1
		}
		if d.Lines[i].UOM == "" && t.Lines == LinesItem {
			d.Lines[i].UOM = "EA"
		}
	}
	for _, f := range t.Required {
		if ok, line := fieldPresent(d, f); !ok {
			switch {
			case f == "lines":
				return fmt.Errorf("at least one line is required")
			case line > 0:
				return fmt.Errorf("line %d: %s is required", line, strings.TrimPrefix(f, "lines."))
			default:
				return fmt.Errorf("%s is required", f)
			}
		}
	}
	if err := t.checkVocabulary(d); err != nil {
		return err
	}
	if hasKey(t.Amounts, "total") {
		if _, has := d.Amounts["total"]; !has && d.Total() != 0 {
			if d.Amounts == nil {
				d.Amounts = map[string]float64{}
			}
			d.Amounts["total"] = d.Total()
		}
	}
	for _, r := range t.Rules {
		if err := r.check(d); err != nil {
			return err
		}
	}
	return nil
}

// checkVocabulary rejects statuses, dates, parties and amounts the type does not declare.
func (t Type) checkVocabulary(d *Document) error {
	if d.Status != "" && !hasKey(t.Statuses, d.Status) {
		return fmt.Errorf("status %q is not allowed for %s%s", d.Status, t.Key, allowed(t.Statuses))
	}
	for i, l := range d.Lines {
		if l.Status != "" && !hasKey(t.LineStatuses, l.Status) {
			return fmt.Errorf("line %d: status %q is not allowed for %s%s", i+1, l.Status, t.Key, allowed(t.LineStatuses))
		}
		for _, k := range sortedKeys(l.Dates) {
			if !hasKey(t.LineDates, k) {
				return fmt.Errorf("line %d: date %q is not allowed for %s%s", i+1, k, t.Key, allowed(t.LineDates))
			}
			if _, err := parseDay(l.Dates[k]); err != nil {
				return fmt.Errorf("line %d: date %s %q is not YYYY-MM-DD", i+1, k, l.Dates[k])
			}
		}
	}
	for _, k := range sortedKeys(d.Dates) {
		if !hasKey(t.Dates, k) {
			return fmt.Errorf("date %q is not allowed for %s%s", k, t.Key, allowed(t.Dates))
		}
		if _, err := parseDay(d.Dates[k]); err != nil {
			return fmt.Errorf("date %s %q is not YYYY-MM-DD", k, d.Dates[k])
		}
	}
	for _, k := range sortedKeys(d.Parties) {
		if !hasKey(t.Parties, k) {
			return fmt.Errorf("party %q is not allowed for %s%s", k, t.Key, allowed(t.Parties))
		}
	}
	for _, k := range sortedKeys(d.Amounts) {
		if !hasKey(t.Amounts, k) {
			return fmt.Errorf("amount %q is not allowed for %s%s", k, t.Key, allowed(t.Amounts))
		}
	}
	return nil
}

func allowed(keys []string) string {
	if len(keys) == 0 {
		return " (none are defined)"
	}
	return " (allowed: " + strings.Join(keys, ", ") + ")"
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// --- mapping targets ---

var baseHeaderTargets = []string{"number", "date", "currency", "buyer.name", "buyer.gln", "seller.name", "seller.gln", "shipTo.name", "shipTo.gln", "ssccs",
	"references.po", "references.asn", "references.invoice", "references.contract"}

var itemLineTargets = []string{"lines.gtin", "lines.description", "lines.quantity", "lines.uom", "lines.unitPrice", "lines.lot", "lines.expiry", "lines.serials", "lines.reason"}

var referenceLineTargets = []string{"lines.ref", "lines.refType", "lines.status", "lines.amount", "lines.reason"}

// Targets lists the canonical field paths a mapping can fill for the type: header fields,
// then line fields ("lines." prefix). references.<key> is open-ended beyond the listed keys.
func (t Type) Targets() []string {
	out := append([]string{}, baseHeaderTargets...)
	if len(t.Statuses) > 0 {
		out = append(out, "status")
	}
	out = append(out, "note")
	for _, k := range t.Dates {
		out = append(out, "dates."+k)
	}
	for _, k := range t.Parties {
		out = append(out, "parties."+k+".name", "parties."+k+".gln")
	}
	for _, k := range t.Amounts {
		out = append(out, "amounts."+k)
	}
	lines := t.LineFields
	if len(lines) == 0 {
		if t.Lines == LinesReference {
			lines = referenceLineTargets
		} else {
			lines = itemLineTargets
		}
	}
	out = append(out, lines...)
	if len(t.LineStatuses) > 0 && !hasKey(lines, "lines.status") {
		out = append(out, "lines.status")
	}
	for _, k := range t.LineDates {
		out = append(out, "lines.dates."+k)
	}
	return out
}
