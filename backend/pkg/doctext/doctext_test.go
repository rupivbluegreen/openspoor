package doctext

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// minimalPDF builds a valid one-page-per-string PDF with a text layer (built-in Helvetica).
func minimalPDF(pages ...string) []byte {
	var buf bytes.Buffer
	var offs []int
	obj := func(s string) {
		offs = append(offs, buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", len(offs), s)
	}
	buf.WriteString("%PDF-1.4\n")
	kids := ""
	for i := range pages {
		kids += fmt.Sprintf("%d 0 R ", 4+2*i)
	}
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, len(pages)))
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	for i, p := range pages {
		stream := fmt.Sprintf("BT /F1 12 Tf 72 700 Td (%s) Tj ET", p)
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", 5+2*i))
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offs)+1)
	for _, o := range offs {
		fmt.Fprintf(&buf, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offs)+1, xref)
	return buf.Bytes()
}

func TestExtractFormats(t *testing.T) {
	cases := []struct {
		name, mt string
		data     []byte
		format   Format
		want     []string
		notWant  []string
	}{
		{"a.txt", "", []byte("\xef\xbb\xbfHello world\r\n\r\n\r\n\r\nSecond  paragraph \n"), Text, []string{"Hello world", "Second  paragraph"}, nil},
		{"a.csv", "", []byte("sku;qty\nA-1;5\n\"B;2\";7\n"), CSV, []string{"sku | qty", "A-1 | 5", "B;2 | 7"}, nil},
		{"tab", "text/csv", []byte("a\tb\n1\t2\n"), CSV, []string{"a | b"}, nil},
		{"a.xml", "", []byte(`<?xml version="1.0"?><!DOCTYPE x [<!ENTITY e SYSTEM "file:///etc/passwd">]><doc><t>Title</t><p a="hidden">Body &e; text</p></doc>`), XML,
			[]string{"Title", "Body"}, []string{"root:", "hidden"}},
		{"page.html", "", []byte(`<html><head><title>T</title><style>.x{}</style></head><body><script>var secret=1</script><h1>Head</h1><p>One</p><p>Two &amp; three</p></body></html>`), HTML,
			[]string{"Head", "One", "Two & three"}, []string{"secret", ".x{}"}},
		{"x.pdf", "", minimalPDF("Batch release record 4711", "Second page text"), PDF, []string{"Batch release record 4711", "Second page text"}, nil},
		{"noext", "application/pdf", minimalPDF("By media type"), PDF, []string{"By media type"}, nil},
	}
	for _, c := range cases {
		r, err := Extract(c.name, c.mt, c.data, DefaultLimits)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if r.Format != c.format {
			t.Errorf("%s: format %s", c.name, r.Format)
		}
		for _, w := range c.want {
			if !strings.Contains(r.Text, w) {
				t.Errorf("%s: missing %q in %q", c.name, w, r.Text)
			}
		}
		for _, w := range c.notWant {
			if strings.Contains(r.Text, w) {
				t.Errorf("%s: unexpected %q in %q", c.name, w, r.Text)
			}
		}
	}
	r, _ := Extract("x.pdf", "", minimalPDF("a", "b", "c"), DefaultLimits)
	if r.Pages != 3 {
		t.Errorf("pages %d", r.Pages)
	}
	// MaxPages bounds the pages read.
	r, err := Extract("x.pdf", "", minimalPDF("first", "second", "third"), Limits{MaxBytes: 1 << 20, MaxPages: 1, MaxChars: 1000})
	if err != nil || strings.Contains(r.Text, "second") || !strings.Contains(r.Text, "first") {
		t.Errorf("max pages: %q %v", r.Text, err)
	}
}

func TestExtractRefusals(t *testing.T) {
	big := bytes.Repeat([]byte("a"), 1000)
	if _, err := Extract("a.txt", "", big, Limits{MaxBytes: 999}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("size: %v", err)
	}
	for name, data := range map[string][]byte{"a.docx": []byte("PK\x03\x04"), "a.bin": []byte{1, 2, 3}, "noext": []byte("hello")} {
		if _, err := Extract(name, "", data, DefaultLimits); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, data := range map[string][]byte{"a.txt": []byte("\xff\xfe\x00bad"), "b.csv": []byte("a,b\n1,\x002"), "c.pdf": []byte("%PDF-1.4 not really")} {
		if _, err := Extract(name, "", data, DefaultLimits); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Empty text, and a PDF without any text (what a scan looks like), are reported, not indexed.
	for name, data := range map[string][]byte{"a.txt": []byte("  \n\n"), "b.html": []byte("<html><script>x</script></html>"), "c.pdf": minimalPDF(""), "d.xml": []byte("<a><b></a>")} {
		if _, err := Extract(name, "", data, DefaultLimits); !errors.Is(err, ErrNoText) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Truncation is reported, never silent, and respects UTF-8.
	r, err := Extract("a.txt", "", []byte(strings.Repeat("é", 100)), Limits{MaxBytes: 1 << 20, MaxChars: 11})
	if err != nil || !r.Truncated || len(r.Warnings) != 1 || !strings.HasPrefix(r.Text, "éé") || len(r.Text)%2 != 0 {
		t.Errorf("truncate: %+v %v", r, err)
	}
	// Deeply nested XML is refused.
	deep := strings.Repeat("<a>", 300) + "x" + strings.Repeat("</a>", 300)
	if _, err := Extract("d.xml", "", []byte(deep), DefaultLimits); !errors.Is(err, ErrMalformed) {
		t.Errorf("deep xml: %v", err)
	}
}

func TestPDFTimeoutAndGarbage(t *testing.T) {
	// A zero timeout falls back to the default; a tiny one reports a timeout without hanging.
	_, _, err := extractPDF(minimalPDF("x"), Limits{Timeout: time.Nanosecond, MaxPages: 5})
	if err != nil && !errors.Is(err, ErrMalformed) {
		t.Errorf("timeout: %v", err)
	}
	for _, garbage := range [][]byte{{}, []byte("%PDF-"), []byte("%PDF-1.7\n1 0 obj<<>>endobj trailer<</Root 1 0 R>>"), bytes.Repeat([]byte{0xff}, 4096)} {
		if _, _, err := extractPDF(garbage, DefaultLimits); err == nil {
			t.Errorf("garbage accepted: %q", garbage[:min(8, len(garbage))])
		}
	}
}

func TestChunks(t *testing.T) {
	if got := Chunks("", 400); len(got) != 0 {
		t.Errorf("empty: %v", got)
	}
	text := "Alpha paragraph.\nBeta paragraph.\n\n" + strings.Repeat("word ", 200) + "\n" + strings.Repeat("é", 500)
	got := Chunks(text, 300)
	if len(got) < 4 {
		t.Fatalf("chunks: %d", len(got))
	}
	var joined string
	for _, c := range got {
		if len(c) > 300 {
			t.Errorf("chunk of %d bytes", len(c))
		}
		if strings.ContainsRune(c, '�') {
			t.Errorf("broken rune in chunk")
		}
		joined += c + " "
	}
	if !strings.Contains(got[0], "Alpha paragraph.\nBeta paragraph.") {
		t.Errorf("short paragraphs should share a chunk: %q", got[0])
	}
	if strings.Count(joined, "word") != 200 || strings.Count(joined, "é") != 500 {
		t.Error("text lost in chunking")
	}
	if got := Chunks("tiny", 5); len(got) != 1 {
		t.Errorf("min size: %v", got)
	}
}
