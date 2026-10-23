package edi

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// A segSpec is one header segment of an X12 template. Each element is a role:
//
//	""             empty element
//	const:X        the literal X
//	number         the document number
//	date           the document date (CCYYMMDD)
//	date:<key>     a named date from Document.Dates
//	status         the document status, translated through the template's Status table
//	ref:<key>      a document reference from Document.References
//	amount:<key>   a header amount from Document.Amounts
//	gsid           the functional identifier of the acknowledged transaction sets (997)
type segSpec struct {
	ID    string
	Elems []string
}

// x12Template drives the generic X12 writer and reader for one transaction set: header
// segments, then (optionally) CUR, REF, DTM and N1 segments for the common header data, then
// the lines in the named profile, then CTT.
type x12Template struct {
	GS         string
	Header     []segSpec
	Lines      string // key of x12Profiles
	Currency   bool   // emit CUR*BY
	CTT        bool   // emit CTT with the line count
	Status     map[string]string
	LineStatus map[string]string
	// DetailID and DetailRoles describe the quantity segment of the "lin" line profile: the
	// segment ID and one role per element (line, qty, uom, price or const:X).
	DetailID    string
	DetailRoles []string
	// AckSeg is the per-set response segment of the "ak2" profile (AK5 for 997, IK5 for 999).
	AckSeg string
}

// x12Profile writes and reads the line segments of one line layout.
type x12Profile struct {
	start   string
	write   func(emit func(id string, els ...string), tm *x12Template, l Line)
	read    func(l *Line, id string, get func(int) string, tm *x12Template)
	trailer func(emit func(id string, els ...string), d Document)
}

func fnum(s string) float64 { f, _ := strconv.ParseFloat(s, 64); return f }
func inum(s string) int     { n, _ := strconv.Atoi(s); return n }

func reverse(m map[string]string, code string) string {
	for k, v := range m {
		if v == code {
			return k
		}
	}
	return ""
}

func ymdOf(day string) string { return strings.ReplaceAll(day, "-", "") }

func dayOf(ymd string) string {
	if t, err := time.Parse("20060102", ymd); err == nil {
		return t.Format("2006-01-02")
	}
	return ""
}

func setDate(m *map[string]string, k, v string) {
	if v == "" {
		return
	}
	if *m == nil {
		*m = map[string]string{}
	}
	(*m)[k] = v
}

func dateX12(key string) string { c, _ := codeByKey(DateKeys, key); return c.X12 }

// dateOrder returns the keys of m in vocabulary order, then any others sorted (those are
// rejected by validation and never written).
func dateOrder(m map[string]string) []string {
	var out []string
	for _, c := range DateKeys {
		if m[c.Key] != "" {
			out = append(out, c.Key)
		}
	}
	return out
}

func byKeyX12(key string) string {
	if t, ok := Lookup(key); ok {
		return t.X12
	}
	return ""
}

func refQual(refType string) string {
	if c, ok := codeByKey(ReferenceKeys, refType); ok && c.X12 != "" {
		return c.X12
	}
	return "ZZ"
}

func refKeyOf(qual string) string {
	for _, c := range ReferenceKeys {
		if c.X12 != "" && c.X12 == qual {
			return c.Key
		}
	}
	return ""
}

func itemID(from int, get func(int) string) string {
	for i := from; i+1 < 12; i += 2 {
		if q := get(i); q == "UK" || q == "EN" || q == "UP" {
			return get(i + 1)
		}
	}
	return ""
}

func (tm *x12Template) ackSeg() string {
	if tm.AckSeg != "" {
		return tm.AckSeg
	}
	return "AK5"
}

// detailEls renders the elements of a "lin" detail segment from its roles.
func detailEls(roles []string, l Line) []string {
	els := make([]string, len(roles))
	for i, r := range roles {
		switch kind, arg, _ := strings.Cut(r, ":"); kind {
		case "const":
			els[i] = arg
		case "line":
			els[i] = strconv.Itoa(l.Line)
		case "qty":
			els[i] = num(l.Quantity)
		case "uom":
			els[i] = l.UOM
		case "price":
			if l.UnitPrice != 0 {
				els[i] = num(l.UnitPrice)
			}
		}
	}
	return els
}

func readDetail(roles []string, l *Line, get func(int) string) {
	for i, r := range roles {
		switch r {
		case "line":
			l.Line = inum(get(i + 1))
		case "qty":
			l.Quantity = fnum(get(i + 1))
		case "uom":
			l.UOM = get(i + 1)
		case "price":
			l.UnitPrice = fnum(get(i + 1))
		}
	}
}

var x12Profiles = map[string]x12Profile{
	// "lin" is the item profile of the report and advice sets: LIN with the GTIN, the set's own
	// quantity segment (DetailID), then description (PID), lot (REF*LT), expiry (DTM*036), a reason
	// (NTE*ADD) and the line status as message text (MSG). The status travels as text because no
	// code list for it is verified (see docs/OPEN_QUESTIONS.md).
	"lin": {start: "LIN",
		write: func(emit func(string, ...string), tm *x12Template, l Line) {
			emit("LIN", strconv.Itoa(l.Line), "UK", l.GTIN)
			if l.Description != "" {
				emit("PID", "F", "", "", "", clean(l.Description))
			}
			emit(tm.DetailID, detailEls(tm.DetailRoles, l)...)
			if l.Lot != "" {
				emit("REF", "LT", clean(l.Lot))
			}
			if l.Expiry != "" {
				emit("DTM", "036", ymdOf(l.Expiry))
			}
			if l.Reason != "" {
				emit("NTE", "ADD", clean(l.Reason))
			}
			if l.Status != "" {
				emit("MSG", clean(l.Status))
			}
		},
		read: func(l *Line, id string, get func(int) string, tm *x12Template) {
			switch id {
			case "LIN":
				*l = Line{Line: inum(get(1)), GTIN: itemID(2, get)}
			case "PID":
				l.Description = get(5)
			case tm.DetailID:
				readDetail(tm.DetailRoles, l, get)
			case "REF":
				if get(1) == "LT" {
					l.Lot = get(2)
				}
			case "DTM":
				if get(1) == "036" {
					l.Expiry = dayOf(get(2))
				}
			case "NTE":
				if get(1) == "ADD" {
					l.Reason = get(2)
				}
			case "MSG":
				l.Status = get(1)
			}
		}},
	"po1ack": {start: "PO1",
		write: func(emit func(string, ...string), tm *x12Template, l Line) {
			emit("PO1", strconv.Itoa(l.Line), num(l.Quantity), l.UOM, num(l.UnitPrice), "", "UK", l.GTIN)
			if l.Description != "" {
				emit("PID", "F", "", "", "", clean(l.Description))
			}
			if l.Status != "" {
				emit("ACK", tm.LineStatus[l.Status], num(l.Quantity), l.UOM)
			}
		},
		read: func(l *Line, id string, get func(int) string, tm *x12Template) {
			switch id {
			case "PO1":
				*l = Line{Line: inum(get(1)), Quantity: fnum(get(2)), UOM: get(3), UnitPrice: fnum(get(4)), GTIN: itemID(6, get)}
			case "PID":
				l.Description = get(5)
			case "ACK":
				l.Status = reverse(tm.LineStatus, get(1))
			}
		}},
	"poc": {start: "POC",
		write: func(emit func(string, ...string), tm *x12Template, l Line) {
			emit("POC", strconv.Itoa(l.Line), tm.LineStatus[l.Status], num(l.Quantity), "", l.UOM, num(l.UnitPrice), "", "UK", l.GTIN)
			if l.Description != "" {
				emit("PID", "F", "", "", "", clean(l.Description))
			}
		},
		read: func(l *Line, id string, get func(int) string, tm *x12Template) {
			switch id {
			case "POC":
				*l = Line{Line: inum(get(1)), Status: reverse(tm.LineStatus, get(2)), Quantity: fnum(get(3)), UOM: get(5), UnitPrice: fnum(get(6)), GTIN: itemID(8, get)}
			case "PID":
				l.Description = get(5)
			}
		}},
	"fst": {start: "LIN",
		write: func(emit func(string, ...string), tm *x12Template, l Line) {
			emit("LIN", strconv.Itoa(l.Line), "UK", l.GTIN)
			emit("UIT", l.UOM)
			emit("FST", num(l.Quantity), tm.LineStatus[l.Status], "D", ymdOf(l.Dates["delivery_requested"]))
			if l.Description != "" {
				emit("PID", "F", "", "", "", clean(l.Description))
			}
		},
		read: func(l *Line, id string, get func(int) string, tm *x12Template) {
			switch id {
			case "LIN":
				*l = Line{Line: inum(get(1)), GTIN: itemID(2, get)}
			case "UIT":
				l.UOM = get(1)
			case "FST":
				l.Quantity = fnum(get(1))
				l.Status = reverse(tm.LineStatus, get(2))
				setDate(&l.Dates, "delivery_requested", dayOf(get(4)))
			case "PID":
				l.Description = get(5)
			}
		}},
	"rmt": {start: "RMT",
		write: func(emit func(string, ...string), tm *x12Template, l Line) {
			emit("RMT", refQual(l.RefType), clean(l.Ref), num(l.Amount))
		},
		read: func(l *Line, id string, get func(int) string, tm *x12Template) {
			*l = Line{RefType: refKeyOf(get(1)), Ref: get(2), Amount: fnum(get(3))}
			if get(1) == "ZZ" {
				l.RefType = "other"
			}
		}},
	"oti": {start: "OTI",
		write: func(emit func(string, ...string), tm *x12Template, l Line) {
			emit("OTI", tm.LineStatus[l.Status], refQual(l.RefType), clean(l.Ref))
		},
		read: func(l *Line, id string, get func(int) string, tm *x12Template) {
			*l = Line{Status: reverse(tm.LineStatus, get(1)), RefType: refKeyOf(get(2)), Ref: get(3)}
		}},
	"ak2": {start: "AK2",
		write: func(emit func(string, ...string), tm *x12Template, l Line) {
			emit("AK2", l.RefType, clean(l.Ref))
			emit(tm.ackSeg(), tm.LineStatus[l.Status])
		},
		read: func(l *Line, id string, get func(int) string, tm *x12Template) {
			switch id {
			case "AK2":
				*l = Line{RefType: get(1), Ref: get(2)}
			case tm.ackSeg():
				l.Status = reverse(tm.LineStatus, get(1))
			}
		},
		trailer: func(emit func(string, ...string), d Document) {
			var acc, errs, rej int
			for _, l := range d.Lines {
				switch l.Status {
				case "accepted":
					acc++
				case "accepted_with_errors":
					errs++
				case "rejected":
					rej++
				}
			}
			agg := "P"
			switch {
			case rej == 0 && errs == 0:
				agg = "A"
			case rej == len(d.Lines):
				agg = "R"
			case rej == 0:
				agg = "E"
			}
			n := strconv.Itoa(len(d.Lines))
			emit("AK9", agg, n, n, strconv.Itoa(acc+errs))
		}},
}

// x12Role renders one element of a header segment.
func (t Type) x12Role(d Document, role string, usedDates, usedRefs map[string]bool) string {
	tm := t.x12
	kind, arg, _ := strings.Cut(role, ":")
	switch kind {
	case "":
		return ""
	case "const":
		return arg
	case "number":
		return clean(d.Number)
	case "date":
		if arg == "" {
			return d.Date.UTC().Format("20060102")
		}
		usedDates[arg] = true
		return ymdOf(d.Dates[arg])
	case "status":
		return tm.Status[d.Status]
	case "ref":
		usedRefs[arg] = true
		return clean(d.References[arg])
	case "amount":
		if v, ok := d.Amounts[arg]; ok {
			return num(v)
		}
		return ""
	case "gsid":
		if len(d.Lines) > 0 {
			return gsCode(d.Lines[0].RefType)
		}
	}
	return ""
}

// x12Body renders the segments between ST and SE for a template type.
func (t Type) x12Body(d Document) []string {
	tm := t.x12
	var segs []string
	emit := func(id string, els ...string) {
		for len(els) > 0 && els[len(els)-1] == "" {
			els = els[:len(els)-1]
		}
		segs = append(segs, strings.Join(append([]string{id}, els...), "*"))
	}
	usedDates, usedRefs := map[string]bool{}, map[string]bool{}
	for _, h := range tm.Header {
		els := make([]string, len(h.Elems))
		for i, role := range h.Elems {
			els[i] = t.x12Role(d, role, usedDates, usedRefs)
		}
		emit(h.ID, els...)
	}
	if tm.Currency && d.Currency != "" {
		emit("CUR", "BY", d.Currency)
	}
	for _, c := range ReferenceKeys {
		if c.X12 != "" && d.References[c.Key] != "" && !usedRefs[c.Key] {
			emit("REF", c.X12, clean(d.References[c.Key]))
		}
	}
	for _, k := range dateOrder(d.Dates) {
		if c := dateX12(k); c != "" && !usedDates[k] {
			emit("DTM", c, ymdOf(d.Dates[k]))
		}
	}
	party := func(code string, p Party) {
		if partyPresent(p) {
			if p.GLN != "" {
				emit("N1", code, clean(p.Name), "UL", p.GLN)
			} else {
				emit("N1", code, clean(p.Name))
			}
		}
	}
	party("BY", d.Buyer)
	party("SE", d.Seller)
	party("ST", d.ShipTo)
	for _, c := range PartyKeys {
		party(c.X12, d.Parties[c.Key])
	}
	prof := x12Profiles[tm.Lines]
	for _, l := range d.Lines {
		prof.write(emit, tm, l)
	}
	if prof.trailer != nil {
		prof.trailer(emit, d)
	}
	if tm.CTT {
		emit("CTT", strconv.Itoa(len(d.Lines)))
	}
	return segs
}

// applyRole stores one element value read from a header segment.
func (t Type) applyRole(d *Document, role, v string) {
	if v == "" {
		return
	}
	tm := t.x12
	kind, arg, _ := strings.Cut(role, ":")
	switch kind {
	case "number":
		d.Number = v
	case "date":
		if arg == "" {
			if tt, err := time.Parse("20060102", v); err == nil {
				d.Date = tt
			}
		} else {
			setDate(&d.Dates, arg, dayOf(v))
		}
	case "status":
		d.Status = reverse(tm.Status, v)
	case "ref":
		if d.References == nil {
			d.References = map[string]string{}
		}
		d.References[arg] = v
	case "amount":
		if d.Amounts == nil {
			d.Amounts = map[string]float64{}
		}
		d.Amounts[arg] = fnum(v)
	}
}

// parseX12Generic reads a template type's interchange.
func parseX12Generic(t Type, segs []string, sep string) (Document, error) {
	tm := t.x12
	d := Document{Type: t.Key}
	prof := x12Profiles[tm.Lines]
	seen := map[int]bool{}
	var cur *Line
	flush := func() {
		if cur != nil {
			d.Lines = append(d.Lines, *cur)
			cur = nil
		}
	}
	inST := false
	for _, sg := range segs {
		e := strings.Split(sg, sep)
		get := func(i int) string {
			if i < len(e) {
				return strings.TrimSpace(e[i])
			}
			return ""
		}
		id := e[0]
		if id == "GS" {
			if tt, err := time.Parse("20060102", get(4)); err == nil {
				d.Date = tt
			}
			continue
		}
		if id == "ST" {
			inST = true
			continue
		}
		if id == "SE" {
			break
		}
		if !inST {
			continue
		}
		headerSeg := false
		for hi, h := range tm.Header {
			if h.ID == id && !seen[hi] {
				seen[hi] = true
				headerSeg = true
				for i, role := range h.Elems {
					t.applyRole(&d, role, get(i+1))
				}
				break
			}
		}
		if headerSeg {
			continue
		}
		switch id {
		case "CUR":
			d.Currency = get(2)
			continue
		case "REF":
			if cur == nil || tm.Lines != "lin" {
				if k := refKeyOf(get(1)); k != "" && cur == nil {
					if d.References == nil {
						d.References = map[string]string{}
					}
					d.References[k] = get(2)
				}
				continue
			}
		case "N1":
			p := Party{Name: get(2), GLN: get(4)}
			switch get(1) {
			case "BY":
				d.Buyer = p
			case "SE":
				d.Seller = p
			case "ST":
				d.ShipTo = p
			default:
				for _, c := range PartyKeys {
					if c.X12 == get(1) {
						if d.Parties == nil {
							d.Parties = map[string]Party{}
						}
						d.Parties[c.Key] = p
					}
				}
			}
			continue
		case "DTM":
			if cur == nil {
				for _, c := range DateKeys {
					if c.X12 == get(1) {
						setDate(&d.Dates, c.Key, dayOf(get(2)))
					}
				}
				continue
			}
		}
		if id == prof.start {
			flush()
			cur = &Line{}
		}
		if cur != nil {
			prof.read(cur, id, get, tm)
		}
	}
	flush()
	if !inST {
		return d, errors.New("no transaction set found")
	}
	if len(d.Lines) == 0 {
		d.Lines = nil
	}
	if err := d.Validate(); err != nil {
		return d, fmt.Errorf("%s: %w", t.Key, err)
	}
	return d, nil
}
