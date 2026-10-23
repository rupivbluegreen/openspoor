package act_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/openspoor/openspoor/internal/platform"
	"github.com/openspoor/openspoor/internal/testkit"
)

// textPDF builds a small valid PDF with a text layer (built-in font, no OCR needed).
func textPDF(pages ...string) []byte {
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

type docMeta struct {
	ID, Name, Format string
	Pages, Passages  int
	Truncated        bool
	Warnings         []string
}

type hit struct {
	DocumentID, Document, Ref, Text string
	Seq                             int
}

func TestDocumentsUploadSearchDelete(t *testing.T) {
	e := testkit.New(t)
	viewer := login(t, e, "viewer@acme.test", "Viewer")
	ops := login(t, e, "ops@acme.test", "Ops")
	up := func(tok string, body map[string]any) testkit.Response { return e.Call(tok, "POST", "/api/v1/documents", body) }

	// Validation, roles and limits.
	e.Must(http.StatusForbidden, up(viewer, map[string]any{"name": "a.txt", "content": "text"}), nil)
	e.Must(http.StatusBadRequest, up(e.Token, map[string]any{"name": "", "content": "text"}), nil)
	e.Must(http.StatusBadRequest, up(e.Token, map[string]any{"name": strings.Repeat("n", 201), "content": "text"}), nil)
	e.Must(http.StatusBadRequest, up(e.Token, map[string]any{"name": "a.txt"}), nil)
	e.Must(http.StatusBadRequest, up(e.Token, map[string]any{"name": "a.txt", "contentBase64": "***"}), nil)
	e.Must(http.StatusBadRequest, up(e.Token, map[string]any{"name": "a.docx", "content": "PK"}), nil)
	e.Must(http.StatusBadRequest, up(e.Token, map[string]any{"name": "scan.pdf", "contentBase64": base64.StdEncoding.EncodeToString(textPDF(""))}), nil)
	e.Must(http.StatusBadRequest, up(e.Token, map[string]any{"name": "broken.pdf", "content": "%PDF-1.4 nothing"}), nil)
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/documents", "{"), nil)
	big := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("a"), 5<<20+1))
	e.Must(http.StatusRequestEntityTooLarge, up(e.Token, map[string]any{"name": "big.txt", "contentBase64": big}), nil)

	// Every supported format.
	var sop, csvDoc, xmlDoc, htmlDoc, pdfDoc docMeta
	e.Must(http.StatusCreated, up(ops, map[string]any{"name": "sop-quarantine.txt", "content": "Batch release procedure.\n\nProduct under investigation is placed in quarantine until the QP signs the release.\nQuarantine location: cage 4."}), &sop)
	e.Must(http.StatusCreated, up(ops, map[string]any{"name": "suppliers.csv", "mediaType": "text/csv", "contentBase64": base64.StdEncoding.EncodeToString([]byte("supplier;country\nAcme Pharma;NL\nDelta Foods;SA\n"))}), &csvDoc)
	e.Must(http.StatusCreated, up(ops, map[string]any{"name": "cert.xml", "content": `<cert><issuer>Halal Authority</issuer><scope>Poultry</scope></cert>`}), &xmlDoc)
	e.Must(http.StatusCreated, up(ops, map[string]any{"name": "notice.html", "content": `<html><body><h1>Recall notice</h1><p>Lot A17 is withdrawn.</p><script>secretToken()</script></body></html>`}), &htmlDoc)
	e.Must(http.StatusCreated, up(ops, map[string]any{"name": "spec.pdf", "contentBase64": base64.StdEncoding.EncodeToString(textPDF("Storage temperature 2 to 8 degrees", "Shelf life eighteen months"))}), &pdfDoc)
	if sop.Format != "text" || csvDoc.Format != "csv" || xmlDoc.Format != "xml" || htmlDoc.Format != "html" || pdfDoc.Format != "pdf" || pdfDoc.Pages != 2 || sop.Passages != 1 {
		t.Fatalf("formats: %+v %+v %+v %+v %+v", sop, csvDoc, xmlDoc, htmlDoc, pdfDoc)
	}
	if auditCount(t, e, "document.upload") != 5 {
		t.Fatal("uploads must be audited")
	}
	// The same file twice is refused.
	e.Must(http.StatusConflict, up(e.Token, map[string]any{"name": "copy.xml", "content": `<cert><issuer>Halal Authority</issuer><scope>Poultry</scope></cert>`}), nil)

	search := func(tok, q string, want int) []hit {
		t.Helper()
		var out struct{ Items []hit }
		e.Must(http.StatusOK, e.Call(tok, "GET", "/api/v1/documents/search?q="+url.QueryEscape(q), nil), &out)
		if len(out.Items) != want {
			t.Fatalf("search %q: %d hits, want %d: %+v", q, len(out.Items), want, out.Items)
		}
		return out.Items
	}
	h := search(viewer, "quarantine", 1)[0] // Viewer may read
	if h.DocumentID != sop.ID || h.Document != "sop-quarantine.txt" || h.Ref != "document/"+sop.ID+"#0" || !strings.Contains(h.Text, "[[quarantine]]") {
		t.Fatalf("hit: %+v", h)
	}
	if search(e.Token, "shelf life", 1)[0].DocumentID != pdfDoc.ID || search(e.Token, "Delta", 1)[0].DocumentID != csvDoc.ID ||
		search(e.Token, "poultry", 1)[0].DocumentID != xmlDoc.ID || search(e.Token, "withdrawn", 1)[0].DocumentID != htmlDoc.ID {
		t.Fatal("format-specific hits")
	}
	search(e.Token, "secretToken", 0) // scripts are not indexed
	search(e.Token, `"release procedure"`, 1)
	search(e.Token, `"release procedure" -cage`, 0)
	search(e.Token, `quarantine -cage`, 0)
	search(e.Token, "nothingmatchesthis", 0)
	e.Must(http.StatusBadRequest, e.Do("GET", "/api/v1/documents/search?q=a", nil), nil)
	e.Must(http.StatusBadRequest, e.Do("GET", "/api/v1/documents/search", nil), nil)
	// Query text is a bound parameter: operators and quotes cannot break out.
	search(e.Token, "'; drop table document; --", 0)
	var list struct {
		Items []docMeta
		Total int
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/documents", nil), &list)
	if list.Total != 5 {
		t.Fatalf("list after injection attempt: %+v", list)
	}

	// Read one document with its passages.
	var one struct {
		Document docMeta
		Passages []hit
	}
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/documents/"+pdfDoc.ID, nil), &one)
	if one.Document.Name != "spec.pdf" || len(one.Passages) != 1 || !strings.Contains(one.Passages[0].Text, "Storage temperature") {
		t.Fatalf("document: %+v", one)
	}
	e.Must(http.StatusNotFound, e.Do("GET", "/api/v1/documents/00000000-0000-0000-0000-000000000000", nil), nil)
	e.Must(http.StatusBadRequest, e.Do("GET", "/api/v1/documents/not-a-uuid", nil), nil)

	// Tenant isolation.
	if _, _, err := e.App.Platform.CreateTenant(context.Background(), platform.NewTenant{Slug: "globex", Name: "Globex", AdminEmail: "admin@globex.test", AdminPassword: testkit.Password}); err != nil {
		t.Fatal(err)
	}
	var gt struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "globex", "email": "admin@globex.test", "password": testkit.Password}), &gt)
	search(gt.Token, "quarantine", 0)
	e.Must(http.StatusOK, e.Call(gt.Token, "GET", "/api/v1/documents", nil), &list)
	if list.Total != 0 {
		t.Fatalf("other tenant sees documents: %+v", list)
	}
	e.Must(http.StatusNotFound, e.Call(gt.Token, "GET", "/api/v1/documents/"+sop.ID, nil), nil)
	e.Must(http.StatusNotFound, e.Call(gt.Token, "DELETE", "/api/v1/documents/"+sop.ID, nil), nil)
	// The same file may be uploaded by another tenant (the duplicate check is per tenant).
	e.Must(http.StatusCreated, e.Call(gt.Token, "POST", "/api/v1/documents", map[string]any{"name": "sop.txt", "content": "Batch release procedure.\n\nProduct under investigation is placed in quarantine until the QP signs the release.\nQuarantine location: cage 4."}), nil)
	search(gt.Token, "quarantine", 1)

	// Delete: writers only, audited, passages go with it.
	e.Must(http.StatusForbidden, e.Call(viewer, "DELETE", "/api/v1/documents/"+sop.ID, nil), nil)
	e.Must(http.StatusNoContent, e.Call(ops, "DELETE", "/api/v1/documents/"+sop.ID, nil), nil)
	e.Must(http.StatusNotFound, e.Do("DELETE", "/api/v1/documents/"+sop.ID, nil), nil)
	search(e.Token, "quarantine", 0)
	if auditCount(t, e, "document.delete") != 1 {
		t.Fatal("delete must be audited")
	}
}

func TestDocumentTruncationIsReported(t *testing.T) {
	e := testkit.New(t)
	// More text than the passage cap: reported, not silent.
	var sb strings.Builder
	for i := 0; i < 2300; i++ {
		fmt.Fprintf(&sb, "Line %d %s\n\n", i, strings.Repeat("word ", 230))
	}
	var d docMeta
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/documents", map[string]any{"name": "huge.txt", "contentBase64": base64.StdEncoding.EncodeToString([]byte(sb.String()))}), &d)
	if !d.Truncated || len(d.Warnings) == 0 || d.Passages == 0 || d.Passages > 2000 {
		t.Fatalf("huge document: %+v", d)
	}
}
