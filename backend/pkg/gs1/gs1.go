// Package gs1 implements GS1 identifiers from the public GS1 General Specifications,
// GS1 Digital Link URI syntax and the EPC Tag Data Standard (pure identity URNs).
package gs1

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// CheckDigit computes the GS1 mod-10 check digit for digits (without the check digit).
func CheckDigit(digits string) (byte, error) {
	sum := 0
	n := len(digits)
	for i := 0; i < n; i++ {
		c := digits[n-1-i]
		if c < '0' || c > '9' {
			return 0, errors.New("non-digit")
		}
		d := int(c - '0')
		if i%2 == 0 {
			d *= 3
		}
		sum += d
	}
	return byte('0' + (10-sum%10)%10), nil
}

func validKey(s string, n int) bool {
	if len(s) != n {
		return false
	}
	cd, err := CheckDigit(s[:n-1])
	return err == nil && cd == s[n-1]
}

// ValidGTIN validates a GTIN-14 (pad shorter GTINs with NormalizeGTIN first).
func ValidGTIN(s string) bool { return validKey(s, 14) }

// ValidGLN validates a 13-digit GLN.
func ValidGLN(s string) bool { return validKey(s, 13) }

// ValidSSCC validates an 18-digit SSCC.
func ValidSSCC(s string) bool { return validKey(s, 18) }

// NormalizeGTIN left-pads GTIN-8/12/13 to GTIN-14.
func NormalizeGTIN(s string) string {
	if len(s) < 14 {
		return strings.Repeat("0", 14-len(s)) + s
	}
	return s
}

// WithCheckDigit appends the check digit.
func WithCheckDigit(body string) string {
	cd, _ := CheckDigit(body)
	return body + string(cd)
}

// AI is a GS1 Application Identifier value.
type AI struct {
	Code  string
	Value string
}

// Known fixed-length AIs (length of value). Others are variable and terminated by GS.
var fixedAI = map[string]int{"00": 18, "01": 14, "02": 14, "11": 6, "12": 6, "13": 6, "15": 6, "16": 6, "17": 6, "20": 2, "410": 13, "411": 13, "412": 13, "414": 13, "416": 13}

var varAI = map[string]int{"10": 20, "21": 20, "22": 20, "240": 30, "241": 30, "250": 30, "254": 20, "400": 30, "401": 30, "710": 20, "711": 20, "712": 20, "713": 20, "714": 20, "235": 28, "7003": 10, "7240": 20}

// GS is the ASCII group separator used as FNC1 in element strings.
const GS = "\x1d"

// ParseElementString parses a GS1 element string (as read from a DataMatrix, with GS separators,
// an optional leading "]d2" symbology identifier, or human-readable "(01)...(21)..." form).
func ParseElementString(s string) ([]AI, error) {
	s = strings.TrimPrefix(s, "]d2")
	s = strings.TrimPrefix(s, "]C1")
	s = strings.TrimPrefix(s, "]Q3")
	if strings.HasPrefix(s, "(") {
		return parseHRI(s)
	}
	var out []AI
	for len(s) > 0 {
		if strings.HasPrefix(s, GS) {
			s = s[1:]
			continue
		}
		code, n, fixed := lookupAI(s)
		if code == "" {
			return out, fmt.Errorf("unknown application identifier at %q", truncate(s, 6))
		}
		s = s[len(code):]
		if fixed {
			if len(s) < n {
				return out, fmt.Errorf("AI (%s) too short", code)
			}
			out = append(out, AI{code, s[:n]})
			s = s[n:]
			continue
		}
		end := strings.Index(s, GS)
		if end < 0 {
			end = len(s)
		}
		if end > n {
			return out, fmt.Errorf("AI (%s) exceeds %d characters", code, n)
		}
		out = append(out, AI{code, s[:end]})
		s = s[end:]
	}
	return out, nil
}

func lookupAI(s string) (string, int, bool) {
	for l := 2; l <= 4 && l <= len(s); l++ {
		c := s[:l]
		if n, ok := fixedAI[c]; ok {
			return c, n, true
		}
		if n, ok := varAI[c]; ok {
			return c, n, false
		}
	}
	return "", 0, false
}

var hriRe = regexp.MustCompile(`\((\d{2,4})\)([^(]*)`)

func parseHRI(s string) ([]AI, error) {
	m := hriRe.FindAllStringSubmatch(s, -1)
	if len(m) == 0 {
		return nil, errors.New("no application identifiers found")
	}
	out := make([]AI, 0, len(m))
	for _, g := range m {
		out = append(out, AI{g[1], strings.TrimSpace(g[2])})
	}
	return out, nil
}

// ElementString builds a GS1 element string with GS separators after variable-length fields.
// Fixed-length AIs are placed first, as recommended, to minimize separators.
func ElementString(ais []AI) string {
	sorted := append([]AI(nil), ais...)
	sort.SliceStable(sorted, func(i, j int) bool {
		_, fi := fixedAI[sorted[i].Code]
		_, fj := fixedAI[sorted[j].Code]
		return fi && !fj
	})
	var b strings.Builder
	for i, a := range sorted {
		b.WriteString(a.Code)
		b.WriteString(a.Value)
		if _, fixed := fixedAI[a.Code]; !fixed && i < len(sorted)-1 {
			b.WriteString(GS)
		}
	}
	return b.String()
}

// HRI renders human-readable "(01)...(17)..." text.
func HRI(ais []AI) string {
	var b strings.Builder
	for _, a := range ais {
		fmt.Fprintf(&b, "(%s)%s", a.Code, a.Value)
	}
	return b.String()
}

// Pack is a serialized pharma pack as encoded in a GS1 DataMatrix.
type Pack struct {
	GTIN   string `json:"gtin"`
	Serial string `json:"serial"`
	Lot    string `json:"lot,omitempty"`
	Expiry string `json:"expiry,omitempty"` // YYMMDD
	NHRN   string `json:"nhrn,omitempty"`   // national healthcare reimbursement number (AI 710-714)
}

// PackFromAIs extracts a Pack.
func PackFromAIs(ais []AI) (Pack, error) {
	var p Pack
	for _, a := range ais {
		switch a.Code {
		case "01":
			p.GTIN = a.Value
		case "21":
			p.Serial = a.Value
		case "10":
			p.Lot = a.Value
		case "17":
			p.Expiry = a.Value
		case "710", "711", "712", "713", "714":
			p.NHRN = a.Value
		}
	}
	if !ValidGTIN(p.GTIN) {
		return p, errors.New("missing or invalid GTIN (01)")
	}
	return p, nil
}

// AIs converts a Pack to AIs in canonical order.
func (p Pack) AIs() []AI {
	out := []AI{{"01", p.GTIN}}
	if p.Expiry != "" {
		out = append(out, AI{"17", p.Expiry})
	}
	if p.Lot != "" {
		out = append(out, AI{"10", p.Lot})
	}
	if p.Serial != "" {
		out = append(out, AI{"21", p.Serial})
	}
	if p.NHRN != "" {
		out = append(out, AI{"710", p.NHRN})
	}
	return out
}

// ExpiryDate parses YYMMDD (DD=00 means end of month) per GS1 rules.
func ExpiryDate(yymmdd string) (time.Time, error) {
	if len(yymmdd) != 6 {
		return time.Time{}, errors.New("expiry must be YYMMDD")
	}
	t, err := time.Parse("060102", yymmdd[:4]+"01")
	if err != nil {
		return t, err
	}
	if yymmdd[4:] == "00" {
		return t.AddDate(0, 1, -1), nil
	}
	return time.Parse("060102", yymmdd)
}

// --- GS1 Digital Link ---

// DefaultResolver is the GS1 global resolver domain.
const DefaultResolver = "https://id.gs1.org"

// DigitalLink builds a GS1 Digital Link URI for the pack on the given domain.
func DigitalLink(domain string, p Pack) string {
	if domain == "" {
		domain = DefaultResolver
	}
	u := strings.TrimSuffix(domain, "/") + "/01/" + p.GTIN
	if p.Lot != "" {
		u += "/10/" + url.PathEscape(p.Lot)
	}
	if p.Serial != "" {
		u += "/21/" + url.PathEscape(p.Serial)
	}
	if p.Expiry != "" {
		u += "?17=" + p.Expiry
	}
	return u
}

// ParseDigitalLink extracts AIs from a GS1 Digital Link URI (path + query AIs).
func ParseDigitalLink(raw string) ([]AI, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	segs := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	var out []AI
	start := -1
	for i := 0; i+1 < len(segs); i++ {
		if segs[i] == "01" || segs[i] == "00" || segs[i] == "414" || segs[i] == "8004" {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, errors.New("no primary key (01/00/414) in digital link path")
	}
	for i := start; i+1 < len(segs); i += 2 {
		v, err := url.PathUnescape(segs[i+1])
		if err != nil {
			return nil, err
		}
		code := segs[i]
		if code == "01" {
			v = NormalizeGTIN(v)
		}
		out = append(out, AI{code, v})
	}
	for k, vs := range u.Query() {
		if _, ok := fixedAI[k]; ok {
			out = append(out, AI{k, vs[0]})
		} else if _, ok := varAI[k]; ok {
			out = append(out, AI{k, vs[0]})
		}
	}
	return out, nil
}

// --- EPC pure identity URNs (Tag Data Standard) ---

// SGTINURN builds urn:epc:id:sgtin:CompanyPrefix.IndicatorItemRef.Serial.
func SGTINURN(gtin, serial string, cpLen int) (string, error) {
	if !ValidGTIN(gtin) {
		return "", errors.New("invalid GTIN")
	}
	if cpLen < 6 || cpLen > 12 {
		return "", errors.New("company prefix length must be 6-12")
	}
	indicator := gtin[0:1]
	cp := gtin[1 : 1+cpLen]
	item := gtin[1+cpLen : 13]
	return fmt.Sprintf("urn:epc:id:sgtin:%s.%s%s.%s", cp, indicator, item, escapeURN(serial)), nil
}

// ParseSGTINURN returns GTIN-14 and serial from an SGTIN URN.
func ParseSGTINURN(urn string) (gtin, serial string, cpLen int, err error) {
	rest, ok := strings.CutPrefix(urn, "urn:epc:id:sgtin:")
	if !ok {
		return "", "", 0, errors.New("not an sgtin urn")
	}
	parts := strings.SplitN(rest, ".", 3)
	if len(parts) != 3 || len(parts[1]) < 1 {
		return "", "", 0, errors.New("malformed sgtin urn")
	}
	cp, ir := parts[0], parts[1]
	body := ir[:1] + cp + ir[1:]
	if len(body) != 13 {
		return "", "", 0, errors.New("sgtin urn has wrong digit count")
	}
	return WithCheckDigit(body), unescapeURN(parts[2]), len(cp), nil
}

// LGTINURN builds a class-level urn:epc:class:lgtin for a lot.
func LGTINURN(gtin, lot string, cpLen int) (string, error) {
	s, err := SGTINURN(gtin, lot, cpLen)
	if err != nil {
		return "", err
	}
	return strings.Replace(s, "urn:epc:id:sgtin:", "urn:epc:class:lgtin:", 1), nil
}

// SSCCURN builds urn:epc:id:sscc:CompanyPrefix.ExtSerial.
func SSCCURN(sscc string, cpLen int) (string, error) {
	if !ValidSSCC(sscc) {
		return "", errors.New("invalid SSCC")
	}
	return fmt.Sprintf("urn:epc:id:sscc:%s.%s%s", sscc[1:1+cpLen], sscc[0:1], sscc[1+cpLen:17]), nil
}

// ParseSSCCURN returns the 18-digit SSCC.
func ParseSSCCURN(urn string) (string, error) {
	rest, ok := strings.CutPrefix(urn, "urn:epc:id:sscc:")
	if !ok {
		return "", errors.New("not an sscc urn")
	}
	cp, ser, ok := strings.Cut(rest, ".")
	if !ok || len(ser) < 1 {
		return "", errors.New("malformed sscc urn")
	}
	return WithCheckDigit(ser[:1] + cp + ser[1:]), nil
}

// SGLNURN builds urn:epc:id:sgln:CompanyPrefix.LocationRef.Extension.
func SGLNURN(gln string, cpLen int, ext string) (string, error) {
	if !ValidGLN(gln) {
		return "", errors.New("invalid GLN")
	}
	if ext == "" {
		ext = "0"
	}
	return fmt.Sprintf("urn:epc:id:sgln:%s.%s.%s", gln[:cpLen], gln[cpLen:12], ext), nil
}

// ParseSGLNURN returns the GLN from an SGLN URN.
func ParseSGLNURN(urn string) (string, error) {
	rest, ok := strings.CutPrefix(urn, "urn:epc:id:sgln:")
	if !ok {
		return "", errors.New("not an sgln urn")
	}
	parts := strings.Split(rest, ".")
	if len(parts) < 2 {
		return "", errors.New("malformed sgln urn")
	}
	return WithCheckDigit(parts[0] + parts[1]), nil
}

// URNToDigitalLink converts an EPC URN (sgtin, sscc, sgln, lgtin) to a GS1 Digital Link URI.
func URNToDigitalLink(urn, domain string) (string, error) {
	if domain == "" {
		domain = DefaultResolver
	}
	switch {
	case strings.HasPrefix(urn, "urn:epc:id:sgtin:"):
		g, s, _, err := ParseSGTINURN(urn)
		if err != nil {
			return "", err
		}
		return DigitalLink(domain, Pack{GTIN: g, Serial: s}), nil
	case strings.HasPrefix(urn, "urn:epc:class:lgtin:"):
		g, l, _, err := ParseSGTINURN(strings.Replace(urn, "urn:epc:class:lgtin:", "urn:epc:id:sgtin:", 1))
		if err != nil {
			return "", err
		}
		return DigitalLink(domain, Pack{GTIN: g, Lot: l}), nil
	case strings.HasPrefix(urn, "urn:epc:id:sscc:"):
		s, err := ParseSSCCURN(urn)
		if err != nil {
			return "", err
		}
		return strings.TrimSuffix(domain, "/") + "/00/" + s, nil
	case strings.HasPrefix(urn, "urn:epc:id:sgln:"):
		g, err := ParseSGLNURN(urn)
		if err != nil {
			return "", err
		}
		return strings.TrimSuffix(domain, "/") + "/414/" + g, nil
	}
	return "", fmt.Errorf("unsupported urn scheme: %s", truncate(urn, 24))
}

// DigitalLinkToURN converts a Digital Link to an EPC URN; cpLen is the GS1 company prefix length.
func DigitalLinkToURN(dl string, cpLen int) (string, error) {
	ais, err := ParseDigitalLink(dl)
	if err != nil {
		return "", err
	}
	m := map[string]string{}
	for _, a := range ais {
		m[a.Code] = a.Value
	}
	switch {
	case m["01"] != "" && m["21"] != "":
		return SGTINURN(m["01"], m["21"], cpLen)
	case m["01"] != "" && m["10"] != "":
		return LGTINURN(m["01"], m["10"], cpLen)
	case m["00"] != "":
		return SSCCURN(m["00"], cpLen)
	case m["414"] != "":
		return SGLNURN(m["414"], cpLen, m["254"])
	}
	return "", errors.New("digital link has no serialized identifier")
}

// IsDigitalLink reports whether s looks like an http(s) GS1 Digital Link.
func IsDigitalLink(s string) bool {
	return (strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")) && (strings.Contains(s, "/01/") || strings.Contains(s, "/00/") || strings.Contains(s, "/414/"))
}

// escapeURN percent-encodes characters not allowed in EPC URN serials (TDS GS3A3 component).
func escapeURN(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9',
			strings.ContainsRune("-_.:;=!*'()+,", r):
			b.WriteRune(r)
		default:
			fmt.Fprintf(&b, "%%%02X", r)
		}
	}
	return b.String()
}

func unescapeURN(s string) string {
	if v, err := url.PathUnescape(s); err == nil {
		return v
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
