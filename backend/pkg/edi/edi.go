// Package edi maps Open Spoor's canonical B2B documents to and from ANSI ASC X12,
// UN/EDIFACT and IDoc (XML) representations. It covers the healthcare and food
// supply-chain transaction set: orders, despatch advice (ASN), invoices, inventory
// reports, returns and chargebacks. Only public segment structures are used.
package edi

import (
	"encoding/xml"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Party is a trading party.
type Party struct {
	Name string `json:"name"`
	GLN  string `json:"gln,omitempty"`
}

// Line is a document line.
type Line struct {
	Line        int      `json:"line"`
	GTIN        string   `json:"gtin"`
	Description string   `json:"description,omitempty"`
	Quantity    float64  `json:"quantity"`
	UOM         string   `json:"uom,omitempty"`
	UnitPrice   float64  `json:"unitPrice,omitempty"`
	Lot         string   `json:"lot,omitempty"`
	Expiry      string   `json:"expiry,omitempty"` // YYYY-MM-DD
	Serials     []string `json:"serials,omitempty"`
	Reason      string   `json:"reason,omitempty"` // returns and chargebacks
	// Generic extensions used by the transaction types in the registry.
	Status  string            `json:"status,omitempty"`  // canonical line status (see Type.LineStatuses)
	Ref     string            `json:"ref,omitempty"`     // reference line: the referenced document number
	RefType string            `json:"refType,omitempty"` // reference line: key from ReferenceKeys (or an X12 set number for acknowledgements)
	Amount  float64           `json:"amount,omitempty"`  // reference line: monetary amount
	Dates   map[string]string `json:"dates,omitempty"`   // line dates, YYYY-MM-DD (see DateKeys)
}

// Document is the canonical B2B document.
type Document struct {
	Type       string            `json:"type"` // a key of the registry (Types), for example order, asn, forecast
	Number     string            `json:"number"`
	Date       time.Time         `json:"date"`
	Buyer      Party             `json:"buyer"`
	Seller     Party             `json:"seller"`
	ShipTo     Party             `json:"shipTo,omitempty"`
	Currency   string            `json:"currency,omitempty"`
	References map[string]string `json:"references,omitempty"` // po, asn, invoice, contract
	Lines      []Line            `json:"lines"`
	SSCCs      []string          `json:"ssccs,omitempty"`
	// Generic extensions used by the transaction types in the registry. All are optional.
	Status  string             `json:"status,omitempty"`  // canonical status (see Type.Statuses)
	Dates   map[string]string  `json:"dates,omitempty"`   // named dates, YYYY-MM-DD (see DateKeys)
	Parties map[string]Party   `json:"parties,omitempty"` // further parties (see PartyKeys)
	Amounts map[string]float64 `json:"amounts,omitempty"` // header amounts (see AmountKeys)
	Note    string             `json:"note,omitempty"`    // free text
}

// Total sums line amounts.
func (d Document) Total() float64 {
	t := 0.0
	for _, l := range d.Lines {
		t += l.Quantity*l.UnitPrice + l.Amount
	}
	return t
}

func num(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// --- X12 ---

// x12Sets, edifactTypes and idocTypes are the type-to-identifier maps of the six hand-written
// mappers (order, asn, invoice, inventory, return, chargeback); the registry fills them.
var (
	x12Sets      = map[string]string{}
	edifactTypes = map[string]string{}
	idocTypes    = map[string][2]string{}
)

// X12Set returns the X12 transaction set number for a document type ("" when none is attributed).
func X12Set(docType string) string { return byKeyX12(docType) }

// gsCodes maps the hand-written sets to their functional identifier codes (GS01).
var gsCodes = map[string]string{"850": "PO", "856": "SH", "810": "IN", "846": "IB", "180": "AN", "844": "CA"}

// gsCode returns the functional identifier code for a transaction set ("" when unknown).
func gsCode(set string) string {
	if c, ok := gsCodes[set]; ok {
		return c
	}
	if t, ok := LookupX12(set); ok && t.x12 != nil {
		return t.x12.GS
	}
	return ""
}

// buyerSends reports whether the buyer is the interchange sender. The hand-written mappers
// keep their original rule (order and return); registry types follow their flow.
func (t Type) buyerSends() bool {
	if t.legacy {
		return t.Key == "order" || t.Key == "return"
	}
	return t.Flow == FlowBuyerToSeller
}

// Envelope carries interchange identifiers.
type Envelope struct {
	SenderID   string
	ReceiverID string
	Control    int
}

func pad(s string, n int) string {
	if len(s) >= n {
		return s[:n]
	}
	return s + strings.Repeat(" ", n-len(s))
}

func clean(s string) string {
	return strings.NewReplacer("*", " ", "~", " ", ":", " ", "'", " ", "+", " ", "?", " ").Replace(s)
}

// X12 renders an X12 interchange (version 004010) with one transaction set. It returns ""
// when the document's type has no X12 writer (see Type.CanWriteX12).
func X12(d Document, env Envelope) string {
	t, ok := Lookup(d.Type)
	if !ok || !t.CanWriteX12() {
		return ""
	}
	if env.Control == 0 {
		env.Control = 1
	}
	if env.SenderID == "" {
		env.SenderID = d.Seller.GLN
	}
	if env.ReceiverID == "" {
		env.ReceiverID = d.Buyer.GLN
	}
	if t.buyerSends() {
		env.SenderID, env.ReceiverID = env.ReceiverID, env.SenderID
		if env.SenderID == "" {
			env.SenderID = d.Buyer.GLN
		}
	}
	set := t.X12
	ctrl := fmt.Sprintf("%09d", env.Control)
	date, tm := d.Date.UTC().Format("060102"), d.Date.UTC().Format("1504")
	var segs []string
	seg := func(els ...string) { segs = append(segs, strings.Join(els, "*")) }
	isa := []string{"ISA", "00", pad("", 10), "00", pad("", 10), "ZZ", pad(env.SenderID, 15), "ZZ", pad(env.ReceiverID, 15), date, tm, "U", "00401", ctrl, "0", "P", ">"}
	header := []string{strings.Join(isa, "*"), strings.Join([]string{"GS", gsCode(set), env.SenderID, env.ReceiverID, d.Date.UTC().Format("20060102"), tm, strconv.Itoa(env.Control), "X", "004010"}, "*")}
	seg("ST", set, "0001")
	ymd := d.Date.UTC().Format("20060102")
	party := func(code string, p Party) {
		if p.Name != "" || p.GLN != "" {
			seg("N1", code, clean(p.Name), "UL", p.GLN)
		}
	}
	if t.x12 != nil {
		segs = append(segs, t.x12Body(d)...)
		set = ""
	}
	switch set {
	case "850":
		seg("BEG", "00", "SA", clean(d.Number), "", ymd)
		seg("CUR", "BY", d.Currency)
		for _, k := range dateOrder(d.Dates) {
			if c := dateX12(k); c != "" {
				seg("DTM", c, strings.ReplaceAll(d.Dates[k], "-", ""))
			}
		}
		party("BY", d.Buyer)
		party("SE", d.Seller)
		party("ST", d.ShipTo)
		for _, l := range d.Lines {
			seg("PO1", strconv.Itoa(l.Line), num(l.Quantity), l.UOM, num(l.UnitPrice), "", "UK", l.GTIN)
			if l.Description != "" {
				seg("PID", "F", "", "", "", clean(l.Description))
			}
		}
		seg("CTT", strconv.Itoa(len(d.Lines)))
	case "856":
		seg("BSN", "00", clean(d.Number), ymd, tm)
		seg("HL", "1", "", "S")
		seg("DTM", "011", ymd)
		party("SF", d.Seller)
		party("ST", d.ShipTo)
		hl := 2
		seg("HL", strconv.Itoa(hl), "1", "O")
		if po := d.References["po"]; po != "" {
			seg("PRF", clean(po))
		}
		order := hl
		for _, s := range d.SSCCs {
			hl++
			seg("HL", strconv.Itoa(hl), strconv.Itoa(order), "P")
			seg("MAN", "GM", s)
		}
		for _, l := range d.Lines {
			hl++
			seg("HL", strconv.Itoa(hl), strconv.Itoa(order), "I")
			seg("LIN", strconv.Itoa(l.Line), "UK", l.GTIN, "LT", clean(l.Lot))
			seg("SN1", "", num(l.Quantity), l.UOM)
			if l.Expiry != "" {
				seg("DTM", "036", strings.ReplaceAll(l.Expiry, "-", ""))
			}
			for _, s := range l.Serials {
				seg("REF", "SE", clean(s))
			}
		}
		seg("CTT", strconv.Itoa(hl))
	case "810":
		seg("BIG", ymd, clean(d.Number), "", clean(d.References["po"]))
		seg("CUR", "SE", d.Currency)
		party("BT", d.Buyer)
		party("RE", d.Seller)
		for _, l := range d.Lines {
			seg("IT1", strconv.Itoa(l.Line), num(l.Quantity), l.UOM, num(l.UnitPrice), "", "UK", l.GTIN)
		}
		seg("TDS", strconv.FormatInt(int64(d.Total()*100+0.5), 10))
		seg("CTT", strconv.Itoa(len(d.Lines)))
	case "846":
		seg("BIA", "00", "DD", clean(d.Number), ymd)
		party("SE", d.Seller)
		for _, l := range d.Lines {
			seg("LIN", strconv.Itoa(l.Line), "UK", l.GTIN, "LT", clean(l.Lot))
			seg("QTY", "33", num(l.Quantity), l.UOM)
		}
		seg("CTT", strconv.Itoa(len(d.Lines)))
	case "180":
		seg("BGN", "00", clean(d.Number), ymd)
		party("BY", d.Buyer)
		party("SE", d.Seller)
		for _, l := range d.Lines {
			seg("BLI", "UK", l.GTIN, num(l.Quantity), "", l.UOM)
			seg("RDR", "", "", clean(l.Reason))
			if l.Lot != "" {
				seg("REF", "LT", clean(l.Lot))
			}
		}
	case "844":
		seg("BCP", "00", clean(d.Number), ymd, "", clean(d.References["contract"]))
		party("BY", d.Buyer)
		party("SE", d.Seller)
		for _, l := range d.Lines {
			seg("CDD", clean(l.Reason), "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", num(l.Quantity*l.UnitPrice))
			seg("LIN", strconv.Itoa(l.Line), "UK", l.GTIN)
			seg("QTY", "39", num(l.Quantity), l.UOM)
		}
	}
	segs = append(segs, strings.Join([]string{"SE", strconv.Itoa(len(segs) + 1), "0001"}, "*"))
	trailer := []string{"GE*1*" + strconv.Itoa(env.Control), "IEA*1*" + ctrl}
	return strings.Join(append(append(header, segs...), trailer...), "~\n") + "~\n"
}

// ParseX12 reads an X12 interchange with one transaction set into a canonical document:
// the 850 purchase order, and every registry type that has a segment template.
func ParseX12(s string) (Document, error) {
	if set, sep := firstSet(s); set != "" && set != "850" {
		t, ok := LookupX12(set)
		if !ok || t.x12 == nil {
			return Document{}, fmt.Errorf("X12 %s import is not supported", set)
		}
		return parseX12Generic(t, splitSegments(s, '~'), sep)
	}
	return parseX12Order(s)
}

// firstSet returns the transaction set number of the first ST segment and the element separator.
func firstSet(s string) (set, sep string) {
	segs := splitSegments(s, '~')
	if len(segs) == 0 || !strings.HasPrefix(segs[0], "ISA") || len(segs[0]) < 4 {
		return "", ""
	}
	sep = string(segs[0][3])
	for _, sg := range segs {
		if e := strings.Split(sg, sep); e[0] == "ST" && len(e) > 1 {
			return strings.TrimSpace(e[1]), sep
		}
	}
	return "", sep
}

func parseX12Order(s string) (Document, error) {
	d := Document{Type: "order", References: map[string]string{}}
	segs := splitSegments(s, '~')
	if len(segs) == 0 || !strings.HasPrefix(segs[0], "ISA") {
		return d, errors.New("not an X12 interchange")
	}
	elSep := string(segs[0][3])
	seenST := false
	for _, sg := range segs {
		e := strings.Split(sg, elSep)
		get := func(i int) string {
			if i < len(e) {
				return strings.TrimSpace(e[i])
			}
			return ""
		}
		switch e[0] {
		case "ST":
			if get(1) != "850" {
				return d, fmt.Errorf("only X12 850 import is supported, got %s", get(1))
			}
			seenST = true
		case "DTM":
			for _, c := range DateKeys {
				if c.X12 == get(1) && get(2) != "" {
					if t, err := time.Parse("20060102", get(2)); err == nil {
						setDate(&d.Dates, c.Key, t.Format("2006-01-02"))
					}
				}
			}
		case "BEG":
			d.Number = get(3)
			if t, err := time.Parse("20060102", get(5)); err == nil {
				d.Date = t
			}
		case "CUR":
			d.Currency = get(2)
		case "N1":
			p := Party{Name: get(2), GLN: get(4)}
			switch get(1) {
			case "BY":
				d.Buyer = p
			case "SE":
				d.Seller = p
			case "ST":
				d.ShipTo = p
			}
		case "PO1":
			q, _ := strconv.ParseFloat(get(2), 64)
			pr, _ := strconv.ParseFloat(get(4), 64)
			ln, _ := strconv.Atoi(get(1))
			l := Line{Line: ln, Quantity: q, UOM: get(3), UnitPrice: pr}
			for i := 6; i+1 < len(e); i += 2 {
				if e[i] == "UK" || e[i] == "EN" || e[i] == "UP" {
					l.GTIN = e[i+1]
				}
			}
			d.Lines = append(d.Lines, l)
		case "PID":
			if n := len(d.Lines); n > 0 {
				d.Lines[n-1].Description = get(5)
			}
		}
	}
	if !seenST {
		return d, errors.New("no transaction set found")
	}
	return d, d.Validate()
}

func splitSegments(s string, term byte) []string {
	var out []string
	for _, p := range strings.Split(s, string(term)) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// --- IDoc (XML) ---

// IDocType returns the basic type and message type for a document type ("" when none).
func IDocType(docType string) (string, string) {
	if t, ok := Lookup(docType); ok {
		return t.IDocBasic, t.IDocMessage
	}
	return "", ""
}

type idocField struct {
	XMLName xml.Name
	Value   string `xml:",chardata"`
}

type idocSegment struct {
	XMLName  xml.Name
	Segment  string        `xml:"SEGMENT,attr"`
	Fields   []idocField   `xml:",any"`
	Children []idocSegment `xml:"-"`
}

// IDoc renders an IDoc XML document (control record EDI_DC40 plus header, partner and item segments).
func IDoc(d Document, env Envelope) (string, error) {
	basic, msg := IDocType(d.Type)
	if basic == "" {
		return "", fmt.Errorf("no IDoc mapping for document type %q", d.Type)
	}
	var b strings.Builder
	w := &b
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>`+"\n<%s>\n <IDOC BEGIN=\"1\">\n", basic)
	f := func(indent, name, v string) {
		if v == "" {
			return
		}
		fmt.Fprintf(w, "%s<%s>", indent, name)
		_ = xml.EscapeText(w, []byte(v))
		fmt.Fprintf(w, "</%s>\n", name)
	}
	fmt.Fprint(w, "  <EDI_DC40 SEGMENT=\"1\">\n")
	f("   ", "TABNAM", "EDI_DC40")
	f("   ", "DOCNUM", fmt.Sprintf("%016d", max(env.Control, 1)))
	f("   ", "DIRECT", "2")
	f("   ", "IDOCTYP", basic)
	f("   ", "MESTYP", msg)
	f("   ", "SNDPRT", "LS")
	f("   ", "SNDPRN", firstNonEmpty(env.SenderID, d.Seller.GLN))
	f("   ", "RCVPRT", "LS")
	f("   ", "RCVPRN", firstNonEmpty(env.ReceiverID, d.Buyer.GLN))
	f("   ", "CREDAT", d.Date.UTC().Format("20060102"))
	f("   ", "CRETIM", d.Date.UTC().Format("150405"))
	fmt.Fprint(w, "  </EDI_DC40>\n")
	fmt.Fprint(w, "  <E1EDK01 SEGMENT=\"1\">\n")
	f("   ", "CURCY", d.Currency)
	f("   ", "BELNR", d.Number)
	fmt.Fprint(w, "  </E1EDK01>\n")
	for _, p := range []struct {
		role string
		p    Party
	}{{"AG", d.Buyer}, {"LF", d.Seller}, {"WE", d.ShipTo}} {
		if p.p.GLN == "" && p.p.Name == "" {
			continue
		}
		fmt.Fprint(w, "  <E1EDKA1 SEGMENT=\"1\">\n")
		f("   ", "PARVW", p.role)
		f("   ", "ILNNR", p.p.GLN)
		f("   ", "NAME1", p.p.Name)
		fmt.Fprint(w, "  </E1EDKA1>\n")
	}
	for k, v := range d.References {
		fmt.Fprint(w, "  <E1EDK02 SEGMENT=\"1\">\n")
		f("   ", "QUALF", map[string]string{"po": "001", "asn": "012", "invoice": "009", "contract": "004"}[k])
		f("   ", "BELNR", v)
		fmt.Fprint(w, "  </E1EDK02>\n")
	}
	for _, l := range d.Lines {
		fmt.Fprint(w, "  <E1EDP01 SEGMENT=\"1\">\n")
		f("   ", "POSEX", fmt.Sprintf("%06d", l.Line*10))
		f("   ", "MENGE", num(l.Quantity))
		f("   ", "MENEE", l.UOM)
		if l.UnitPrice != 0 {
			f("   ", "VPREI", num(l.UnitPrice))
		}
		f("   ", "CHARG", l.Lot)
		fmt.Fprint(w, "   <E1EDP19 SEGMENT=\"1\">\n")
		f("    ", "QUALF", "003")
		f("    ", "IDTNR", l.GTIN)
		f("    ", "KTEXT", l.Description)
		fmt.Fprint(w, "   </E1EDP19>\n")
		fmt.Fprint(w, "  </E1EDP01>\n")
	}
	fmt.Fprintf(w, " </IDOC>\n</%s>\n", basic)
	return b.String(), nil
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

var _ = idocSegment{}
