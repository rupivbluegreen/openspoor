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
}

// Document is the canonical B2B document.
type Document struct {
	Type       string            `json:"type"` // order | asn | invoice | inventory | return | chargeback
	Number     string            `json:"number"`
	Date       time.Time         `json:"date"`
	Buyer      Party             `json:"buyer"`
	Seller     Party             `json:"seller"`
	ShipTo     Party             `json:"shipTo,omitempty"`
	Currency   string            `json:"currency,omitempty"`
	References map[string]string `json:"references,omitempty"` // po, asn, invoice, contract
	Lines      []Line            `json:"lines"`
	SSCCs      []string          `json:"ssccs,omitempty"`
}

// Total sums line amounts.
func (d Document) Total() float64 {
	t := 0.0
	for _, l := range d.Lines {
		t += l.Quantity * l.UnitPrice
	}
	return t
}

// Validate checks required fields.
func (d *Document) Validate() error {
	if _, ok := x12Sets[d.Type]; !ok {
		return fmt.Errorf("unknown document type %q", d.Type)
	}
	if d.Number == "" {
		return errors.New("number is required")
	}
	if len(d.Lines) == 0 && d.Type != "inventory" {
		return errors.New("at least one line is required")
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
		if d.Lines[i].UOM == "" {
			d.Lines[i].UOM = "EA"
		}
		if d.Lines[i].GTIN == "" {
			return fmt.Errorf("line %d: gtin is required", i+1)
		}
	}
	return nil
}

func num(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// --- X12 ---

var x12Sets = map[string]string{"order": "850", "asn": "856", "invoice": "810", "inventory": "846", "return": "180", "chargeback": "844"}

// X12Set returns the transaction set number for a document type.
func X12Set(docType string) string { return x12Sets[docType] }

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

// X12 renders an X12 interchange (version 004010) with one transaction set.
func X12(d Document, env Envelope) string {
	if env.Control == 0 {
		env.Control = 1
	}
	if env.SenderID == "" {
		env.SenderID = d.Seller.GLN
	}
	if env.ReceiverID == "" {
		env.ReceiverID = d.Buyer.GLN
	}
	if d.Type == "order" || d.Type == "return" {
		env.SenderID, env.ReceiverID = env.ReceiverID, env.SenderID
		if env.SenderID == "" {
			env.SenderID = d.Buyer.GLN
		}
	}
	set := x12Sets[d.Type]
	ctrl := fmt.Sprintf("%09d", env.Control)
	date, tm := d.Date.UTC().Format("060102"), d.Date.UTC().Format("1504")
	var segs []string
	seg := func(els ...string) { segs = append(segs, strings.Join(els, "*")) }
	isa := []string{"ISA", "00", pad("", 10), "00", pad("", 10), "ZZ", pad(env.SenderID, 15), "ZZ", pad(env.ReceiverID, 15), date, tm, "U", "00401", ctrl, "0", "P", ">"}
	gsCode := map[string]string{"850": "PO", "856": "SH", "810": "IN", "846": "IB", "180": "AN", "844": "CA"}[set]
	header := []string{strings.Join(isa, "*"), strings.Join([]string{"GS", gsCode, env.SenderID, env.ReceiverID, d.Date.UTC().Format("20060102"), tm, strconv.Itoa(env.Control), "X", "004010"}, "*")}
	seg("ST", set, "0001")
	ymd := d.Date.UTC().Format("20060102")
	party := func(code string, p Party) {
		if p.Name != "" || p.GLN != "" {
			seg("N1", code, clean(p.Name), "UL", p.GLN)
		}
	}
	switch set {
	case "850":
		seg("BEG", "00", "SA", clean(d.Number), "", ymd)
		seg("CUR", "BY", d.Currency)
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

// ParseX12 reads an X12 850 (purchase order) into a canonical document.
func ParseX12(s string) (Document, error) {
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

// --- EDIFACT ---

var edifactTypes = map[string]string{"order": "ORDERS", "asn": "DESADV", "invoice": "INVOIC", "inventory": "INVRPT", "return": "RETANN", "chargeback": "COMDIS"}

// EDIFACTType returns the message type for a document type.
func EDIFACTType(docType string) string { return edifactTypes[docType] }

// EDIFACT renders a UN/EDIFACT D.96A interchange with one message.
func EDIFACT(d Document, env Envelope) string {
	if env.Control == 0 {
		env.Control = 1
	}
	from, to := d.Seller.GLN, d.Buyer.GLN
	if d.Type == "order" || d.Type == "return" {
		from, to = to, from
	}
	if env.SenderID != "" {
		from = env.SenderID
	}
	if env.ReceiverID != "" {
		to = env.ReceiverID
	}
	msg := edifactTypes[d.Type]
	ref := strconv.Itoa(env.Control)
	var segs []string
	seg := func(s string) { segs = append(segs, s) }
	seg("UNH+1+" + msg + ":D:96A:UN:EAN008")
	bgm := map[string]string{"ORDERS": "220", "DESADV": "351", "INVOIC": "380", "INVRPT": "35", "RETANN": "7", "COMDIS": "385"}[msg]
	seg("BGM+" + bgm + "+" + clean(d.Number) + "+9")
	seg("DTM+137:" + d.Date.UTC().Format("20060102") + ":102")
	for k, v := range d.References {
		code := map[string]string{"po": "ON", "asn": "AAK", "invoice": "IV", "contract": "CT"}[k]
		if code != "" {
			seg("RFF+" + code + ":" + clean(v))
		}
	}
	nad := func(q string, p Party) {
		if p.GLN != "" || p.Name != "" {
			seg("NAD+" + q + "+" + p.GLN + "::9++" + clean(p.Name))
		}
	}
	nad("BY", d.Buyer)
	nad("SU", d.Seller)
	nad("DP", d.ShipTo)
	if msg == "ORDERS" || msg == "INVOIC" {
		seg("CUX+2:" + d.Currency + ":9")
	}
	for _, s := range d.SSCCs {
		seg("CPS+" + s)
		seg("GIN+BJ+" + s)
	}
	for _, l := range d.Lines {
		seg("LIN+" + strconv.Itoa(l.Line) + "++" + l.GTIN + ":SRV")
		if l.Description != "" {
			seg("IMD+F++:::" + clean(l.Description))
		}
		qtyCode := map[string]string{"ORDERS": "21", "DESADV": "12", "INVOIC": "47", "INVRPT": "145", "RETANN": "61", "COMDIS": "47"}[msg]
		seg("QTY+" + qtyCode + ":" + num(l.Quantity) + ":" + l.UOM)
		if l.Lot != "" {
			seg("PIA+5+" + clean(l.Lot) + ":NB")
		}
		if l.Expiry != "" {
			seg("DTM+361:" + strings.ReplaceAll(l.Expiry, "-", "") + ":102")
		}
		if l.UnitPrice != 0 {
			seg("PRI+AAA:" + num(l.UnitPrice))
		}
		for _, s := range l.Serials {
			seg("GIN+BN+" + clean(s))
		}
		if l.Reason != "" {
			seg("FTX+AAI+++" + clean(l.Reason))
		}
	}
	seg("UNS+S")
	if msg == "INVOIC" || msg == "COMDIS" {
		seg("MOA+86:" + num(d.Total()))
	}
	seg("CNT+2:" + strconv.Itoa(len(d.Lines)))
	segs = append(segs, "UNT+"+strconv.Itoa(len(segs)+1)+"+1")
	head := []string{"UNA:+.? ", "UNB+UNOC:3+" + from + ":14+" + to + ":14+" + d.Date.UTC().Format("060102:1504") + "+" + ref}
	return strings.Join(head, "'\n") + "'\n" + strings.Join(segs, "'\n") + "'\nUNZ+1+" + ref + "'\n"
}

// ParseEDIFACT reads an EDIFACT ORDERS message into a canonical document.
func ParseEDIFACT(s string) (Document, error) {
	d := Document{Type: "order", References: map[string]string{}}
	s = strings.TrimPrefix(strings.TrimSpace(s), "UNA:+.? ")
	segs := splitSegments(s, '\'')
	seen := false
	for _, sg := range segs {
		e := strings.Split(sg, "+")
		comp := func(i, j int) string {
			if i < len(e) {
				c := strings.Split(e[i], ":")
				if j < len(c) {
					return c[j]
				}
			}
			return ""
		}
		switch e[0] {
		case "UNH":
			if comp(2, 0) != "ORDERS" {
				return d, fmt.Errorf("only EDIFACT ORDERS import is supported, got %s", comp(2, 0))
			}
			seen = true
		case "BGM":
			d.Number = comp(2, 0)
		case "DTM":
			if comp(1, 0) == "137" {
				if t, err := time.Parse("20060102", comp(1, 1)); err == nil {
					d.Date = t
				}
			}
		case "RFF":
			if comp(1, 0) == "ON" {
				d.References["po"] = comp(1, 1)
			}
		case "NAD":
			p := Party{GLN: comp(2, 0), Name: comp(4, 0)}
			switch comp(1, 0) {
			case "BY":
				d.Buyer = p
			case "SU":
				d.Seller = p
			case "DP":
				d.ShipTo = p
			}
		case "CUX":
			d.Currency = comp(1, 1)
		case "LIN":
			ln, _ := strconv.Atoi(comp(1, 0))
			d.Lines = append(d.Lines, Line{Line: ln, GTIN: comp(3, 0)})
		case "IMD":
			if n := len(d.Lines); n > 0 {
				d.Lines[n-1].Description = comp(3, 3)
			}
		case "QTY":
			if n := len(d.Lines); n > 0 {
				d.Lines[n-1].Quantity, _ = strconv.ParseFloat(comp(1, 1), 64)
				d.Lines[n-1].UOM = comp(1, 2)
			}
		case "PRI":
			if n := len(d.Lines); n > 0 {
				d.Lines[n-1].UnitPrice, _ = strconv.ParseFloat(comp(1, 1), 64)
			}
		}
	}
	if !seen {
		return d, errors.New("no UNH segment found")
	}
	return d, d.Validate()
}

// --- IDoc (XML) ---

var idocTypes = map[string][2]string{"order": {"ORDERS05", "ORDERS"}, "asn": {"DELVRY07", "DESADV"}, "invoice": {"INVOIC02", "INVOIC"},
	"inventory": {"WMMBID02", "WMMBXY"}, "return": {"ORDERS05", "ORDRSP"}, "chargeback": {"INVOIC02", "INVOIC"}}

// IDocType returns the basic type and message type for a document type.
func IDocType(docType string) (string, string) { t := idocTypes[docType]; return t[0], t[1] }

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
