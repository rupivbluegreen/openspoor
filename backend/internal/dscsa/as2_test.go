package dscsa_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/dscsa"
	"github.com/openspoor/openspoor/internal/testkit"
	"github.com/openspoor/openspoor/pkg/as2"
)

// as2Node is one side of the loopback pair: a tenant on the test node with its own AS2 station.
// The test keeps the station's key so that it can also craft messages by hand.
type as2Node struct {
	token string
	id    string
	st    as2.Station
	// partnerID is this node's partner record for the other side.
	partnerID string
}

type as2Pair struct {
	e       *testkit.Env
	acme    *as2Node // manufacturer: sends
	globex  *as2Node // wholesaler: receives
	epcs    []string
	receive string
}

func stationPEMs(t *testing.T, id string) (as2.Station, string, string) {
	t.Helper()
	c, k, err := as2.GenerateStation(id, time.Hour*24, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	kp, _ := as2.EncodeKeyPEM(k)
	return as2.Station{ID: id, Cert: c, Key: k}, as2.CertPEM(c), string(kp)
}

func newAS2Pair(t *testing.T) *as2Pair {
	t.Helper()
	e := testkit.New(t)
	_, whTok := e.NewTenant("globex", "admin@globex.test")
	p := &as2Pair{e: e, receive: e.Server.URL + "/api/v1/as2/receive"}
	for _, m := range e.App.Modules {
		if d, ok := m.(*dscsa.Module); ok {
			d.Cfg.PublicURL = e.Server.URL // where async receipts are asked to go
		}
	}
	p.acme = &as2Node{token: e.Token, id: "ACME-AS2"}
	p.globex = &as2Node{token: whTok, id: "GLOBEX-AS2"}
	for _, n := range []*as2Node{p.acme, p.globex} {
		st, certPEM, keyPEM := stationPEMs(t, n.id)
		n.st = st
		var out struct{ As2Id, Fingerprint, KeyPem, ReceiveUrl string }
		e.Must(http.StatusOK, e.Call(n.token, "PUT", "/api/v1/as2/station", map[string]any{"as2Id": n.id, "certPem": certPEM, "keyPem": keyPEM}), &out)
		if out.As2Id != n.id || out.Fingerprint != as2.Fingerprint(st.Cert) || out.ReceiveUrl != p.receive {
			t.Fatalf("station: %+v", out)
		}
	}
	// Each side registers the other as a partner and pins its certificate.
	for _, c := range []struct {
		me, other *as2Node
		name, gln string
	}{{p.acme, p.globex, "Globex Wholesale", "0614141000005"}, {p.globex, p.acme, "Acme Pharma", "9506000000008"}} {
		var pt struct{ ID string }
		e.Must(http.StatusCreated, e.Call(c.me.token, "POST", "/api/v1/partners", map[string]any{"name": c.name, "gln": c.gln,
			"endpoints": map[string]string{"as2Url": p.receive, "as2Id": c.other.id}}), &pt)
		c.me.partnerID = pt.ID
		e.Must(http.StatusOK, e.Call(c.me.token, "PUT", "/api/v1/partners/"+pt.ID+"/as2", map[string]any{"certPem": as2.CertPEM(c.other.st.Cert)}), nil)
	}
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/partners/listing", map[string]any{"name": "Acme Pharma", "gln": "9506000000008", "kind": "manufacturer", "country": "US"}), nil)
	_, p.epcs = commissionPacks(t, e, 6)
	capture(t, e, commission(p.epcs, "L9", "2030-01-31", "active", time.Now().UTC().Add(-time.Hour)))
	return p
}

// shipment creates an outbound AS2 shipment to Globex and returns its id.
func (p *as2Pair) shipment(t *testing.T, po string, epcs []string) string {
	t.Helper()
	var s struct{ ID string }
	p.e.Must(http.StatusCreated, p.e.Do("POST", "/api/v1/shipments", map[string]any{"partnerId": p.acme.partnerID, "poNumber": po, "asnNumber": "ASN-" + po, "epcs": epcs, "transport": "as2"}), &s)
	return s.ID
}

type sendResult struct {
	Status   string
	Response map[string]any
}

func (p *as2Pair) send(t *testing.T, id string) sendResult {
	t.Helper()
	var r sendResult
	p.e.Must(http.StatusOK, p.e.Do("POST", "/api/v1/shipments/"+id+"/send", nil), &r)
	return r
}

type logRow struct {
	ID, Kind, Direction, Status, Reference, Channel, LastError, PartnerName string
	Attempts                                                                int
	Retryable                                                               bool
}

func (p *as2Pair) log(t *testing.T, token, query string) []logRow {
	t.Helper()
	var l struct{ Items []logRow }
	p.e.Must(http.StatusOK, p.e.Call(token, "GET", "/api/v1/messages?"+query, nil), &l)
	return l.Items
}

func (p *as2Pair) shipments(t *testing.T, token, direction string) []struct{ ID, PONumber, Status string } {
	t.Helper()
	var l struct {
		Items []struct{ ID, PONumber, Status string }
	}
	p.e.Must(http.StatusOK, p.e.Call(token, "GET", "/api/v1/shipments?direction="+direction, nil), &l)
	return l.Items
}

func hasAction(t *testing.T, e *testkit.Env, token, action string) int {
	t.Helper()
	var aud struct{ Items []struct{ Action string } }
	e.Must(http.StatusOK, e.Call(token, "GET", "/api/v1/audit?limit=500&action="+action, nil), &aud)
	return len(aud.Items)
}

// craft builds a message from Acme to Globex by hand (the test holds Acme's key).
func (p *as2Pair) craft(t *testing.T, o as2.Outgoing, from as2.Station) *as2.Wire {
	t.Helper()
	if o.MessageID == "" {
		o.MessageID = as2.NewMessageID(from.ID)
	}
	if o.ContentType == "" {
		o.ContentType = "application/ld+json"
	}
	w, err := as2.Build(from, as2.Peer{ID: p.globex.id, Cert: p.globex.st.Cert}, o)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func post(t *testing.T, url string, w *as2.Wire) (int, http.Header, []byte) {
	t.Helper()
	code, h, b, err := as2.Post(context.Background(), http.DefaultClient, url, w)
	if err != nil {
		t.Fatal(err)
	}
	return code, h, b
}

func TestAS2LoopbackSyncReceiptAndIdempotency(t *testing.T) {
	p := newAS2Pair(t)
	e := p.e
	id := p.shipment(t, "PO-AS2-1", p.epcs[:3])
	r := p.send(t, id)
	if r.Status != "sent" || r.Response["mdn"] != "processed" || r.Response["signed"] != true || r.Response["encrypted"] != true || r.Response["transport"] != "as2" {
		t.Fatalf("send: %+v", r)
	}

	// Globex received and stored the TI/TS, linked to its partner Acme.
	in := p.shipments(t, p.globex.token, "inbound")
	if len(in) != 1 || in[0].PONumber != "PO-AS2-1" {
		t.Fatalf("inbound: %+v", in)
	}
	// Message log rows on both sides, plus the TI/TS row of the shipment.
	out := p.log(t, p.acme.token, "kind=as2")
	if len(out) != 1 || out[0].Direction != "outbound" || out[0].Status != "acknowledged" || out[0].Channel != "as2" || out[0].PartnerName != "Globex Wholesale" {
		t.Fatalf("acme as2 log: %+v", out)
	}
	rin := p.log(t, p.globex.token, "kind=as2")
	if len(rin) != 1 || rin[0].Direction != "inbound" || rin[0].Status != "received" || rin[0].PartnerName != "Acme Pharma" || rin[0].Reference != out[0].Reference {
		t.Fatalf("globex as2 log: %+v", rin)
	}
	tits := p.log(t, p.acme.token, "kind=tits")
	if len(tits) != 1 || tits[0].Status != "acknowledged" || tits[0].Retryable {
		t.Fatalf("tits row: %+v", tits)
	}
	if hasAction(t, e, p.acme.token, "as2.send") != 1 || hasAction(t, e, p.globex.token, "as2.receive") != 1 {
		t.Fatal("send / receive must each be audited once")
	}

	// A repeat of the same Message-ID is answered with the stored receipt and processed once.
	w := p.craft(t, as2.Outgoing{Payload: []byte(`{"type":"EPCISDocument","note":"not a TI/TS"}`), Sign: true, Encrypt: true, MDN: as2.MDNSync}, p.acme.st)
	code1, h1, b1 := post(t, p.receive, w)
	// The payload is not a valid EPCIS document: rejected (processing error), receipt says so.
	mdn1, err := as2.ParseMDN(h1, b1, as2.Peer{ID: p.globex.id, Cert: p.globex.st.Cert}, true, time.Now())
	if code1 != 200 || err != nil || mdn1.Processed || mdn1.ErrorCode != as2.ErrUnexpected {
		t.Fatalf("invalid payload: %d %v %+v", code1, err, mdn1)
	}
	if mdn1.MIC != "" {
		t.Fatal("error receipts carry no MIC")
	}

	// Valid payload: sent twice with the same Message-ID.
	var ship struct{ TITS []byte }
	_ = ship
	tits2 := fetchTITS(t, p, id)
	w2 := p.craft(t, as2.Outgoing{MessageID: "dup-1@acme", Payload: tits2, Sign: true, Encrypt: true, MDN: as2.MDNSync, Filename: "t.json"}, p.acme.st)
	c1, h1, b1 := post(t, p.receive, w2)
	c2, h2, b2 := post(t, p.receive, w2)
	if c1 != 200 || c2 != 200 || string(b1) != string(b2) || h1.Get("Message-ID") != h2.Get("Message-ID") {
		t.Fatalf("duplicate must replay the stored receipt: %d %d same=%v", c1, c2, string(b1) == string(b2))
	}
	m, err := as2.ParseMDN(h2, b2, as2.Peer{ID: p.globex.id, Cert: p.globex.st.Cert}, true, time.Now())
	if err != nil || !m.Processed || m.MIC != w2.MIC || m.OriginalMessageID != "dup-1@acme" {
		t.Fatalf("receipt: %+v %v", m, err)
	}
	if got := len(p.shipments(t, p.globex.token, "inbound")); got != 2 {
		t.Fatalf("inbound shipments after duplicate: %d (want 2: the first send and one new Message-ID)", got)
	}
	if hasAction(t, e, p.globex.token, "as2.receive.duplicate") != 1 {
		t.Fatal("duplicate not audited")
	}
}

func fetchTITS(t *testing.T, p *as2Pair, shipmentID string) []byte {
	t.Helper()
	var s struct {
		TITS map[string]any
	}
	p.e.Must(http.StatusOK, p.e.Do("GET", "/api/v1/shipments/"+shipmentID, nil), &s)
	// Re-render through the raw response to keep the exact bytes of the stored document.
	r := p.e.Do("GET", "/api/v1/shipments/"+shipmentID, nil)
	var raw struct {
		TITS jsonRaw `json:"tits"`
	}
	if err := r.JSON(&raw); err != nil || len(raw.TITS) == 0 {
		t.Fatalf("no stored TI/TS: %v", err)
	}
	return raw.TITS
}

type jsonRaw []byte

func (j *jsonRaw) UnmarshalJSON(b []byte) error { *j = append((*j)[:0], b...); return nil }

func TestAS2NegativeBadSignaturesAndPolicy(t *testing.T) {
	p := newAS2Pair(t)
	e := p.e
	id := p.shipment(t, "PO-AS2-N", p.epcs[:2])
	doc := func() []byte {
		// A valid TI/TS document: build it through the API once and reuse the bytes.
		e.Must(http.StatusOK, e.Do("POST", "/api/v1/shipments/"+id+"/tits", nil), nil)
		return fetchTITS(t, p, id)
	}()
	peer := as2.Peer{ID: p.globex.id, Cert: p.globex.st.Cert}
	mdnOf := func(w *as2.Wire, wantCode int) *as2.MDN {
		t.Helper()
		code, h, b := post(t, p.receive, w)
		if code != wantCode {
			t.Fatalf("HTTP %d (%s), want %d", code, b, wantCode)
		}
		if wantCode != 200 {
			return nil
		}
		m, err := as2.ParseMDN(h, b, peer, true, time.Now())
		if err != nil {
			t.Fatalf("receipt: %v", err)
		}
		return m
	}
	base := as2.Outgoing{Payload: doc, Sign: true, Encrypt: true, MDN: as2.MDNSync}

	// 1. Signed with a key that is not Acme's pinned certificate.
	evil, _, _ := stationPEMs(t, p.acme.id)
	m := mdnOf(p.craft(t, base, evil), 200)
	if m.Processed || m.ErrorCode != as2.ErrAuthentication {
		t.Fatalf("wrong signer: %+v", m)
	}
	// 3. Unsigned, while the connection requires signatures.
	if m = mdnOf(p.craft(t, as2.Outgoing{Payload: doc, Encrypt: true, MDN: as2.MDNSync}, p.acme.st), 200); m.Processed || m.ErrorCode != as2.ErrInsufficient {
		t.Fatalf("unsigned: %+v", m)
	}
	// 4. Not encrypted, while the connection requires encryption.
	if m = mdnOf(p.craft(t, as2.Outgoing{Payload: doc, Sign: true, MDN: as2.MDNSync}, p.acme.st), 200); m.Processed || m.ErrorCode != as2.ErrInsufficient {
		t.Fatalf("plain: %+v", m)
	}
	// 2. Signed message whose content was altered after signing. Encryption is switched off for
	// this connection meanwhile, because an encrypted message cannot be edited.
	setEnc := func(on bool) {
		e.Must(http.StatusOK, e.Call(p.globex.token, "PUT", "/api/v1/partners/"+p.globex.partnerID+"/as2", map[string]any{"certPem": as2.CertPEM(p.acme.st.Cert), "encrypt": on}), nil)
	}
	setEnc(false)
	sw := p.craft(t, as2.Outgoing{Payload: doc, Sign: true, MDN: as2.MDNSync}, p.acme.st)
	sw.Body = []byte(strings.Replace(string(sw.Body), "PO-AS2-N", "PO-AS2-X", 1))
	if m = mdnOf(sw, 200); m.Processed || m.ErrorCode != as2.ErrIntegrity {
		t.Fatalf("tampered: %+v", m)
	}
	setEnc(true)
	// 5. Encrypted to the wrong recipient certificate.
	other, _, _ := stationPEMs(t, p.globex.id)
	wrongRcpt, err := as2.Build(p.acme.st, as2.Peer{ID: p.globex.id, Cert: other.Cert}, as2.Outgoing{MessageID: "wr@acme", ContentType: "application/ld+json", Payload: doc, Sign: true, Encrypt: true, MDN: as2.MDNSync})
	if err != nil {
		t.Fatal(err)
	}
	if m = mdnOf(wrongRcpt, 200); m.Processed || m.ErrorCode != as2.ErrDecryption {
		t.Fatalf("wrong recipient: %+v", m)
	}
	// 6. Without a receipt request the failure is an HTTP error.
	if code, _, _ := post(t, p.receive, p.craft(t, as2.Outgoing{Payload: doc, Sign: true}, evil)); code != http.StatusBadRequest {
		t.Fatalf("no-receipt failure: %d", code)
	}
	// 7. Unknown sender, unknown recipient, malformed headers.
	ghost, _, _ := stationPEMs(t, "GHOST")
	if code, _, _ := post(t, p.receive, p.craft(t, base, ghost)); code != http.StatusForbidden {
		t.Fatalf("unknown sender: %d", code)
	}
	toNobody := p.craft(t, base, p.acme.st)
	toNobody.Header["AS2-To"] = "NOBODY"
	if code, _, _ := post(t, p.receive, toNobody); code != http.StatusNotFound {
		t.Fatalf("unknown recipient: %d", code)
	}
	noFrom := p.craft(t, base, p.acme.st)
	delete(noFrom.Header, "AS2-From")
	if code, _, _ := post(t, p.receive, noFrom); code != http.StatusBadRequest {
		t.Fatalf("malformed: %d", code)
	}

	// Nothing was stored as a shipment; the rejections are in the message log, the audit trail and as alerts.
	if got := len(p.shipments(t, p.globex.token, "inbound")); got != 0 {
		t.Fatalf("rejected messages created %d shipments", got)
	}
	rej := p.log(t, p.globex.token, "kind=as2&status=failed")
	if len(rej) < 5 {
		t.Fatalf("rejected rows: %+v", rej)
	}
	if hasAction(t, e, p.globex.token, "as2.receive.rejected") < 5 {
		t.Fatal("rejections not audited")
	}
	var al struct{ Items []struct{ Kind string } }
	e.Must(http.StatusOK, e.Call(p.globex.token, "GET", "/api/v1/alerts?kind=dscsa.as2_rejected", nil), &al)
	if len(al.Items) == 0 || len(al.Items) > 5 {
		t.Fatalf("alerts (deduplicated per sender and reason): %d", len(al.Items))
	}

	// A rejected Message-ID is not claimed: a correct resend with the same id is accepted.
	good := p.craft(t, as2.Outgoing{MessageID: "resend-1@acme", Payload: doc, Sign: true, Encrypt: true, MDN: as2.MDNSync}, p.acme.st)
	bad := p.craft(t, as2.Outgoing{MessageID: "resend-1@acme", Payload: doc, Sign: true, Encrypt: true, MDN: as2.MDNSync}, evil)
	if m = mdnOf(bad, 200); m.Processed {
		t.Fatal("forged message accepted")
	}
	if m = mdnOf(good, 200); !m.Processed {
		t.Fatalf("resend after a forged attempt: %+v", m)
	}

	// The sender side: a partner whose receipt fails verification fails the shipment.
	// Globex's certificate on file at Acme is replaced by another one, so Globex's (genuine)
	// receipt no longer verifies, and encryption goes to a key Globex does not hold.
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/partners/"+p.acme.partnerID+"/as2", map[string]any{"certPem": as2.CertPEM(other.Cert), "encrypt": false}), nil)
	id2 := p.shipment(t, "PO-AS2-F", p.epcs[2:3])
	r := p.send(t, id2)
	if r.Status != "send_failed" || r.Response["mdn"] != "failed" || r.Response["error"] == "" {
		t.Fatalf("send with wrong pinned certificate: %+v", r)
	}
}

func TestAS2AsyncReceipt(t *testing.T) {
	p := newAS2Pair(t)
	e := p.e
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/partners/"+p.acme.partnerID+"/as2", map[string]any{"certPem": as2.CertPEM(p.globex.st.Cert), "mdn": "async"}), nil)
	// Globex returns async receipts to Acme's receive URL (the default: the partner's AS2 URL).
	id := p.shipment(t, "PO-ASYNC-1", p.epcs[:2])
	r := p.send(t, id)
	if r.Status != "sent" || r.Response["mdn"] != "pending" {
		t.Fatalf("async send: %+v", r)
	}
	if o := p.log(t, p.acme.token, "kind=as2"); len(o) != 1 || o[0].Status != "sent" {
		t.Fatalf("awaiting receipt: %+v", o)
	}
	if got := p.shipments(t, p.globex.token, "inbound"); len(got) != 1 {
		t.Fatalf("globex inbound: %+v", got)
	}

	// Before the genuine receipt is delivered, forged and invalid receipts are refused.
	var orig string
	if o := p.log(t, p.acme.token, "kind=as2"); len(o) == 1 {
		orig = o[0].Reference
	}
	mdnWire := func(from as2.Station, in as2.MDNInput) *as2.Wire {
		t.Helper()
		in.MessageID, in.OriginalMessageID, in.To = as2.NewMessageID(from.ID), orig, p.acme.id
		w, err := as2.BuildMDN(from, in)
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	evil, _, _ := stationPEMs(t, p.globex.id)
	if code, _, _ := post(t, p.receive, mdnWire(evil, as2.MDNInput{MIC: "x", Sign: true})); code != http.StatusBadRequest {
		t.Fatalf("forged receipt: %d", code)
	}
	if code, _, _ := post(t, p.receive, mdnWire(p.globex.st, as2.MDNInput{MIC: "x"})); code != http.StatusBadRequest {
		t.Fatalf("unsigned receipt: %d", code)
	}
	unknown := mdnWire(p.globex.st, as2.MDNInput{MIC: "x", Sign: true})
	w2, _ := as2.BuildMDN(p.globex.st, as2.MDNInput{MessageID: "r9@g", OriginalMessageID: "nothing@acme", To: p.acme.id, MIC: "x", Sign: true})
	_ = unknown
	if code, _, _ := post(t, p.receive, w2); code != http.StatusNotFound {
		t.Fatalf("receipt for unknown message: %d", code)
	}
	// A genuine error receipt for the pending message fails the shipment and raises an alert.
	if code, _, _ := post(t, p.receive, mdnWire(p.globex.st, as2.MDNInput{ErrorCode: as2.ErrIntegrity, Sign: true})); code != http.StatusOK {
		t.Fatalf("error receipt: %d", code)
	}
	if o := p.log(t, p.acme.token, "kind=as2"); o[0].Status != "failed" || !strings.Contains(o[0].LastError, as2.ErrIntegrity) {
		t.Fatalf("after error receipt: %+v", o)
	}
	if tr := p.log(t, p.acme.token, "kind=tits"); tr[0].Status != "failed" || !tr[0].Retryable {
		t.Fatalf("shipment after error receipt: %+v", tr)
	}
	var al struct{ Items []struct{ Kind string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?kind=dscsa.send_failed", nil), &al)
	if len(al.Items) != 1 {
		t.Fatalf("alerts: %+v", al)
	}
	// Then Globex's queued (genuine, processed) receipt arrives: a message that is no longer
	// pending is acknowledged without changing anything.
	e.Drain()
	if o := p.log(t, p.acme.token, "kind=as2"); o[0].Status != "failed" {
		t.Fatalf("late receipt must not flip a settled message: %+v", o)
	}
	if hasAction(t, e, p.globex.token, "as2.mdn.send") != 1 || hasAction(t, e, p.acme.token, "as2.mdn.receive") != 1 {
		t.Fatal("receipt exchange not audited")
	}

	// Second message: the genuine receipt settles it.
	id2 := p.shipment(t, "PO-ASYNC-2", p.epcs[2:4])
	if r := p.send(t, id2); r.Status != "sent" || r.Response["mdn"] != "pending" {
		t.Fatalf("second send: %+v", r)
	}
	e.Drain()
	var ok []logRow
	for _, row := range p.log(t, p.acme.token, "kind=as2") {
		if row.Status == "acknowledged" {
			ok = append(ok, row)
		}
	}
	if len(ok) != 1 {
		t.Fatalf("async receipt did not settle the message: %+v", p.log(t, p.acme.token, "kind=as2"))
	}
	if tr := p.log(t, p.acme.token, "kind=tits"); tr[0].Status != "acknowledged" {
		t.Fatalf("shipment after processed receipt: %+v", tr)
	}

	// A receipt URL other than the one configured for the connection is refused (no callbacks to
	// addresses chosen by the sender).
	w := p.craft(t, as2.Outgoing{Payload: []byte(`{}`), Sign: true, Encrypt: true, MDN: as2.MDNAsync, AsyncURL: "http://169.254.169.254/latest"}, p.acme.st)
	if code, _, _ := post(t, p.receive, w); code != http.StatusBadRequest {
		t.Fatalf("foreign receipt url: %d", code)
	}

	// An async receipt that cannot be delivered is retried and then reported.
	e.Must(http.StatusOK, e.Call(p.globex.token, "PUT", "/api/v1/partners/"+p.globex.partnerID+"/as2", map[string]any{"certPem": as2.CertPEM(p.acme.st.Cert), "mdnUrl": "http://127.0.0.1:1/never"}), nil)
	w = p.craft(t, as2.Outgoing{Payload: fetchTITS(t, p, id2), Sign: true, Encrypt: true, MDN: as2.MDNAsync, AsyncURL: "http://127.0.0.1:1/never", MessageID: "undeliverable@acme"}, p.acme.st)
	if code, _, _ := post(t, p.receive, w); code != http.StatusOK {
		t.Fatalf("async accept: %d", code)
	}
	for i := 0; i < 8; i++ { // backoff: pull the job forward until it is exhausted
		forceJobsDue(t, e)
		e.Drain()
	}
	var al2 struct{ Items []struct{ Kind string } }
	e.Must(http.StatusOK, e.Call(p.globex.token, "GET", "/api/v1/alerts?kind=dscsa.as2_mdn_failed", nil), &al2)
	if len(al2.Items) != 1 {
		t.Fatalf("undeliverable receipt alert: %+v", al2)
	}
}

func forceJobsDue(t *testing.T, e *testkit.Env) {
	t.Helper()
	ctx := context.Background()
	c, err := pgx.Connect(ctx, e.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	if _, err := c.Exec(ctx, `update job set run_at = now() where status = 'queued'`); err != nil {
		t.Fatal(err)
	}
}

func TestAS2StationAndProfileAdministration(t *testing.T) {
	p := newAS2Pair(t)
	e := p.e
	viewer := userToken(t, e, "viewer-as2@acme.test", "Viewer")

	// Station: readable by viewers, writable by admin only, validated, unique across the node.
	var st struct{ As2Id, CertPem, Fingerprint string }
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/as2/station", nil), &st)
	if st.As2Id != p.acme.id || !strings.Contains(st.CertPem, "BEGIN CERTIFICATE") || strings.Contains(string(e.Call(viewer, "GET", "/api/v1/as2/station", nil).Body), "PRIVATE") {
		t.Fatalf("station: %+v", st)
	}
	e.Must(http.StatusForbidden, e.Call(viewer, "PUT", "/api/v1/as2/station", map[string]any{"as2Id": "V", "generate": true}), nil)
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/as2/station", map[string]any{"as2Id": "bad id", "generate": true}), nil)
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/as2/station", map[string]any{"as2Id": "X1", "certPem": "nope", "keyPem": "nope"}), nil)
	_, certPEM, keyPEM := stationPEMs(t, "X1")
	_, otherCert, otherKey := stationPEMs(t, "X2")
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/as2/station", map[string]any{"as2Id": "X1", "certPem": certPEM, "keyPem": "nope"}), nil)
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/as2/station", map[string]any{"as2Id": "X1", "certPem": certPEM, "keyPem": otherKey}), nil)
	_ = otherCert
	e.Must(http.StatusConflict, e.Do("PUT", "/api/v1/as2/station", map[string]any{"as2Id": p.globex.id, "generate": true}), nil)
	// A generated station works, and the private key is sealed at rest.
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/as2/station", map[string]any{"as2Id": p.acme.id, "generate": true}), &st)
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/as2/station", map[string]any{"as2Id": p.acme.id, "certPem": certPEM, "keyPem": keyPEM}), nil)
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/as2/station", map[string]any{"as2Id": p.acme.id, "generate": true}), nil)
	ctx := context.Background()
	c, err := pgx.Connect(ctx, e.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	var sealedKey, sealedCert []byte
	if err := c.QueryRow(ctx, `select key_sealed, (select cert_sealed from partner_as2 limit 1) from as2_station where tenant_id=$1`, e.Tenant.ID).Scan(&sealedKey, &sealedCert); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealedKey), "PRIVATE KEY") || strings.Contains(string(sealedCert), "BEGIN CERTIFICATE") || len(sealedKey) == 0 || len(sealedCert) == 0 {
		t.Fatal("key and partner certificate must be sealed at rest")
	}

	// Partner profile.
	var prof struct {
		Fingerprint, MDN, CertPem string
		Sign, Encrypt             bool
	}
	pid := p.acme.partnerID
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/partners/"+pid+"/as2", nil), &prof)
	if prof.Fingerprint != as2.Fingerprint(p.globex.st.Cert) || !prof.Sign || !prof.Encrypt || prof.MDN != "sync" || prof.CertPem == "" {
		t.Fatalf("defaults: %+v", prof)
	}
	e.Must(http.StatusForbidden, e.Call(viewer, "PUT", "/api/v1/partners/"+pid+"/as2", map[string]any{"certPem": certPEM}), nil)
	e.Must(http.StatusForbidden, e.Call(viewer, "DELETE", "/api/v1/partners/"+pid+"/as2", nil), nil)
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/partners/"+pid+"/as2", map[string]any{"certPem": "nope"}), nil)
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/partners/"+pid+"/as2", map[string]any{"certPem": certPEM, "mdn": "carrier-pigeon"}), nil)
	e.Must(http.StatusBadRequest, e.Do("PUT", "/api/v1/partners/"+pid+"/as2", map[string]any{"certPem": certPEM, "mdnUrl": "ftp://x"}), nil)
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/partners", map[string]any{"name": "Bad", "endpoints": map[string]string{"as2Id": "has space"}}), nil)
	noID := createPartner(t, e, map[string]any{"name": "No AS2 id"})
	e.Must(http.StatusConflict, e.Do("PUT", "/api/v1/partners/"+noID+"/as2", map[string]any{"certPem": certPEM}), nil)
	e.Must(http.StatusNotFound, e.Do("GET", "/api/v1/partners/"+noID+"/as2", nil), nil)
	e.Must(http.StatusNotFound, e.Do("DELETE", "/api/v1/partners/"+noID+"/as2", nil), nil)

	// Tenant isolation: Globex cannot see Acme's profile, station or messages.
	e.Must(http.StatusNotFound, e.Call(p.globex.token, "GET", "/api/v1/partners/"+pid+"/as2", nil), nil)
	var n int
	if err := e.App.Deps.DB.Tx(ctx, e.Tenant.ID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*) from as2_station`).Scan(&n)
	}); err != nil || n != 1 {
		t.Fatalf("tenant sees %d stations (%v)", n, err)
	}

	// Removing the profile makes AS2 delivery refuse, audited.
	e.Must(http.StatusNoContent, e.Do("DELETE", "/api/v1/partners/"+pid+"/as2", nil), nil)
	id := p.shipment(t, "PO-NOPROF", p.epcs[:1])
	if r := p.send(t, id); r.Status != "send_failed" || !strings.Contains(r.Response["error"].(string), "no AS2 profile") {
		t.Fatalf("no profile: %+v", r)
	}
	for _, a := range []string{"as2.station.set", "partner.as2.set", "partner.as2.delete"} {
		if hasAction(t, e, p.acme.token, a) == 0 {
			t.Fatalf("missing audit entry %s", a)
		}
	}
	// A partner without an AS2 URL cannot be sent to, and without a station nothing can.
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/partners/"+noID, map[string]any{"name": "No AS2 id", "endpoints": map[string]string{"as2Id": "NOURL"}}), nil)
	var s struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/shipments", map[string]any{"partnerId": noID, "poNumber": "PO-NOURL", "epcs": p.epcs[:1], "transport": "as2"}), &s)
	if r := p.send(t, s.ID); !strings.Contains(r.Response["error"].(string), "no AS2 URL") {
		t.Fatalf("no url: %+v", r)
	}
	if _, err := (dscsa.AS2Transport{}).Send(ctx, e.Tenant.ID, partnersPartner(""), dscsa.Shipment{}, nil); err == nil {
		t.Fatal("zero transport must refuse")
	}
	// Without a station at all.
	if err := e.App.Deps.DB.Tx(ctx, e.Tenant.ID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `delete from as2_station`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	e.Must(http.StatusNotFound, e.Do("GET", "/api/v1/as2/station", nil), nil)
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/partners/"+noID, map[string]any{"name": "No AS2 id", "endpoints": map[string]string{"as2Id": "NOURL", "as2Url": p.receive}}), nil)
	if r := p.send(t, s.ID); !strings.Contains(r.Response["error"].(string), "AS2 station") {
		t.Fatalf("no station: %+v", r)
	}
}

// A TI/TS whose AS2 delivery failed is retried from the message log: the retry goes through the
// AS2 transport again (new Message-ID) and succeeds once the connection is configured.
func TestAS2RetryFromMessageLog(t *testing.T) {
	p := newAS2Pair(t)
	e := p.e
	e.Must(http.StatusNoContent, e.Do("DELETE", "/api/v1/partners/"+p.acme.partnerID+"/as2", nil), nil)
	id := p.shipment(t, "PO-AS2-RETRY", p.epcs[:2])
	if r := p.send(t, id); r.Status != "send_failed" {
		t.Fatalf("send without a profile: %+v", r)
	}
	rows := p.log(t, p.acme.token, "kind=tits")
	if len(rows) != 1 || rows[0].Status != "failed" || !rows[0].Retryable || rows[0].Channel != "as2" {
		t.Fatalf("failed TI/TS row: %+v", rows)
	}
	// Retry while still unconfigured: fails again, attempts counted.
	var res struct {
		Status   string
		Response map[string]any
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/messages/"+id+"/retry", nil), &res)
	if res.Status != "send_failed" {
		t.Fatalf("retry 1: %+v", res)
	}
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/partners/"+p.acme.partnerID+"/as2", map[string]any{"certPem": as2.CertPEM(p.globex.st.Cert)}), nil)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/messages/"+id+"/retry", nil), &res)
	if res.Status != "sent" || res.Response["mdn"] != "processed" {
		t.Fatalf("retry 2: %+v", res)
	}
	rows = p.log(t, p.acme.token, "kind=tits")
	if rows[0].Status != "acknowledged" || rows[0].Retryable || rows[0].Attempts != 3 {
		t.Fatalf("after retry: %+v", rows)
	}
	if got := p.shipments(t, p.globex.token, "inbound"); len(got) != 1 || got[0].PONumber != "PO-AS2-RETRY" {
		t.Fatalf("globex inbound: %+v", got)
	}
	// Each AS2 attempt that reached the wire has its own message-log row; the unconfigured ones never did.
	if as2rows := p.log(t, p.acme.token, "kind=as2&direction=outbound"); len(as2rows) != 1 || as2rows[0].Status != "acknowledged" {
		t.Fatalf("as2 rows: %+v", as2rows)
	}
}
