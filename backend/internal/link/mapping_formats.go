package link

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/openspoor/openspoor/pkg/edi"
)

// Source formats of the mapping studio beyond tabular files: X12 and EDIFACT interchanges (read
// with the pkg/edi segment reader) and generic XML (encoding/xml). Every segment element or
// XML node becomes a source field with a path, so the same preview, suggestions and transforms
// work as for CSV and JSON.

const formatList = "csv, json, xml, x12, edifact or flat"

func validFormat(f string) bool {
	switch f {
	case "csv", "json", "xml", "x12", "edifact", "flat":
		return true
	}
	return false
}

var (
	segmentIDRE = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,2}$`)
	qualifierRE = regexp.MustCompile(`^[A-Z0-9]{1,3}$`)
	// qualifierIDs are the segments whose first element is a qualifier (date type, reference
	// type, party role ...), so SEG(Q) paths are offered for them only.
	qualifierIDs = map[string]bool{"DTM": true, "REF": true, "N1": true, "N9": true, "NAD": true, "RFF": true, "QTY": true, "MOA": true, "PIA": true,
		"FTX": true, "GIN": true, "NTE": true, "LOC": true, "AMT": true, "PER": true, "CTA": true, "COM": true, "TAX": true}
	// lineStarts lists, in order of preference, the segments that open a line when the mapping
	// does not name one. summaries end the line section (the header carries on after them).
	lineStarts = map[string][]string{
		"x12":     {"PO1", "IT1", "POC", "LIN", "BLI", "CDD", "RMT", "OTI", "AK2"},
		"edifact": {"LIN", "DOC"},
	}
	summaries = map[string]map[string]bool{
		"x12":     {"CTT": true, "SE": true, "TDS": true, "AK9": true},
		"edifact": {"UNS": true, "CNT": true, "UNT": true},
	}
)

// readEDI reads an X12 or EDIFACT interchange: one record per transaction set or message.
//
// Source field paths, relative to the record (header) or to a line:
//
//	SEG.NN       element NN (two digits) of the first occurrence of segment SEG
//	SEG.NN.MM    component MM of a composite element (the whole element is SEG.NN, joined with ":")
//	SEG[k].NN    the k-th occurrence, k >= 2
//	SEG(Q).NN    the first occurrence whose first element (or its first component) is Q, for example
//	             DTM(137).01.02 in EDIFACT, REF(PO).02 or N1(BY).02 in X12
//
// Lines start at the segment named by options.linesPath, or at the first of PO1, IT1, POC, LIN ...
// (X12) or LIN (EDIFACT) that the message contains, and end at a summary segment (CTT, UNS ...).
// The envelope segments (ISA, GS; UNB) are available in every record.
func readEDI(format string, o MappingOptions, content []byte) ([]record, []string, error) {
	ic, err := edi.ParseSegments(string(content))
	if err != nil {
		return nil, nil, fmt.Errorf("cannot read %s: %w", strings.ToUpper(format), err)
	}
	if ic.Standard != format {
		return nil, nil, fmt.Errorf("the file is an %s interchange, the mapping format is %s", strings.ToUpper(ic.Standard), format)
	}
	if len(ic.Messages) > maxRows {
		return nil, nil, fmt.Errorf("files are limited to %d rows", maxRows)
	}
	colSet := map[string]bool{}
	var out []record
	for i, msg := range ic.Messages {
		start := o.LinesPath
		if start == "" {
			start = detectLineStart(format, msg.Segments)
		}
		header, lines := splitLines(format, msg.Segments, start)
		vals := map[string]string{}
		flattenSegments(ic.Envelope, vals)
		flattenSegments(header, vals)
		rec := record{row: i + 1, values: vals}
		for _, l := range lines {
			lv := map[string]string{}
			flattenSegments(l, lv)
			rec.lines = append(rec.lines, lv)
			for k := range lv {
				colSet[k] = true
			}
		}
		if len(lines) == 0 {
			rec.lines = []map[string]string{vals}
		}
		for k := range vals {
			colSet[k] = true
		}
		out = append(out, rec)
	}
	return out, sortedSet(colSet), nil
}

func sortedSet(m map[string]bool) []string {
	cols := make([]string, 0, len(m))
	for k := range m {
		cols = append(cols, k)
	}
	sort.Strings(cols)
	return cols
}

func detectLineStart(format string, segs []edi.Segment) string {
	for _, cand := range lineStarts[format] {
		for _, s := range segs {
			if s.ID == cand {
				return cand
			}
		}
	}
	return ""
}

// splitLines separates the header segments from the line groups that begin at start.
func splitLines(format string, segs []edi.Segment, start string) (header []edi.Segment, lines [][]edi.Segment) {
	in := false
	for _, s := range segs {
		switch {
		case start != "" && s.ID == start:
			lines = append(lines, []edi.Segment{s})
			in = true
		case in && summaries[format][s.ID]:
			in = false
			header = append(header, s)
		case in:
			lines[len(lines)-1] = append(lines[len(lines)-1], s)
		default:
			header = append(header, s)
		}
	}
	return header, lines
}

// flattenSegments stores every non-empty element and component under its path (see readEDI).
func flattenSegments(segs []edi.Segment, out map[string]string) {
	seen, seenQ := map[string]int{}, map[string]bool{}
	put := func(base string, s edi.Segment) {
		for i, el := range s.Elements {
			key := fmt.Sprintf("%s.%02d", base, i+1)
			if joined := strings.Join(el, ":"); strings.Trim(joined, ":") != "" {
				out[key] = joined
			}
			if len(el) > 1 {
				for j, c := range el {
					if c != "" {
						out[fmt.Sprintf("%s.%02d", key, j+1)] = c
					}
				}
			}
		}
	}
	for _, s := range segs {
		seen[s.ID]++
		base := s.ID
		if k := seen[s.ID]; k > 1 {
			base = fmt.Sprintf("%s[%d]", s.ID, k)
		}
		put(base, s)
		if q := s.Element(1); qualifierIDs[s.ID] && qualifierRE.MatchString(q) {
			qb := s.ID + "(" + q + ")"
			if !seenQ[qb] {
				seenQ[qb] = true
				put(qb, s)
			}
		}
	}
}

// --- XML ---

const (
	maxXMLDepth    = 64
	maxXMLElements = 1_000_000
)

// readXML reads a generic XML file. The root element is not part of the paths: its children are
// addressed by their local names joined with dots (namespaces ignored), attributes as @name, the
// text of an element that also has attributes or children as #text. Elements that repeat under one
// parent form an array addressed by index (Line.0.Qty); options.recordsPath selects the repeating
// element that holds the records and options.linesPath the lines inside each (a single child
// counts as one line), as for JSON.
func readXML(o MappingOptions, content []byte) ([]record, []string, error) {
	root, err := xmlTree(content)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot read XML: %w", err)
	}
	return readTree(o, root)
}

type xmlNode struct {
	name     string
	attrs    map[string]any
	children map[string]any
	text     strings.Builder
}

func (n *xmlNode) add(name string, v any) {
	if n.children == nil {
		n.children = map[string]any{}
	}
	switch cur := n.children[name].(type) {
	case nil:
		n.children[name] = v
	case []any:
		n.children[name] = append(cur, v)
	default:
		n.children[name] = []any{cur, v}
	}
}

// value converts the node: a plain string for a leaf, otherwise a map.
func (n *xmlNode) value() any {
	text := strings.TrimSpace(n.text.String())
	if len(n.attrs) == 0 && len(n.children) == 0 {
		return text
	}
	m := map[string]any{}
	for k, v := range n.attrs {
		m[k] = v
	}
	for k, v := range n.children {
		m[k] = v
	}
	if text != "" {
		m["#text"] = text
	}
	return m
}

func latin1Reader(label string, r io.Reader) (io.Reader, error) {
	switch strings.ToLower(label) {
	case "iso-8859-1", "latin1", "us-ascii", "ascii", "windows-1252":
		b, err := io.ReadAll(r)
		if err != nil {
			return nil, err
		}
		rs := make([]rune, len(b))
		for i, c := range b {
			rs[i] = rune(c)
		}
		return strings.NewReader(string(rs)), nil
	}
	return nil, fmt.Errorf("encoding %q is not supported (use UTF-8 or ISO-8859-1)", label)
}

func xmlTree(content []byte) (any, error) {
	dec := xml.NewDecoder(bytes.NewReader(bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))))
	dec.CharsetReader = latin1Reader
	var stack []*xmlNode
	var root any
	rootSeen, elements := false, 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(stack) == 0 && rootSeen {
				return nil, errors.New("more than one root element")
			}
			if len(stack) >= maxXMLDepth {
				return nil, fmt.Errorf("elements are nested deeper than %d levels", maxXMLDepth)
			}
			if elements++; elements > maxXMLElements {
				return nil, fmt.Errorf("files are limited to %d elements", maxXMLElements)
			}
			rootSeen = true
			n := &xmlNode{name: t.Name.Local}
			for _, a := range t.Attr {
				if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" {
					continue
				}
				if n.attrs == nil {
					n.attrs = map[string]any{}
				}
				n.attrs["@"+a.Name.Local] = a.Value
			}
			stack = append(stack, n)
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text.Write(t)
			}
		case xml.EndElement:
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				root = n.value()
			} else {
				stack[len(stack)-1].add(n.name, n.value())
			}
		}
	}
	if !rootSeen {
		return nil, errors.New("no root element")
	}
	if s, ok := root.(string); ok {
		root = map[string]any{"#text": s}
	}
	return root, nil
}
