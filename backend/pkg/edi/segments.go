package edi

import (
	"errors"
	"fmt"
	"strings"
)

// Segment is one raw segment of an X12 or EDIFACT interchange: its ID and its data elements,
// each split into components (X12 sub-elements, EDIFACT composite components). Elements[0] is
// the first data element ("01"). This is the syntax level only; ParseX12 and ParseEDIFACT turn
// the segments of a registry type into a canonical Document.
type Segment struct {
	ID       string
	Elements [][]string
}

// Element returns the value of data element n (1-based) joined back from its components, or "".
func (s Segment) Element(n int) string {
	if n < 1 || n > len(s.Elements) {
		return ""
	}
	return s.Elements[n-1][0]
}

// Component returns component c (1-based) of data element n (1-based), or "".
func (s Segment) Component(n, c int) string {
	if n < 1 || n > len(s.Elements) || c < 1 || c > len(s.Elements[n-1]) {
		return ""
	}
	return s.Elements[n-1][c-1]
}

// Message is one transaction set (X12, ST to SE) or message (EDIFACT, UNH to UNT) with its
// segments, the envelope segments excluded.
type Message struct {
	// Type is the transaction set number ("850") or the message type ("ORDERS").
	Type     string
	Segments []Segment
}

// Interchange is the raw structure of an X12 or EDIFACT file.
type Interchange struct {
	// Standard is "x12" or "edifact".
	Standard string
	// Envelope holds the interchange and group segments (ISA, GS, GE, IEA; UNB, UNG, UNE, UNZ).
	Envelope []Segment
	Messages []Message
}

// MaxSegments bounds the segments ParseSegments reads from one file.
const MaxSegments = 500_000

// ParseSegments reads the syntax of an X12 or EDIFACT interchange without interpreting it:
// separators come from the ISA segment (X12) or the UNA service string advice (EDIFACT,
// release character honoured), and the segments are grouped into messages. It accepts any
// transaction set or message type; it fails when the text is neither standard or has no
// message.
func ParseSegments(s string) (Interchange, error) {
	s = strings.TrimPrefix(s, "\xef\xbb\xbf")
	t := strings.TrimLeft(s, " \t\r\n")
	switch {
	case strings.HasPrefix(t, "ISA"):
		return parseX12Segments(t)
	case strings.HasPrefix(t, "UNA"), strings.HasPrefix(t, "UNB"), strings.HasPrefix(t, "UNH"):
		return parseEDIFACTSegments(t)
	}
	return Interchange{}, errors.New("not an X12 (ISA) or EDIFACT (UNA, UNB or UNH) interchange")
}

func parseX12Segments(s string) (Interchange, error) {
	if len(s) < 4 {
		return Interchange{}, errors.New("X12: the ISA segment is truncated")
	}
	sep := s[3]
	// ISA has 16 data elements; the 16th is the sub-element separator, followed by the segment terminator.
	pos, n := 0, 0
	for i := 3; i < len(s); i++ {
		if s[i] == sep {
			n++
			if n == 16 {
				pos = i
				break
			}
		}
	}
	if n < 16 || pos+2 >= len(s) {
		return Interchange{}, errors.New("X12: the ISA segment is truncated (16 elements, a sub-element separator and a terminator are required)")
	}
	sub, term := s[pos+1], s[pos+2]
	ic := Interchange{Standard: "x12"}
	var cur *Message
	count := 0
	for _, raw := range strings.Split(s, string(term)) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if count++; count > MaxSegments {
			return ic, fmt.Errorf("X12: more than %d segments", MaxSegments)
		}
		seg := Segment{}
		for i, el := range strings.Split(raw, string(sep)) {
			if i == 0 {
				seg.ID = strings.TrimSpace(el)
				continue
			}
			seg.Elements = append(seg.Elements, strings.Split(el, string(sub)))
		}
		switch seg.ID {
		case "ST":
			ic.Messages = append(ic.Messages, Message{Type: strings.TrimSpace(seg.Element(1))})
			cur = &ic.Messages[len(ic.Messages)-1]
			cur.Segments = append(cur.Segments, seg)
		case "SE":
			if cur != nil {
				cur.Segments = append(cur.Segments, seg)
				cur = nil
			}
		case "ISA", "GS", "GE", "IEA":
			ic.Envelope = append(ic.Envelope, seg)
		default:
			if cur != nil {
				cur.Segments = append(cur.Segments, seg)
			}
		}
	}
	if len(ic.Messages) == 0 {
		return ic, errors.New("X12: no transaction set (ST) found")
	}
	return ic, nil
}

func parseEDIFACTSegments(s string) (Interchange, error) {
	comp, data, release, term := byte(':'), byte('+'), byte('?'), byte('\'')
	if strings.HasPrefix(s, "UNA") {
		if len(s) < 9 {
			return Interchange{}, errors.New("EDIFACT: the UNA segment is truncated")
		}
		comp, data, release, term = s[3], s[4], s[6], s[8]
		s = s[9:]
	}
	ic := Interchange{Standard: "edifact"}
	var cur *Message
	count := 0
	emit := func(raw []byte) error {
		// raw holds the segment with release characters still in place; split on the unescaped separators.
		str := strings.TrimSpace(string(raw))
		if str == "" {
			return nil
		}
		if count++; count > MaxSegments {
			return fmt.Errorf("EDIFACT: more than %d segments", MaxSegments)
		}
		seg := Segment{}
		var el []string
		var cc []byte
		flushComp := func() { el = append(el, string(cc)); cc = nil }
		flushEl := func() {
			flushComp()
			if seg.ID == "" {
				seg.ID = el[0]
			} else {
				seg.Elements = append(seg.Elements, el)
			}
			el = nil
		}
		for i := 0; i < len(str); i++ {
			switch c := str[i]; {
			case c == release && i+1 < len(str):
				i++
				cc = append(cc, str[i])
			case c == data:
				flushEl()
			case c == comp:
				flushComp()
			default:
				cc = append(cc, c)
			}
		}
		flushEl()
		seg.ID = strings.TrimSpace(seg.ID)
		switch seg.ID {
		case "UNH":
			ic.Messages = append(ic.Messages, Message{Type: seg.Component(2, 1)})
			cur = &ic.Messages[len(ic.Messages)-1]
			cur.Segments = append(cur.Segments, seg)
		case "UNT":
			if cur != nil {
				cur.Segments = append(cur.Segments, seg)
				cur = nil
			}
		case "UNB", "UNG", "UNE", "UNZ":
			ic.Envelope = append(ic.Envelope, seg)
		default:
			if cur != nil {
				cur.Segments = append(cur.Segments, seg)
			}
		}
		return nil
	}
	var buf []byte
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == release && i+1 < len(s):
			buf = append(buf, c, s[i+1])
			i++
		case c == term:
			if err := emit(buf); err != nil {
				return ic, err
			}
			buf = buf[:0]
		default:
			buf = append(buf, c)
		}
	}
	if err := emit(buf); err != nil {
		return ic, err
	}
	if len(ic.Messages) == 0 {
		return ic, errors.New("EDIFACT: no message (UNH) found")
	}
	return ic, nil
}
