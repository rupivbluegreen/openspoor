package dscsa_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/openspoor/openspoor/pkg/as2"
)

// setOptions changes a node's AS2 options for its partner (the certificate stays pinned).
func (p *as2Pair) setOptions(t *testing.T, me *as2Node, body map[string]any) {
	t.Helper()
	p.e.Must(http.StatusOK, p.e.Call(me.token, "PUT", "/api/v1/partners/"+me.partnerID+"/as2/options", body), nil)
}

func (p *as2Pair) sendFails(t *testing.T, po string, epcs []string, want string) {
	t.Helper()
	r := p.send(t, p.shipment(t, po, epcs))
	msg, _ := r.Response["error"].(string)
	if r.Status != "send_failed" || !strings.Contains(msg, want) {
		t.Fatalf("%s: want a failure containing %q, got %+v", po, want, r)
	}
}

func (p *as2Pair) sendOK(t *testing.T, po string, epcs []string) sendResult {
	t.Helper()
	r := p.send(t, p.shipment(t, po, epcs))
	if r.Status != "sent" || r.Response["mdn"] != "processed" {
		t.Fatalf("%s: %+v", po, r)
	}
	return r
}

func lastSend(t *testing.T, p *as2Pair) map[string]any {
	t.Helper()
	var aud struct {
		Items []struct{ After map[string]any }
	}
	p.e.Must(http.StatusOK, p.e.Call(p.acme.token, "GET", "/api/v1/audit?limit=1&action=as2.send", nil), &aud)
	if len(aud.Items) != 1 {
		t.Fatal("no as2.send audit entry")
	}
	return aud.Items[0].After
}

func TestAS2SignatureAlgorithmChoice(t *testing.T) {
	p := newAS2Pair(t)
	// SHA-256 is the default.
	p.sendOK(t, "PO-ALG-0", p.epcs[:1])
	if a := lastSend(t, p); a["digest"] != "sha-256" {
		t.Fatalf("default digest: %+v", a)
	}
	// SHA-384 and SHA-512 are accepted by a default receiver (it requires at least SHA-256).
	for i, d := range []string{"sha-384", "sha-512"} {
		p.setOptions(t, p.acme, map[string]any{"signatureAlgorithm": d})
		p.sendOK(t, "PO-ALG-"+d, p.epcs[i+1:i+2])
		if a := lastSend(t, p); a["digest"] != d {
			t.Fatalf("digest %s: %+v", d, a)
		}
	}
	// A receiver configured for SHA-512 refuses a SHA-256 sender.
	p.setOptions(t, p.globex, map[string]any{"signatureAlgorithm": "sha-512"})
	p.setOptions(t, p.acme, map[string]any{"signatureAlgorithm": "sha-256"})
	p.sendFails(t, "PO-ALG-LOW", p.epcs[3:4], as2.ErrInsufficient)
	// ... and accepts SHA-512.
	p.setOptions(t, p.acme, map[string]any{"signatureAlgorithm": "sha-512"})
	p.sendOK(t, "PO-ALG-512", p.epcs[3:4])
	// Per-message audit and message log show what was used.
	if a := lastSend(t, p); a["digest"] != "sha-512" || a["legacy"] != false {
		t.Fatalf("audit: %+v", a)
	}
	// Without a signature the digest still names the integrity check requested in the receipt.
	p.e.Must(http.StatusOK, p.e.Call(p.acme.token, "PUT", "/api/v1/partners/"+p.acme.partnerID+"/as2", map[string]any{"certPem": as2.CertPEM(p.globex.st.Cert), "sign": false}), nil)
	p.e.Must(http.StatusOK, p.e.Call(p.globex.token, "PUT", "/api/v1/partners/"+p.globex.partnerID+"/as2", map[string]any{"certPem": as2.CertPEM(p.acme.st.Cert), "sign": false}), nil)
	p.sendOK(t, "PO-ALG-UNSIGNED", p.epcs[4:5])
}

func TestAS2LegacySHA1(t *testing.T) {
	p := newAS2Pair(t)
	// SHA-1 cannot be chosen without the explicit legacy flag.
	p.e.Must(http.StatusBadRequest, p.e.Call(p.acme.token, "PUT", "/api/v1/partners/"+p.acme.partnerID+"/as2/options", map[string]any{"signatureAlgorithm": "sha-1"}), nil)
	p.setOptions(t, p.acme, map[string]any{"signatureAlgorithm": "sha-1", "legacy": true})
	// Globex has not flagged Acme legacy: SHA-1 is refused, the shipment fails.
	p.sendFails(t, "PO-SHA1-REFUSED", p.epcs[:1], as2.ErrInsufficient)
	// Once Globex flags Acme legacy (explicitly, audited), SHA-1 signatures are accepted and the
	// receipt, carrying a SHA-1 integrity check, is verified.
	p.setOptions(t, p.globex, map[string]any{"legacy": true})
	if hasAction(t, p.e, p.globex.token, "partner.as2.legacy.set") != 1 || hasAction(t, p.e, p.acme.token, "partner.as2.legacy.set") != 1 {
		t.Fatal("legacy flags not audited")
	}
	p.sendOK(t, "PO-SHA1-OK", p.epcs[1:2])
	if a := lastSend(t, p); a["digest"] != "sha-1" || a["legacy"] != true {
		t.Fatalf("audit: %+v", a)
	}
	// Globex signs its receipts with SHA-1 too: Acme (legacy) accepts them.
	p.setOptions(t, p.globex, map[string]any{"mdnSignatureAlgorithm": "sha-1"})
	p.sendOK(t, "PO-SHA1-MDN", p.epcs[2:3])
	// If Acme drops the legacy flag it is no longer able to use SHA-1 at all.
	p.e.Must(http.StatusBadRequest, p.e.Call(p.acme.token, "PUT", "/api/v1/partners/"+p.acme.partnerID+"/as2/options", map[string]any{"legacy": false}), nil)
	p.setOptions(t, p.acme, map[string]any{"signatureAlgorithm": "sha-256", "legacy": false})
	// A SHA-1 signed receipt from Globex is now refused by Acme (not legacy): the send fails.
	p.sendFails(t, "PO-SHA1-MDN-REFUSED", p.epcs[3:4], as2.ErrInsufficient)
	// The overview carries a warning for each flagged partner.
	var ov struct {
		Partners []struct {
			Name     string
			Warnings []struct{ Code string }
		}
		Summary struct{ Legacy int }
	}
	p.e.Must(http.StatusOK, p.e.Call(p.globex.token, "GET", "/api/v1/as2/certificates", nil), &ov)
	if ov.Summary.Legacy != 1 || len(ov.Partners) != 1 || len(ov.Partners[0].Warnings) == 0 || ov.Partners[0].Warnings[len(ov.Partners[0].Warnings)-1].Code != "legacy" {
		t.Fatalf("overview: %+v", ov)
	}
}

func TestAS2EncryptionChoice(t *testing.T) {
	p := newAS2Pair(t)
	// AES-128 is refused by a receiver that expects the default AES-256 ...
	p.setOptions(t, p.acme, map[string]any{"encryption": "aes-128-cbc"})
	p.sendFails(t, "PO-ENC-LOW", p.epcs[:1], as2.ErrInsufficient)
	// ... and accepted once the receiver allows it.
	p.setOptions(t, p.globex, map[string]any{"encryption": "aes-128-cbc"})
	p.sendOK(t, "PO-ENC-128", p.epcs[1:2])
	if a := lastSend(t, p); a["cipher"] != "aes-128-cbc" {
		t.Fatalf("audit: %+v", a)
	}
	// A receiver allowing AES-128 still accepts AES-256.
	p.setOptions(t, p.acme, map[string]any{"encryption": "aes-256-cbc"})
	p.sendOK(t, "PO-ENC-256", p.epcs[2:3])
}

func TestAS2CompressionChoice(t *testing.T) {
	p := newAS2Pair(t)
	// Off by default: the audit entry says so.
	p.sendOK(t, "PO-CMP-0", p.epcs[:1])
	if a := lastSend(t, p); a["compressed"] != false {
		t.Fatalf("default: %+v", a)
	}
	// A compressed message to a partner that has not enabled compression is refused.
	p.setOptions(t, p.acme, map[string]any{"compress": true})
	p.sendFails(t, "PO-CMP-REFUSED", p.epcs[1:2], as2.ErrUnexpected)
	p.setOptions(t, p.globex, map[string]any{"compress": true})
	p.sendOK(t, "PO-CMP-1", p.epcs[1:2])
	if a := lastSend(t, p); a["compressed"] != true {
		t.Fatalf("audit: %+v", a)
	}
	if got := p.shipments(t, p.globex.token, "inbound"); len(got) != 2 {
		t.Fatalf("inbound: %+v", got)
	}
	// Compression without signing or encryption is not available.
	p.e.Must(http.StatusOK, p.e.Call(p.acme.token, "PUT", "/api/v1/partners/"+p.acme.partnerID+"/as2", map[string]any{"certPem": as2.CertPEM(p.globex.st.Cert), "sign": false, "encrypt": false}), nil)
	p.sendFails(t, "PO-CMP-BARE", p.epcs[2:3], "compression needs")
}

func TestAS2ReceiptOptions(t *testing.T) {
	p := newAS2Pair(t)
	// Sync receipts signed with SHA-384: the integrity check follows the sender's digest.
	p.setOptions(t, p.globex, map[string]any{"mdnSignatureAlgorithm": "sha-384"})
	p.setOptions(t, p.acme, map[string]any{"signatureAlgorithm": "sha-384"})
	p.sendOK(t, "PO-MDN-SYNC", p.epcs[:1])

	// Async receipts, signed with SHA-512, settle the message.
	p.setOptions(t, p.globex, map[string]any{"mdnSignatureAlgorithm": "sha-512"})
	p.e.Must(http.StatusOK, p.e.Call(p.acme.token, "PUT", "/api/v1/partners/"+p.acme.partnerID+"/as2", map[string]any{"certPem": as2.CertPEM(p.globex.st.Cert), "mdn": "async"}), nil)
	id := p.shipment(t, "PO-MDN-ASYNC", p.epcs[1:2])
	if r := p.send(t, id); r.Status != "sent" || r.Response["mdn"] != "pending" {
		t.Fatalf("async: %+v", r)
	}
	p.e.Drain()
	settled := 0
	for _, row := range p.log(t, p.acme.token, "kind=as2") {
		if row.Status == "acknowledged" {
			settled++
		}
	}
	if settled != 2 {
		t.Fatalf("receipts did not settle: %+v", p.log(t, p.acme.token, "kind=as2"))
	}
	// A receipt that echoes a different integrity-check digest than the message used is refused.
	orig := ""
	for _, row := range p.log(t, p.acme.token, "kind=as2&direction=outbound") {
		orig = row.Reference
	}
	id2 := p.shipment(t, "PO-MDN-WRONGALG", p.epcs[2:3])
	if r := p.send(t, id2); r.Status != "sent" {
		t.Fatalf("second async: %+v", r)
	}
	var pending string
	for _, row := range p.log(t, p.acme.token, "kind=as2&direction=outbound") {
		if row.Status == "sent" {
			pending = row.Reference
		}
	}
	if pending == "" || pending == orig {
		t.Fatalf("no pending message: %+v", p.log(t, p.acme.token, "kind=as2"))
	}
	wrong, err := as2.BuildMDN(p.globex.st, as2.MDNInput{MessageID: as2.NewMessageID(p.globex.id), OriginalMessageID: pending, To: p.acme.id, MIC: "AAAA", MICAlg: "sha-256", Sign: true, Digest: "sha-512"})
	if err != nil {
		t.Fatal(err)
	}
	if code, _, _ := post(t, p.receive, wrong); code != http.StatusOK {
		t.Fatalf("mismatching receipt: %d", code)
	}
	var failed bool
	for _, row := range p.log(t, p.acme.token, "kind=as2&direction=outbound") {
		if row.Reference == pending {
			failed = row.Status == "failed" && strings.Contains(row.LastError, "SHA-384")
		}
	}
	if !failed {
		t.Fatalf("mismatching receipt must fail the message: %+v", p.log(t, p.acme.token, "kind=as2"))
	}
}
