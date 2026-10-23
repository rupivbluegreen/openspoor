package edi

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// edifactTypes, x12Sets and idocTypes for the hand-written mappers are filled from the registry.

// EDIFACTType returns the message type for a document type ("" when none is attributed).
func EDIFACTType(docType string) string {
	if t, ok := Lookup(docType); ok {
		return t.EDIFACT
	}
	return ""
}

// lineActions maps canonical line statuses to the EDIFACT LIN action code (1229). Other
// statuses travel as free text (FTX+ZZZ).
var lineActions = map[string]string{"added": "1", "deleted": "2", "changed": "3", "accepted": "5"}

// withCurrency lists the messages that carry a CUX segment.
var withCurrency = map[string]bool{"ORDERS": true, "ORDRSP": true, "ORDCHG": true, "INVOIC": true, "REMADV": true, "QUOTES": true, "REQOTE": true,
	"COMDIS": true, "PRICAT": true, "PAYORD": true}

func statusFunction(status string) string {
	if c, ok := codeByKey(StatusKeys, status); ok {
		return c.EDIFACT
	}
	return ""
}

func edifactDate(q, day string) string { return "DTM+" + q + ":" + ymdOf(day) + ":102" }

// EDIFACT renders a UN/EDIFACT D.96A interchange with one message: the common subset of
// segments (BGM, DTM, RFF, NAD, CUX, LIN with its detail segments, UNS, MOA, CNT) for the
// document's registry type. It returns "" when the type has no EDIFACT writer.
func EDIFACT(d Document, env Envelope) string {
	t, ok := Lookup(d.Type)
	if !ok || !t.CanWriteEDIFACT() {
		return ""
	}
	if env.Control == 0 {
		env.Control = 1
	}
	from, to := d.Seller.GLN, d.Buyer.GLN
	if t.buyerSends() {
		from, to = to, from
	}
	if env.SenderID != "" {
		from = env.SenderID
	}
	if env.ReceiverID != "" {
		to = env.ReceiverID
	}
	msg := t.EDIFACT
	ref := strconv.Itoa(env.Control)
	var segs []string
	seg := func(s string) { segs = append(segs, s) }
	seg("UNH+1+" + msg + ":D:96A:UN:EAN008")
	fn := "9"
	if f := statusFunction(d.Status); f != "" {
		fn = f
	}
	seg("BGM+" + t.EDIFACTBGM + "+" + clean(d.Number) + "+" + fn)
	seg("DTM+137:" + d.Date.UTC().Format("20060102") + ":102")
	for _, c := range DateKeys {
		if v := d.Dates[c.Key]; v != "" {
			seg(edifactDate(c.EDIFACT, v))
		}
	}
	for _, c := range ReferenceKeys {
		if v := d.References[c.Key]; v != "" && c.EDIFACT != "" {
			seg("RFF+" + c.EDIFACT + ":" + clean(v))
		}
	}
	nad := func(q string, p Party) {
		if partyPresent(p) {
			seg("NAD+" + q + "+" + p.GLN + "::9++" + clean(p.Name))
		}
	}
	nad("BY", d.Buyer)
	nad("SU", d.Seller)
	nad("DP", d.ShipTo)
	for _, c := range PartyKeys {
		nad(c.EDIFACT, d.Parties[c.Key])
	}
	if withCurrency[msg] {
		seg("CUX+2:" + d.Currency + ":9")
	}
	if d.Status != "" && statusFunction(d.Status) == "" {
		seg("FTX+ZZZ+++" + clean(d.Status))
	}
	if d.Note != "" {
		seg("FTX+AAI+++" + clean(d.Note))
	}
	for _, s := range d.SSCCs {
		seg("CPS+" + s)
		seg("GIN+BJ+" + s)
	}
	for _, l := range d.Lines {
		lin := "LIN+" + strconv.Itoa(l.Line)
		action := lineActions[l.Status]
		switch {
		case l.GTIN != "":
			lin += "+" + action + "+" + l.GTIN + ":SRV"
		case action != "":
			lin += "+" + action
		}
		seg(lin)
		if l.Status != "" && action == "" {
			seg("FTX+ZZZ+++" + clean(l.Status))
		}
		if l.Description != "" {
			seg("IMD+F++:::" + clean(l.Description))
		}
		if t.Lines == LinesItem {
			seg("QTY+" + t.EDIFACTQty + ":" + num(l.Quantity) + ":" + l.UOM)
		}
		if l.Lot != "" {
			seg("PIA+5+" + clean(l.Lot) + ":NB")
		}
		if l.Expiry != "" {
			seg("DTM+361:" + strings.ReplaceAll(l.Expiry, "-", "") + ":102")
		}
		for _, c := range DateKeys {
			if v := l.Dates[c.Key]; v != "" {
				seg(edifactDate(c.EDIFACT, v))
			}
		}
		if l.UnitPrice != 0 {
			seg("PRI+AAA:" + num(l.UnitPrice))
		}
		if l.Ref != "" {
			q := "ZZZ"
			if c, ok := codeByKey(ReferenceKeys, l.RefType); ok && c.EDIFACT != "" {
				q = c.EDIFACT
			}
			seg("RFF+" + q + ":" + clean(l.Ref))
		}
		if l.Amount != 0 {
			seg("MOA+203:" + num(l.Amount))
		}
		for _, s := range l.Serials {
			seg("GIN+BN+" + clean(s))
		}
		if l.Reason != "" {
			seg("FTX+AAI+++" + clean(l.Reason))
		}
	}
	seg("UNS+S")
	if v, has := d.Amounts["total"]; has {
		seg("MOA+86:" + num(v))
	} else if msg == "INVOIC" && !hasKey(t.Amounts, "total") || msg == "COMDIS" {
		seg("MOA+86:" + num(d.Total()))
	}
	for _, c := range AmountKeys {
		if v, has := d.Amounts[c.Key]; has && c.Key != "total" {
			seg("MOA+" + c.EDIFACT + ":" + num(v))
		}
	}
	seg("CNT+2:" + strconv.Itoa(len(d.Lines)))
	segs = append(segs, "UNT+"+strconv.Itoa(len(segs)+1)+"+1")
	head := []string{"UNA:+.? ", "UNB+UNOC:3+" + from + ":14+" + to + ":14+" + d.Date.UTC().Format("060102:1504") + "+" + ref}
	return strings.Join(head, "'\n") + "'\n" + strings.Join(segs, "'\n") + "'\nUNZ+1+" + ref + "'\n"
}

// ParseEDIFACT reads an EDIFACT message into a canonical document. The registry type is
// chosen from the message type and document name code; where several types share both
// (ORDRSP for order_ack and order_change_ack) the first registered type wins, so use
// ParseEDIFACTAs to name the type.
func ParseEDIFACT(s string) (Document, error) { return parseEDIFACT(s, "") }

// ParseEDIFACTAs is ParseEDIFACT with the document type given by the caller.
func ParseEDIFACTAs(s, typeKey string) (Document, error) {
	if _, ok := Lookup(typeKey); !ok {
		return Document{}, fmt.Errorf("unknown document type %q", typeKey)
	}
	return parseEDIFACT(s, typeKey)
}

func parseEDIFACT(s, typeKey string) (Document, error) {
	var msg, bgm string
	for _, sg := range splitSegments(strings.TrimPrefix(strings.TrimSpace(s), "UNA:+.? "), '\'') {
		e := strings.Split(sg, "+")
		switch {
		case e[0] == "UNH" && len(e) > 2:
			msg = strings.Split(e[2], ":")[0]
		case e[0] == "BGM" && len(e) > 1:
			bgm = strings.Split(e[1], ":")[0]
		}
	}
	if msg == "" {
		return Document{Type: "order"}, errors.New("no UNH segment found")
	}
	if typeKey != "" {
		t, ok := Lookup(typeKey)
		if !ok || t.EDIFACT != msg {
			return Document{Type: "order"}, fmt.Errorf("message %s is not a %s", msg, typeKey)
		}
		return parseEDIFACTAs(s, t)
	}
	cands := candidatesEDIFACT(msg, bgm)
	if len(cands) == 0 {
		return Document{Type: "order"}, fmt.Errorf("EDIFACT message %s is not supported", msg)
	}
	var firstErr error
	var first Document
	for i, t := range cands {
		d, err := parseEDIFACTAs(s, t)
		if err == nil {
			return d, nil
		}
		if i == 0 {
			first, firstErr = d, err
		}
	}
	return first, firstErr
}

func parseEDIFACTAs(s string, t Type) (Document, error) {
	d := Document{Type: t.Key}
	s = strings.TrimPrefix(strings.TrimSpace(s), "UNA:+.? ")
	segs := splitSegments(s, '\'')
	split := func(sg string) (func(i, j int) string, string) {
		e := strings.Split(sg, "+")
		return func(i, j int) string {
			if i < len(e) {
				c := strings.Split(e[i], ":")
				if j < len(c) {
					return c[j]
				}
			}
			return ""
		}, e[0]
	}
	// Segments.
	var cur *Line
	for _, sg := range segs {
		comp, id := split(sg)
		switch id {
		case "BGM":
			d.Number = comp(2, 0)
			for _, c := range StatusKeys {
				if c.EDIFACT == comp(3, 0) {
					d.Status = c.Key
				}
			}
		case "DTM":
			day := ""
			if tt, err := time.Parse("20060102", comp(1, 1)); err == nil {
				day = tt.Format("2006-01-02")
			}
			q := comp(1, 0)
			switch {
			case q == "137" && cur == nil:
				if tt, err := time.Parse("20060102", comp(1, 1)); err == nil {
					d.Date = tt
				}
			case q == "361" && cur != nil:
				cur.Expiry = day
			default:
				for _, c := range DateKeys {
					if c.EDIFACT == q && cur != nil {
						setDate(&cur.Dates, c.Key, day)
					} else if c.EDIFACT == q {
						setDate(&d.Dates, c.Key, day)
					}
				}
			}
		case "RFF":
			q, v := comp(1, 0), comp(1, 1)
			key := ""
			for _, c := range ReferenceKeys {
				if c.EDIFACT == q {
					key = c.Key
				}
			}
			switch {
			case key == "":
			case cur != nil:
				cur.Ref, cur.RefType = v, key
			default:
				if d.References == nil {
					d.References = map[string]string{}
				}
				d.References[key] = v
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
			default:
				for _, c := range PartyKeys {
					if c.EDIFACT == comp(1, 0) {
						if d.Parties == nil {
							d.Parties = map[string]Party{}
						}
						d.Parties[c.Key] = p
					}
				}
			}
		case "CUX":
			d.Currency = comp(1, 1)
		case "GIN":
			switch {
			case comp(1, 0) == "BJ" && cur == nil:
				d.SSCCs = append(d.SSCCs, comp(2, 0))
			case comp(1, 0) == "BN" && cur != nil:
				cur.Serials = append(cur.Serials, comp(2, 0))
			}
		case "LIN":
			d.Lines = append(d.Lines, Line{Line: inum(comp(1, 0)), GTIN: comp(3, 0), Status: reverse(lineActions, comp(2, 0))})
			cur = &d.Lines[len(d.Lines)-1]
		case "IMD":
			if cur != nil {
				cur.Description = comp(3, 3)
			}
		case "QTY":
			if cur != nil {
				cur.Quantity, cur.UOM = fnum(comp(1, 1)), comp(1, 2)
			}
		case "PRI":
			if cur != nil {
				cur.UnitPrice = fnum(comp(1, 1))
			}
		case "PIA":
			if cur != nil {
				cur.Lot = comp(2, 0)
			}
		case "FTX":
			text := comp(4, 0)
			switch {
			case comp(1, 0) == "ZZZ" && cur != nil:
				cur.Status = text
			case comp(1, 0) == "ZZZ":
				d.Status = text
			case comp(1, 0) == "AAI" && cur != nil:
				cur.Reason = text
			case comp(1, 0) == "AAI":
				d.Note = text
			}
		case "MOA":
			v := fnum(comp(1, 1))
			q := comp(1, 0)
			switch {
			case q == "203" && cur != nil:
				cur.Amount = v
			case cur == nil || q != "203":
				for _, c := range AmountKeys {
					if c.EDIFACT == q && (hasKey(t.Amounts, c.Key)) {
						if d.Amounts == nil {
							d.Amounts = map[string]float64{}
						}
						d.Amounts[c.Key] = v
					}
				}
			}
		case "UNS":
			cur = nil // summary section: header-level MOA follow
		}
	}
	return d, d.Validate()
}
