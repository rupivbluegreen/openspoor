package epcis

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// ParseJSON reads an EPCIS 2.0 JSON-LD document. A bare single event is also accepted.
func ParseJSON(b []byte) (*Document, error) {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	switch probe.Type {
	case "EPCISDocument", "EPCISQueryDocument":
		var d Document
		if err := json.Unmarshal(b, &d); err != nil {
			return nil, fmt.Errorf("invalid EPCIS document: %w", err)
		}
		if d.Body.QueryResults != nil && len(d.Body.EventList) == 0 {
			d.Body.EventList = d.Body.QueryResults.ResultsBody.EventList
		}
		return &d, nil
	case ObjectEvent, AggregationEvent, TransactionEvent, TransformationEvent, AssociationEvent:
		var e Event
		if err := json.Unmarshal(b, &e); err != nil {
			return nil, fmt.Errorf("invalid EPCIS event: %w", err)
		}
		return &Document{Type: "EPCISDocument", SchemaVersion: "2.0", CreationDate: time.Now().UTC(), Body: Body{EventList: []Event{e}}}, nil
	}
	return nil, errors.New(`document "type" must be EPCISDocument`)
}

// WriteJSON serializes a document as EPCIS 2.0 JSON-LD.
func WriteJSON(d *Document) ([]byte, error) {
	if d.Context == nil {
		d.Context = []any{DefaultContext}
	}
	if d.SchemaVersion == "" {
		d.SchemaVersion = "2.0"
	}
	return json.Marshal(d)
}

// NewDocument wraps events in a capture document.
func NewDocument(events []Event) *Document {
	return &Document{Context: []any{DefaultContext}, Type: "EPCISDocument", SchemaVersion: "2.0", CreationDate: time.Now().UTC().Truncate(time.Millisecond), Body: Body{EventList: events}}
}

// Parse dispatches on content type (or sniffs the first byte).
func Parse(contentType string, b []byte) (*Document, string, error) {
	ct := strings.ToLower(contentType)
	trim := bytes.TrimSpace(b)
	if strings.Contains(ct, "xml") || (len(trim) > 0 && trim[0] == '<') {
		d, err := ParseXML(bytes.NewReader(b))
		return d, "xml", err
	}
	d, err := ParseJSON(b)
	return d, "json", err
}

// --- XML ---

type node struct {
	Name     string
	Attrs    map[string]string
	Children []*node
	Text     string
}

func (n *node) child(name string) *node {
	for _, c := range n.Children {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func (n *node) text(name string) string {
	if c := n.child(name); c != nil {
		return strings.TrimSpace(c.Text)
	}
	return ""
}

func (n *node) list(name, item string) []string {
	c := n.child(name)
	if c == nil {
		return nil
	}
	var out []string
	for _, x := range c.Children {
		if x.Name == item {
			out = append(out, strings.TrimSpace(x.Text))
		}
	}
	return out
}

// flatten merges <extension> wrappers (EPCIS 1.x) into their parent.
func (n *node) flatten() {
	var kids []*node
	for _, c := range n.Children {
		c.flatten()
		if c.Name == "extension" || c.Name == "baseExtension" {
			kids = append(kids, c.Children...)
		} else {
			kids = append(kids, c)
		}
	}
	n.Children = kids
}

func readTree(r io.Reader) (*node, error) {
	dec := xml.NewDecoder(r)
	dec.Strict = true
	var stack []*node
	var root *node
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("invalid XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &node{Name: t.Name.Local, Attrs: map[string]string{}}
			for _, a := range t.Attr {
				n.Attrs[a.Name.Local] = a.Value
			}
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.Children = append(p.Children, n)
			} else {
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].Text += string(t)
			}
		}
	}
	if root == nil {
		return nil, errors.New("empty XML document")
	}
	return root, nil
}

// ParseXML reads EPCIS 2.0 XML and EPCIS 1.2 XML (extension wrappers are flattened).
func ParseXML(r io.Reader) (*Document, error) {
	root, err := readTree(r)
	if err != nil {
		return nil, err
	}
	if root.Name != "EPCISDocument" && root.Name != "EPCISQueryDocument" {
		return nil, fmt.Errorf("root element must be EPCISDocument, got %s", root.Name)
	}
	root.flatten()
	d := &Document{Type: "EPCISDocument", SchemaVersion: root.Attrs["schemaVersion"]}
	if cd := root.Attrs["creationDate"]; cd != "" {
		d.CreationDate, _ = parseTime(cd)
	}
	body := root.child("EPCISBody")
	if body == nil {
		return nil, errors.New("missing EPCISBody")
	}
	el := body.child("EventList")
	if el == nil {
		// query document: EPCISBody/QueryResults/resultsBody/EventList
		if qr := body.child("QueryResults"); qr != nil {
			if rb := qr.child("resultsBody"); rb != nil {
				el = rb.child("EventList")
			}
		}
	}
	if el == nil {
		return d, nil
	}
	for i, en := range el.Children {
		e, err := eventFromXML(en)
		if err != nil {
			return nil, fmt.Errorf("event %d: %w", i, err)
		}
		d.Body.EventList = append(d.Body.EventList, e)
	}
	return d, nil
}

func parseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, f := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05Z0700"} {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time %q", s)
}

func quantities(n *node, name string) []QuantityElement {
	c := n.child(name)
	if c == nil {
		return nil
	}
	var out []QuantityElement
	for _, q := range c.Children {
		qe := QuantityElement{EPCClass: q.text("epcClass"), UOM: q.text("uom")}
		if s := q.text("quantity"); s != "" {
			if f, err := strconv.ParseFloat(s, 64); err == nil {
				qe.Quantity = &f
			}
		}
		out = append(out, qe)
	}
	return out
}

func eventFromXML(n *node) (Event, error) {
	e := Event{Type: n.Name}
	switch n.Name {
	case ObjectEvent, AggregationEvent, TransactionEvent, TransformationEvent, AssociationEvent:
	default:
		return e, fmt.Errorf("unknown event element %s", n.Name)
	}
	var err error
	if e.EventTime, err = parseTime(n.text("eventTime")); err != nil {
		return e, err
	}
	e.EventTimeZoneOffset = n.text("eventTimeZoneOffset")
	if s := n.text("recordTime"); s != "" {
		if t, err := parseTime(s); err == nil {
			e.RecordTime = &t
		}
	}
	e.EventID = n.text("eventID")
	if ed := n.child("errorDeclaration"); ed != nil {
		dt, _ := parseTime(ed.text("declarationTime"))
		e.ErrorDeclaration = &ErrorDeclaration{DeclarationTime: dt, Reason: ed.text("reason"), CorrectiveEventIDs: ed.list("correctiveEventIDs", "correctiveEventID")}
	}
	e.ParentID = n.text("parentID")
	e.EPCList = n.list("epcList", "epc")
	e.ChildEPCs = n.list("childEPCs", "epc")
	e.InputEPCList = n.list("inputEPCList", "epc")
	e.OutputEPCList = n.list("outputEPCList", "epc")
	e.QuantityList = quantities(n, "quantityList")
	e.ChildQuantityList = quantities(n, "childQuantityList")
	e.InputQuantityList = quantities(n, "inputQuantityList")
	e.OutputQuantityList = quantities(n, "outputQuantityList")
	e.TransformationID = n.text("transformationID")
	e.Action = n.text("action")
	e.BizStep = n.text("bizStep")
	e.Disposition = n.text("disposition")
	if rp := n.child("readPoint"); rp != nil {
		e.ReadPoint = &Location{ID: rp.text("id")}
	}
	if bl := n.child("bizLocation"); bl != nil {
		e.BizLocation = &Location{ID: bl.text("id")}
	}
	if c := n.child("bizTransactionList"); c != nil {
		for _, bt := range c.Children {
			e.BizTransactionList = append(e.BizTransactionList, BizTransaction{Type: bt.Attrs["type"], BizTransaction: strings.TrimSpace(bt.Text)})
		}
	}
	if c := n.child("sourceList"); c != nil {
		for _, s := range c.Children {
			e.SourceList = append(e.SourceList, Source{Type: s.Attrs["type"], Source: strings.TrimSpace(s.Text)})
		}
	}
	if c := n.child("destinationList"); c != nil {
		for _, s := range c.Children {
			e.DestinationList = append(e.DestinationList, Destination{Type: s.Attrs["type"], Destination: strings.TrimSpace(s.Text)})
		}
	}
	if c := n.child("ilmd"); c != nil {
		e.ILMD = map[string]any{}
		for _, x := range c.Children {
			e.ILMD[x.Name] = xmlValue(x)
		}
	}
	return e, nil
}

func xmlValue(n *node) any {
	if len(n.Children) == 0 {
		return strings.TrimSpace(n.Text)
	}
	m := map[string]any{}
	for _, c := range n.Children {
		m[c.Name] = xmlValue(c)
	}
	return m
}

// WriteXML serializes events as an EPCIS 2.0 XML document.
func WriteXML(d *Document) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(xml.Header)
	fmt.Fprintf(&b, `<epcis:EPCISDocument xmlns:epcis="urn:epcglobal:epcis:xsd:2" schemaVersion="2.0" creationDate="%s">`, d.CreationDate.UTC().Format(time.RFC3339Nano))
	b.WriteString("<EPCISBody><EventList>")
	for i := range d.Body.EventList {
		writeEventXML(&b, &d.Body.EventList[i])
	}
	b.WriteString("</EventList></EPCISBody></epcis:EPCISDocument>")
	return b.Bytes(), nil
}

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func writeEventXML(b *bytes.Buffer, e *Event) {
	el := func(name, v string) {
		if v != "" {
			fmt.Fprintf(b, "<%s>%s</%s>", name, esc(v), name)
		}
	}
	lst := func(name string, xs []string) {
		if len(xs) == 0 {
			return
		}
		fmt.Fprintf(b, "<%s>", name)
		for _, x := range xs {
			el("epc", x)
		}
		fmt.Fprintf(b, "</%s>", name)
	}
	qty := func(name string, qs []QuantityElement) {
		if len(qs) == 0 {
			return
		}
		fmt.Fprintf(b, "<%s>", name)
		for _, q := range qs {
			b.WriteString("<quantityElement>")
			el("epcClass", q.EPCClass)
			if q.Quantity != nil {
				el("quantity", strconv.FormatFloat(*q.Quantity, 'f', -1, 64))
			}
			el("uom", q.UOM)
			b.WriteString("</quantityElement>")
		}
		fmt.Fprintf(b, "</%s>", name)
	}
	fmt.Fprintf(b, "<%s>", e.Type)
	el("eventTime", e.EventTime.Format(time.RFC3339Nano))
	if e.RecordTime != nil {
		el("recordTime", e.RecordTime.Format(time.RFC3339Nano))
	}
	el("eventTimeZoneOffset", e.EventTimeZoneOffset)
	el("eventID", e.EventID)
	if e.ErrorDeclaration != nil {
		b.WriteString("<errorDeclaration>")
		el("declarationTime", e.ErrorDeclaration.DeclarationTime.Format(time.RFC3339Nano))
		el("reason", URN("er", e.ErrorDeclaration.Reason))
		b.WriteString("</errorDeclaration>")
	}
	el("parentID", e.ParentID)
	lst("epcList", e.EPCList)
	lst("childEPCs", e.ChildEPCs)
	lst("inputEPCList", e.InputEPCList)
	qty("inputQuantityList", e.InputQuantityList)
	lst("outputEPCList", e.OutputEPCList)
	qty("outputQuantityList", e.OutputQuantityList)
	el("transformationID", e.TransformationID)
	el("action", e.Action)
	el("bizStep", URN("bizstep", e.BizStep))
	el("disposition", URN("disp", e.Disposition))
	if e.ReadPoint != nil {
		fmt.Fprintf(b, "<readPoint><id>%s</id></readPoint>", esc(e.ReadPoint.ID))
	}
	if e.BizLocation != nil {
		fmt.Fprintf(b, "<bizLocation><id>%s</id></bizLocation>", esc(e.BizLocation.ID))
	}
	if len(e.BizTransactionList) > 0 {
		b.WriteString("<bizTransactionList>")
		for _, t := range e.BizTransactionList {
			fmt.Fprintf(b, `<bizTransaction type="%s">%s</bizTransaction>`, esc(URN("btt", t.Type)), esc(t.BizTransaction))
		}
		b.WriteString("</bizTransactionList>")
	}
	qty("quantityList", e.QuantityList)
	qty("childQuantityList", e.ChildQuantityList)
	if len(e.SourceList) > 0 {
		b.WriteString("<sourceList>")
		for _, s := range e.SourceList {
			fmt.Fprintf(b, `<source type="%s">%s</source>`, esc(URN("sdt", s.Type)), esc(s.Source))
		}
		b.WriteString("</sourceList>")
	}
	if len(e.DestinationList) > 0 {
		b.WriteString("<destinationList>")
		for _, d := range e.DestinationList {
			fmt.Fprintf(b, `<destination type="%s">%s</destination>`, esc(URN("sdt", d.Type)), esc(d.Destination))
		}
		b.WriteString("</destinationList>")
	}
	if len(e.ILMD) > 0 {
		b.WriteString("<ilmd>")
		for k, v := range e.ILMD {
			if s, ok := v.(string); ok && isXMLName(k) {
				el(k, s)
			}
		}
		b.WriteString("</ilmd>")
	}
	fmt.Fprintf(b, "</%s>", e.Type)
}

func isXMLName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && (r >= '0' && r <= '9' || r == '-' || r == '.')) {
			return false
		}
	}
	return true
}
