package link

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Canonicalize returns the JSON Canonicalization Scheme (RFC 8785) form of a JSON text:
// no insignificant whitespace, object members sorted by the UTF-16 code units of their names,
// strings escaped as ECMAScript JSON.stringify does, and numbers serialised as ECMAScript
// Number.prototype.toString does for IEEE 754 doubles.
func Canonicalize(in []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(in))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("jcs: trailing data")
	}
	var b bytes.Buffer
	if err := jcsWrite(&b, v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// CanonicalizeValue marshals v to JSON and canonicalizes it.
func CanonicalizeValue(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return Canonicalize(raw)
}

func jcsWrite(b *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case json.Number:
		f, err := strconv.ParseFloat(x.String(), 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return fmt.Errorf("jcs: bad number %s", x)
		}
		s, err := esNumber(f)
		if err != nil {
			return err
		}
		b.WriteString(s)
	case string:
		jcsString(b, x)
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := jcsWrite(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return utf16Less(keys[i], keys[j]) })
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			jcsString(b, k)
			b.WriteByte(':')
			if err := jcsWrite(b, x[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("jcs: unsupported type %T", v)
	}
	return nil
}

func utf16Less(a, b string) bool {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

func jcsString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else if r == utf8.RuneError {
				b.WriteString(`�`)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

// esNumber formats f as ECMAScript Number.prototype.toString (radix 10).
func esNumber(f float64) (string, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", errors.New("jcs: NaN and Infinity are not valid JSON")
	}
	if f == 0 {
		return "0", nil
	}
	sign := ""
	if f < 0 {
		sign, f = "-", -f
	}
	// Shortest round-trip digits and exponent: d.ddddde±x.
	e := strconv.FormatFloat(f, 'e', -1, 64)
	mant, exp, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	x, _ := strconv.Atoi(exp)
	k, n := len(digits), x+1
	switch {
	case k <= n && n <= 21:
		return sign + digits + strings.Repeat("0", n-k), nil
	case 0 < n && n <= 21:
		return sign + digits[:n] + "." + digits[n:], nil
	case -6 < n && n <= 0:
		return sign + "0." + strings.Repeat("0", -n) + digits, nil
	}
	es := "+"
	if n-1 < 0 {
		es = "-"
	}
	m := digits[:1]
	if k > 1 {
		m += "." + digits[1:]
	}
	return sign + m + "e" + es + strconv.Itoa(abs(n-1)), nil
}

func abs(i int) int {
	if i < 0 {
		return -i
	}
	return i
}

const b58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// base58btc encodes b with the Bitcoin alphabet.
func base58btc(b []byte) string {
	zeros := 0
	for zeros < len(b) && b[zeros] == 0 {
		zeros++
	}
	n := new(big.Int).SetBytes(b)
	base, mod := big.NewInt(58), new(big.Int)
	var out []byte
	for n.Sign() > 0 {
		n.DivMod(n, base, mod)
		out = append(out, b58Alphabet[mod.Int64()])
	}
	for i := 0; i < zeros; i++ {
		out = append(out, '1')
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

// unbase58btc decodes a base58btc string.
func unbase58btc(s string) ([]byte, error) {
	n := new(big.Int)
	base := big.NewInt(58)
	zeros := 0
	for zeros < len(s) && s[zeros] == '1' {
		zeros++
	}
	for _, c := range []byte(s) {
		i := strings.IndexByte(b58Alphabet, c)
		if i < 0 {
			return nil, fmt.Errorf("base58: invalid character %q", c)
		}
		n.Mul(n, base)
		n.Add(n, big.NewInt(int64(i)))
	}
	return append(make([]byte, zeros), n.Bytes()...), nil
}

// ed25519PubMulticodec is the multicodec prefix (varint 0xed) of an Ed25519 public key.
var ed25519PubMulticodec = []byte{0xed, 0x01}

// multikeyEd25519 encodes an Ed25519 public key as a Multikey publicKeyMultibase value.
func multikeyEd25519(pub []byte) string {
	return "z" + base58btc(append(append([]byte{}, ed25519PubMulticodec...), pub...))
}

// parseMultikeyEd25519 decodes a Multikey publicKeyMultibase Ed25519 key.
func parseMultikeyEd25519(s string) ([]byte, error) {
	if !strings.HasPrefix(s, "z") {
		return nil, errors.New("publicKeyMultibase must be base58btc (z)")
	}
	b, err := unbase58btc(s[1:])
	if err != nil {
		return nil, err
	}
	if len(b) != 34 || !bytes.Equal(b[:2], ed25519PubMulticodec) {
		return nil, errors.New("publicKeyMultibase is not an Ed25519 key")
	}
	return b[2:], nil
}
