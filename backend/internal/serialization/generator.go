package serialization

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"runtime"
	"strings"
	"sync"

	"github.com/capitalone/fpe/ff1"
)

// DefaultAlphabet excludes I and O to avoid confusion with 1 and 0 on printed packs.
const DefaultAlphabet = "0123456789ABCDEFGHJKLMNPQRSTUVWXYZ"

const bigDigits = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

// Generator maps pool counters to serial numbers.
//
// Random mode applies NIST SP 800-38G FF1 format-preserving encryption to the
// counter, written in the pool alphabet's radix. FF1 is a permutation of the
// domain alphabet^length, so distinct counters always give distinct serials:
// uniqueness holds by construction, with no lookup of previously issued serials.
// Sequential mode writes the counter itself in the alphabet.
type Generator struct {
	Mode     string
	Alphabet string
	Length   int
	Prefix   string
	key      []byte
	tweak    []byte
	index    [256]int8
	pool     sync.Pool
}

// NewGenerator validates the parameters and prepares the cipher.
func NewGenerator(mode, alphabet string, length int, prefix string, key, tweak []byte) (*Generator, error) {
	if alphabet == "" {
		alphabet = DefaultAlphabet
	}
	radix := len(alphabet)
	if radix < 2 || radix > 62 {
		return nil, errors.New("alphabet must have 2-62 distinct ASCII characters")
	}
	g := &Generator{Mode: mode, Alphabet: alphabet, Length: length, Prefix: prefix, key: key, tweak: tweak}
	for i := range g.index {
		g.index[i] = -1
	}
	for i := 0; i < radix; i++ {
		c := alphabet[i]
		if c > 127 || g.index[c] != -1 {
			return nil, errors.New("alphabet must have distinct ASCII characters")
		}
		g.index[c] = int8(i)
	}
	switch mode {
	case "random":
		if math.Pow(float64(radix), float64(length)) < 1e6 {
			return nil, errors.New("random pools need alphabet^length ≥ 1,000,000 (NIST FF1 domain minimum)")
		}
		if len(key) != 16 && len(key) != 24 && len(key) != 32 {
			return nil, errors.New("FF1 key must be 16, 24 or 32 bytes")
		}
		if _, err := ff1.NewCipher(radix, len(tweak), key, tweak); err != nil {
			return nil, err
		}
		g.pool.New = func() any {
			c, _ := ff1.NewCipher(radix, len(g.tweak), g.key, g.tweak)
			return &c
		}
	case "sequential":
	default:
		return nil, errors.New("mode must be random or sequential")
	}
	if len(prefix)+length > 20 {
		return nil, errors.New("prefix + length must be ≤ 20 characters (GS1 AI 21)")
	}
	return g, nil
}

// Capacity is the number of distinct serials (capped at 2^62).
func (g *Generator) Capacity() int64 {
	c := math.Pow(float64(len(g.Alphabet)), float64(g.Length))
	if c > float64(int64(1)<<62) {
		return int64(1) << 62
	}
	return int64(c)
}

func (g *Generator) toRadix(n int64) string {
	s := big.NewInt(n).Text(len(g.Alphabet))
	if len(s) < g.Length {
		s = strings.Repeat("0", g.Length-len(s)) + s
	}
	return s
}

func (g *Generator) mapOut(digits string) string {
	b := make([]byte, 0, len(g.Prefix)+len(digits))
	b = append(b, g.Prefix...)
	for i := 0; i < len(digits); i++ {
		b = append(b, g.Alphabet[strings.IndexByte(bigDigits, digits[i])])
	}
	return string(b)
}

func (g *Generator) mapIn(serial string) (string, error) {
	s, ok := strings.CutPrefix(serial, g.Prefix)
	if !ok || len(s) != g.Length {
		return "", errors.New("serial does not belong to this pool")
	}
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		v := g.index[s[i]]
		if v < 0 {
			return "", errors.New("serial contains characters outside the pool alphabet")
		}
		b[i] = bigDigits[v]
	}
	return string(b), nil
}

// Serial returns the serial for one counter.
func (g *Generator) Serial(counter int64) (string, error) {
	digits := g.toRadix(counter)
	if g.Mode == "random" {
		c := g.pool.Get().(*ff1.Cipher)
		defer g.pool.Put(c)
		enc, err := c.Encrypt(digits)
		if err != nil {
			return "", err
		}
		digits = enc
	}
	return g.mapOut(digits), nil
}

// Counter reverses Serial (decrypts in random mode).
func (g *Generator) Counter(serial string) (int64, error) {
	digits, err := g.mapIn(serial)
	if err != nil {
		return 0, err
	}
	if g.Mode == "random" {
		c := g.pool.Get().(*ff1.Cipher)
		defer g.pool.Put(c)
		if digits, err = c.Decrypt(digits); err != nil {
			return 0, err
		}
	}
	n, ok := new(big.Int).SetString(digits, len(g.Alphabet))
	if !ok || !n.IsInt64() {
		return 0, fmt.Errorf("serial out of range")
	}
	return n.Int64(), nil
}

// Range generates serials for counters [start, start+count) using all CPUs.
func (g *Generator) Range(start int64, count int) ([]string, error) {
	out := make([]string, count)
	workers := runtime.GOMAXPROCS(0)
	if count < 2048 || workers == 1 {
		for i := 0; i < count; i++ {
			s, err := g.Serial(start + int64(i))
			if err != nil {
				return nil, err
			}
			out[i] = s
		}
		return out, nil
	}
	var wg sync.WaitGroup
	errs := make([]error, workers)
	chunk := (count + workers - 1) / workers
	for w := 0; w < workers; w++ {
		lo, hi := w*chunk, min((w+1)*chunk, count)
		if lo >= hi {
			break
		}
		wg.Add(1)
		go func(w, lo, hi int) {
			defer wg.Done()
			for i := lo; i < hi; i++ {
				s, err := g.Serial(start + int64(i))
				if err != nil {
					errs[w] = err
					return
				}
				out[i] = s
			}
		}(w, lo, hi)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
