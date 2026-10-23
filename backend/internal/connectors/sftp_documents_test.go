package connectors_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	connector "github.com/openspoor/connectors-sdk"
	"github.com/openspoor/openspoor/internal/connectors"
	"github.com/openspoor/openspoor/internal/connectors/mockhub"
	"github.com/openspoor/openspoor/internal/connectors/sftpdrop"
	"github.com/openspoor/openspoor/internal/core/auth"
	"github.com/openspoor/openspoor/internal/testkit"
	"github.com/openspoor/openspoor/pkg/edi"
)

type sentDoc struct {
	ID, Status string
	Delivery   map[string]any
}

// TestSFTPOutboundB2BDocuments drops B2B documents (not only EPCIS) as EDI files on a partner's
// SFTP server: the format follows the document type, a resend is idempotent, a type without a
// writer or a failed upload is reported and the document stays unsent, and everything is audited.
func TestSFTPOutboundB2BDocuments(t *testing.T) {
	e := testkit.New(t)
	srv, err := mockhub.NewSFTPServer("partner", "pw", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	var partner, plain struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", map[string]any{"name": "Globex", "kind": "wholesaler"}), &partner)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", map[string]any{"name": "Initech", "kind": "wholesaler"}), &plain)
	cfg := sftpConfig(srv)

	// Validation of the document settings.
	for name, c := range map[string]map[string]string{
		"no partner":    {"documentDir": "documents"},
		"bad partner":   {"documentDir": "documents", "partnerId": "not-a-uuid"},
		"bad format":    {"documentDir": "documents", "partnerId": partner.ID, "documentFormat": "xml"},
		"parent folder": {"documentDir": "../up", "partnerId": partner.ID},
	} {
		e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/connectors/instances", map[string]any{"connector": "sftp", "name": name, "config": merge(cfg, c)}), nil)
	}
	var inst struct {
		ID     string
		Config map[string]string
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/connectors/instances", map[string]any{"connector": "sftp", "name": "Globex drop",
		"config": merge(cfg, map[string]string{"documentDir": "documents", "partnerId": partner.ID})}), &inst)

	newDoc := func(partnerID string, payload map[string]any) string {
		var d struct{ ID string }
		e.Must(http.StatusCreated, e.Do("POST", "/api/v1/b2b/documents", map[string]any{"partnerId": partnerID, "payload": payload}), &d)
		return d.ID
	}
	order := map[string]any{"type": "order", "number": "PO-501", "buyer": map[string]any{"name": "Acme", "gln": "9506000000008"}, "seller": map[string]any{"name": "Globex", "gln": "0614141000005"},
		"lines": []map[string]any{{"gtin": "09506000134352", "quantity": 12, "uom": "EA", "unitPrice": 2.5}}}
	rfq := map[string]any{"type": "rfq", "number": "RFQ-9", "buyer": map[string]any{"name": "Acme", "gln": "9506000000008"}, "seller": map[string]any{"name": "Globex", "gln": "0614141000005"},
		"lines": []map[string]any{{"gtin": "09506000134352", "quantity": 100, "uom": "EA"}}}
	ep, _ := sftpdrop.EndpointFromConfig(connector.Config(cfg))
	sess, err := sftpdrop.Dial(context.Background(), ep)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// An order goes out as X12.
	oid := newDoc(partner.ID, order)
	var sent sentDoc
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/documents/"+oid+"/send", nil), &sent)
	file, _ := sent.Delivery["file"].(string)
	if sent.Status != "sent" || sent.Delivery["channel"] != "sftp" || sent.Delivery["format"] != "x12" || !strings.HasPrefix(file, "/documents/openspoor-order-PO-501-") || !strings.HasSuffix(file, ".x12") {
		t.Fatalf("order delivery: %+v", sent)
	}
	b, err := sess.Read(file, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	back, err := edi.ParseX12(string(b))
	if err != nil || back.Number != "PO-501" || len(back.Lines) != 1 || back.Lines[0].Quantity != 12 {
		t.Fatalf("dropped X12: %v %+v\n%s", err, back, b)
	}

	// A request for quotation has only an EDIFACT writer.
	qid := newDoc(partner.ID, rfq)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/documents/"+qid+"/send", nil), &sent)
	qfile, _ := sent.Delivery["file"].(string)
	if sent.Delivery["format"] != "edifact" || !strings.HasSuffix(qfile, ".edi") {
		t.Fatalf("rfq delivery: %+v", sent)
	}
	qb, _ := sess.Read(qfile, 1<<20)
	if d, err := edi.ParseEDIFACT(string(qb)); err != nil || d.Type != "rfq" || d.Number != "RFQ-9" {
		t.Fatalf("dropped EDIFACT: %v %+v", err, d)
	}

	// Resending the same document is idempotent (same file); a corrected document is a new file.
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/documents/"+oid+"/send", nil), &sent)
	if sent.Delivery["file"] != file {
		t.Errorf("resend wrote %v, want %v", sent.Delivery["file"], file)
	}
	order["lines"] = []map[string]any{{"gtin": "09506000134352", "quantity": 13, "uom": "EA", "unitPrice": 2.5}}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/documents/"+newDoc(partner.ID, order)+"/send", nil), &sent)
	if sent.Delivery["file"] == file {
		t.Error("a changed document must not overwrite the delivered file")
	}
	if files, _ := sess.List("documents"); len(files) != 3 {
		t.Errorf("document folder: %+v", files)
	}

	// A configured format is honoured; a type without a writer in it is a 400 and stays a draft.
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/connectors/instances/"+inst.ID, map[string]any{"config": merge(inst.Config, map[string]string{"documentFormat": "x12"})}), nil)
	var doc sentDoc
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/b2b/documents/"+qid+"/send", nil), nil) // RFQ has no X12 writer
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/connectors/instances/"+inst.ID, map[string]any{"config": merge(inst.Config, map[string]string{"documentFormat": "auto"})}), nil)
	gro := newDoc(partner.ID, map[string]any{"type": "grocery_order", "number": "G-1", "lines": []map[string]any{{"gtin": "09506000134352", "quantity": 1}}})
	r := e.Do("POST", "/api/v1/b2b/documents/"+gro+"/send", nil)
	if r.Status != http.StatusBadRequest || !strings.Contains(string(r.Body), "has no EDIFACT writer") {
		t.Fatalf("catalogued type: %d %s", r.Status, r.Body)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/b2b/documents/"+gro, nil), &doc)
	if doc.Status != "draft" {
		t.Errorf("a failed drop must not mark the document sent: %+v", doc)
	}

	// A partner without an SFTP document folder keeps the EDI pickup channel.
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/documents/"+newDoc(plain.ID, order)+"/send", nil), &sent)
	if sent.Delivery["channel"] != "edi-pickup" {
		t.Errorf("unbound partner: %+v", sent.Delivery)
	}
	// So does a partner whose instance is disabled.
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/connectors/instances/"+inst.ID, map[string]any{"enabled": false}), nil)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/b2b/documents/"+oid+"/send", nil), &sent)
	if sent.Delivery["channel"] != "edi-pickup" {
		t.Errorf("disabled instance: %+v", sent.Delivery)
	}
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/connectors/instances/"+inst.ID, map[string]any{"enabled": true}), nil)

	// A server presenting another host key: 502, logged, alerted, document unchanged.
	other, _ := mockhub.NewSFTPServer("partner", "pw", nil)
	defer other.Close()
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/connectors/instances/"+inst.ID, map[string]any{"config": merge(inst.Config, map[string]string{"hostKeyFingerprint": other.Fingerprint})}), nil)
	fresh := newDoc(partner.ID, map[string]any{"type": "order", "number": "PO-502", "buyer": order["buyer"], "seller": order["seller"], "lines": order["lines"]})
	r = e.Do("POST", "/api/v1/b2b/documents/"+fresh+"/send", nil)
	if r.Status != http.StatusBadGateway || !strings.Contains(string(r.Body), "SFTP delivery failed") {
		t.Fatalf("host key mismatch: %d %s", r.Status, r.Body)
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/b2b/documents/"+fresh, nil), &doc)
	if doc.Status != "draft" {
		t.Errorf("document after a failed drop: %+v", doc)
	}
	var failedAlerts, connAlerts int
	for _, a := range alerts(t, e, "integration.sftp") {
		if strings.Contains(a.Title, "outbound document") {
			failedAlerts++
		}
		if strings.Contains(a.Title, "connection failed") {
			connAlerts++
		}
	}
	if failedAlerts != 1 || connAlerts != 1 {
		t.Errorf("alerts: %d outbound, %d connection", failedAlerts, connAlerts)
	}

	// Log and audit.
	var files struct{ Items []connectors.SFTPFile }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/connectors/instances/"+inst.ID+"/files", nil), &files)
	var out, outFailed int
	for _, f := range files.Items {
		if f.Direction == "outbound" && f.Status == "processed" && f.SHA256 != "" && f.Size > 0 {
			out++
		}
		if f.Direction == "outbound" && f.Status == "failed" && f.Error != "" {
			outFailed++
		}
	}
	if out != 4 || outFailed != 1 {
		t.Errorf("sftp_file log: %d delivered, %d failed in %+v", out, outFailed, files.Items)
	}
	var aud struct{ Items []struct{ Action string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?limit=200", nil), &aud)
	actions := map[string]int{}
	for _, a := range aud.Items {
		actions[a.Action]++
	}
	if actions["sftp.outbound.processed"] != 4 || actions["sftp.outbound.failed"] != 1 || actions["b2b.send"] < 5 {
		t.Errorf("audit: %+v", actions)
	}

	// A viewer cannot send documents; another tenant's partner id never matches this instance.
	viewer := login(t, e, "viewer@acme.test", "Viewer")
	e.Must(http.StatusForbidden, e.Call(viewer, "POST", "/api/v1/b2b/documents/"+oid+"/send", nil), nil)
	ten, _ := e.NewTenant("globex", "admin@globex.test")
	ctx := auth.With(context.Background(), auth.System(ten.ID))
	if _, ok, err := findConnectors(e).DropDocument(ctx, uuid.MustParse(partner.ID), edi.Document{Type: "order", Number: "X"}); ok || err != nil {
		t.Errorf("another tenant: dropped=%v err=%v", ok, err)
	}
}
