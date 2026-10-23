// Package doctext extracts plain text from uploaded documents without any external service:
// PDF (text layer only), CSV, XML, HTML and plain text. There is no OCR: a scanned PDF without a
// text layer yields ErrNoText. Every extractor is bounded by Limits, so an untrusted file cannot
// use unbounded memory or time.
//
// TODO(spec): OCR for scanned documents is not built (docs/OPEN_QUESTIONS.md).
package doctext

import (
	"bytes"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
	"golang.org/x/net/html"
)

// Format is a supported input format.
type Format string

// Supported formats.
const (
	PDF  Format = "pdf"
	CSV  Format = "csv"
	XML  Format = "xml"
	HTML Format = "html"
	Text Format = "text"
)

// Errors returned by Extract.
var (
	ErrTooLarge    = errors.New("document is larger than the size limit")
	ErrUnsupported = errors.New("unsupported document format (PDF, CSV, XML, HTML and plain text are supported)")
	ErrNoText      = errors.New("no extractable text; scanned documents need OCR, which is not supported")
	ErrMalformed   = errors.New("document could not be read")
)

// Limits bound the work done for one document.
type Limits struct {
	MaxBytes int           // input size
	MaxChars int           // extracted text (longer text is cut and Result.Truncated is set)
	MaxPages int           // PDF pages read
	Timeout  time.Duration // PDF extraction
}

// DefaultLimits: 5 MiB input, 1 Mi characters of text, 200 PDF pages, 20 s.
var DefaultLimits = Limits{MaxBytes: 5 << 20, MaxChars: 1 << 20, MaxPages: 200, Timeout: 20 * time.Second}

// Result is the extracted text and what was done to get it.
type Result struct {
	Format    Format
	Text      string
	Pages     int // PDF pages (0 for other formats)
	Truncated bool
	Warnings  []string
}

// Detect decides the format from the content, then the media type, then the file extension.
func Detect(name, mediaType string, data []byte) (Format, error) {
	if bytes.HasPrefix(bytes.TrimLeft(data[:min(len(data), 64)], "\xef\xbb\xbf \t\r\n"), []byte("%PDF-")) {
		return PDF, nil
	}
	mt := strings.ToLower(strings.TrimSpace(strings.SplitN(mediaType, ";", 2)[0]))
	switch mt {
	case "application/pdf":
		return PDF, nil
	case "text/csv", "application/csv":
		return CSV, nil
	case "text/xml", "application/xml":
		return XML, nil
	case "text/html", "application/xhtml+xml":
		return HTML, nil
	case "text/plain", "text/markdown":
		return Text, nil
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".pdf":
		return PDF, nil
	case ".csv", ".tsv":
		return CSV, nil
	case ".xml":
		return XML, nil
	case ".html", ".htm", ".xhtml":
		return HTML, nil
	case ".txt", ".text", ".md", ".log":
		return Text, nil
	}
	return "", ErrUnsupported
}

// Extract reads data as the detected format.
func Extract(name, mediaType string, data []byte, lim Limits) (Result, error) {
	if lim.MaxBytes <= 0 {
		lim = DefaultLimits
	}
	if len(data) > lim.MaxBytes {
		return Result{}, ErrTooLarge
	}
	f, err := Detect(name, mediaType, data)
	if err != nil {
		return Result{}, err
	}
	res := Result{Format: f}
	switch f {
	case PDF:
		res.Text, res.Pages, err = extractPDF(data, lim)
	case CSV:
		res.Text, err = extractCSV(data, lim)
	case XML:
		res.Text, err = extractXML(data, lim)
	case HTML:
		res.Text, err = extractHTML(data, lim)
	case Text:
		res.Text, err = extractPlain(data)
	}
	if err != nil {
		return Result{}, err
	}
	res.Text = tidy(res.Text)
	if lim.MaxChars > 0 && len(res.Text) > lim.MaxChars {
		res.Text = cutUTF8(res.Text, lim.MaxChars)
		res.Truncated = true
		res.Warnings = append(res.Warnings, fmt.Sprintf("text cut at %d bytes", lim.MaxChars))
	}
	if strings.TrimSpace(res.Text) == "" {
		return Result{}, ErrNoText
	}
	return res, nil
}

func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// tidy trims trailing spaces per line and collapses runs of blank lines.
func tidy(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, l := range lines {
		l = strings.TrimRight(strings.Map(func(r rune) rune {
			if r == 0 || (r < 32 && r != '\t') || r == 0xfffd {
				return -1
			}
			return r
		}, l), " \t")
		if l == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func extractPlain(data []byte) (string, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return "", fmt.Errorf("%w: not UTF-8 text", ErrMalformed)
	}
	return string(data), nil
}

func extractCSV(data []byte, lim Limits) (string, error) {
	text, err := extractPlain(data)
	if err != nil {
		return "", err
	}
	r := csv.NewReader(strings.NewReader(text))
	r.Comma = sniffDelimiter(text)
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	var b strings.Builder
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("%w: %w", ErrMalformed, err)
		}
		b.WriteString(strings.Join(rec, " | "))
		b.WriteByte('\n')
		if lim.MaxChars > 0 && b.Len() > lim.MaxChars*2 {
			break
		}
	}
	return b.String(), nil
}

// sniffDelimiter picks the most frequent of , ; tab | in the first line.
func sniffDelimiter(s string) rune {
	line, _, _ := strings.Cut(s, "\n")
	best, bestN := ',', 0
	for _, d := range []rune{',', ';', '\t', '|'} {
		if n := strings.Count(line, string(d)); n > bestN {
			best, bestN = d, n
		}
	}
	return best
}

func extractXML(data []byte, lim Limits) (string, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	// Custom entities are not expanded and DTDs are not fetched, so there is no entity-expansion or
	// external-entity risk; unknown entities stay as typed.
	dec.CharsetReader = func(label string, in io.Reader) (io.Reader, error) { return in, nil }
	var b strings.Builder
	depth := 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if b.Len() > 0 {
				break // keep what was read from a truncated or slightly malformed file
			}
			return "", fmt.Errorf("%w: %w", ErrMalformed, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if depth > 200 {
				return "", fmt.Errorf("%w: nesting too deep", ErrMalformed)
			}
			b.WriteByte('\n')
		case xml.EndElement:
			depth--
			b.WriteByte('\n')
		case xml.CharData:
			if s := strings.TrimSpace(string(t)); s != "" {
				b.WriteString(s)
				b.WriteByte(' ')
			}
		}
		if lim.MaxChars > 0 && b.Len() > lim.MaxChars*2 {
			break
		}
	}
	return b.String(), nil
}

var blockTags = map[string]bool{"p": true, "div": true, "br": true, "li": true, "tr": true, "h1": true, "h2": true, "h3": true, "h4": true,
	"h5": true, "h6": true, "section": true, "article": true, "table": true, "ul": true, "ol": true, "pre": true, "blockquote": true, "hr": true,
	"header": true, "footer": true, "main": true, "title": true}

func extractHTML(data []byte, lim Limits) (string, error) {
	text, err := extractPlain(data)
	if err != nil {
		return "", err
	}
	z := html.NewTokenizer(strings.NewReader(text))
	var b strings.Builder
	skip := 0
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := z.TagName()
			n := string(name)
			if n == "script" || n == "style" || n == "noscript" || n == "template" {
				if tt == html.StartTagToken {
					skip++
				}
			} else if blockTags[n] {
				b.WriteByte('\n')
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			n := string(name)
			if n == "script" || n == "style" || n == "noscript" || n == "template" {
				if skip > 0 {
					skip--
				}
			} else if blockTags[n] {
				b.WriteByte('\n')
			}
		case html.TextToken:
			if skip == 0 {
				b.Write(z.Text())
			}
		}
		if lim.MaxChars > 0 && b.Len() > lim.MaxChars*2 {
			break
		}
	}
	return b.String(), nil
}

func extractPDF(data []byte, lim Limits) (text string, pages int, err error) {
	type out struct {
		text  string
		pages int
		err   error
	}
	ch := make(chan out, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil { // the parser panics on some malformed files
				ch <- out{err: fmt.Errorf("%w: %v", ErrMalformed, p)}
			}
		}()
		r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			ch <- out{err: fmt.Errorf("%w: %w", ErrMalformed, err)}
			return
		}
		n := r.NumPage()
		var b strings.Builder
		for i := 1; i <= min(n, max(lim.MaxPages, 1)); i++ {
			s, err := r.Page(i).GetPlainText(nil)
			if err != nil {
				continue // a page without a readable text layer is skipped
			}
			b.WriteString(s)
			b.WriteString("\n\n")
			if lim.MaxChars > 0 && b.Len() > lim.MaxChars*2 {
				break
			}
		}
		ch <- out{text: b.String(), pages: n}
	}()
	timeout := lim.Timeout
	if timeout <= 0 {
		timeout = DefaultLimits.Timeout
	}
	select {
	case o := <-ch:
		return o.text, o.pages, o.err
	case <-time.After(timeout):
		return "", 0, fmt.Errorf("%w: reading took longer than %s", ErrMalformed, timeout)
	}
}

// Chunks splits text into passages of about size bytes at paragraph, then line, then word
// boundaries. Passages are what the search index holds and what the assistant cites.
func Chunks(text string, size int) []string {
	if size < 200 {
		size = 200
	}
	var out []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}
	for _, para := range strings.Split(text, "\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		for len(para) > size {
			cut := strings.LastIndexByte(para[:size], ' ')
			if cut < size/2 {
				cut = size
				for cut > 0 && !utf8.RuneStart(para[cut]) {
					cut--
				}
			}
			if cur.Len() > 0 {
				flush()
			}
			out = append(out, strings.TrimSpace(para[:cut]))
			para = strings.TrimSpace(para[cut:])
		}
		if cur.Len()+len(para)+1 > size {
			flush()
		}
		if cur.Len() > 0 {
			cur.WriteByte('\n')
		}
		cur.WriteString(para)
	}
	flush()
	return out
}
