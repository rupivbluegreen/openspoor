package connectors_test

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	connector "github.com/openspoor/connectors-sdk"
	"github.com/openspoor/openspoor/internal/connectors"
	"github.com/openspoor/openspoor/internal/connectors/mockhub"
	"github.com/openspoor/openspoor/internal/connectors/sftpdrop"
	"github.com/openspoor/openspoor/internal/testkit"
	"github.com/openspoor/openspoor/pkg/edi"
	lib "github.com/openspoor/openspoor/pkg/epcis"
)

func capture(t *testing.T, e *testkit.Env, evs ...lib.Event) {
	t.Helper()
	b, _ := lib.WriteJSON(lib.NewDocument(evs))
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/epcis/capture", b), nil)
}

func commission(epcs []string, lot string) lib.Event {
	ilmd := map[string]any{"itemExpirationDate": time.Now().AddDate(2, 0, 0).Format("2006-01-02")}
	if lot != "" {
		ilmd["lotNumber"] = lot
	}
	return lib.Event{Type: lib.ObjectEvent, Action: "ADD", BizStep: "commissioning", Disposition: "active", EventTime: time.Now().UTC().Add(-time.Hour),
		EventTimeZoneOffset: "+00:00", EPCList: epcs, ILMD: ilmd}
}

type alertItem struct {
	Title  string
	Detail map[string]any
}

func alerts(t *testing.T, e *testkit.Env, kind string) []alertItem {
	t.Helper()
	var a struct{ Items []alertItem }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?kind="+kind, nil), &a)
	return a.Items
}

func TestTatmeenAndEDAReporting(t *testing.T) {
	e := testkit.New(t)
	var cat struct{ Items []connector.Info }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/connectors", nil), &cat)
	seen := map[string]bool{}
	for _, c := range cat.Items {
		seen[c.ID] = true
	}
	if !seen["ae-tatmeen"] || !seen["eg-eda"] || !seen["sftp"] {
		t.Fatalf("catalogue: %+v", seen)
	}
	var ae, eg struct {
		ID         string
		Config     map[string]string
		SecretsSet map[string]bool
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/connectors/instances", map[string]any{"connector": "ae-tatmeen",
		"config": map[string]string{"clientId": "acme", "clientSecret": "s3cret", "participantId": "P1"}}), &ae)
	if ae.Config["clientSecret"] != "" || !ae.SecretsSet["clientSecret"] || ae.Config["baseUrl"] != "local-mock" {
		t.Fatalf("instance: %+v", ae)
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/connectors/instances", map[string]any{"connector": "eg-eda"}), &eg)

	good := packs(t, e, "09506000134352", []string{"AE", "EG"}, 2, "k1")
	capture(t, e, commission(good, "LOT-1"))
	rs := settle(t, e, 2)
	for _, r := range rs {
		if r.Status != "accepted" || r.Kind != "commission" {
			t.Fatalf("commission: %+v", rs)
		}
	}
	user, party := findConnectors(e).Mocks.Handlers[mockhub.AEHost].(*mockhub.JSONHub).LastCaller()
	if user != "acme" || party != "P1" {
		t.Fatalf("hub saw %s / %s", user, party)
	}
	// Dispatch without a receiver GLN: rejected, with a suggested fix in the alert inbox.
	ship := lib.Event{Type: lib.ObjectEvent, Action: "OBSERVE", BizStep: "shipping", Disposition: "in_transit", EventTime: time.Now().UTC(), EventTimeZoneOffset: "+00:00", EPCList: good}
	capture(t, e, ship)
	rs = settle(t, e, 4)
	rejected := 0
	for _, r := range rs {
		if r.Kind == "dispatch" && r.Status == "rejected" {
			rejected++
		}
	}
	if rejected != 2 {
		t.Fatalf("dispatch: %+v", rs)
	}
	as := alerts(t, e, "regulatory.")
	if len(as) != 2 {
		t.Fatalf("alerts: %+v", as)
	}
	for _, a := range as {
		if !strings.Contains(a.Detail["suggestedFix"].(string), "receiver's GLN") || !strings.Contains(a.Title, "Tatmeen") && !strings.Contains(a.Title, "EDA") {
			t.Fatalf("alert: %+v", a)
		}
	}
	// Rule packs: the TODO(spec) rules are listed but never evaluated.
	var pk struct {
		Rules []struct {
			ID, Source, Todo string
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/rulepacks/ae-tatmeen", nil), &pk)
	if len(pk.Rules) < 2 || pk.Rules[1].Source != "" || !strings.HasPrefix(pk.Rules[1].Todo, "TODO(spec)") {
		t.Fatalf("pack: %+v", pk)
	}
	var f struct{ Items []struct{ Pack, Rule string } }
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/rulepacks/evaluate", nil), &f)
	for _, x := range f.Items {
		if (x.Pack == "ae-tatmeen" || x.Pack == "eg-eda") && x.Rule != "serial-max-20" {
			t.Fatalf("unattributed rule evaluated: %+v", x)
		}
	}
}

func sftpConfig(s *mockhub.SFTPServer) map[string]string {
	return map[string]string{"host": "127.0.0.1", "port": strconv.Itoa(s.Port), "username": "partner", "password": "pw", "hostKeyFingerprint": s.Fingerprint,
		"bizSteps": "shipping"}
}

func TestSFTPExchange(t *testing.T) {
	e := testkit.New(t)
	srv, err := mockhub.NewSFTPServer("partner", "pw", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	cfg := sftpConfig(srv)

	// Validation: the host key must be pinned and well-formed.
	bad := map[string]string{"host": "h", "username": "u", "password": "p"}
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/connectors/instances", map[string]any{"connector": "sftp", "config": bad}), nil)
	bad["hostKeyFingerprint"] = "MD5:00:11"
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/connectors/instances", map[string]any{"connector": "sftp", "config": bad}), nil)

	var inst struct {
		ID         string
		Config     map[string]string
		SecretsSet map[string]bool
	}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/connectors/instances", map[string]any{"connector": "sftp", "name": "Wholesaler drop", "config": cfg}), &inst)
	if inst.Config["password"] != "" || !inst.SecretsSet["password"] || inst.Config["inboundDir"] != "inbound" || inst.Config["archiveDir"] != "archive" {
		t.Fatalf("instance: %+v", inst)
	}

	// Host key helper and connection test.
	var hk struct{ Fingerprint string }
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/connectors/sftp/host-key", map[string]any{"host": "127.0.0.1", "port": srv.Port}), &hk)
	if hk.Fingerprint != srv.Fingerprint {
		t.Fatalf("host key: %+v", hk)
	}
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/connectors/sftp/host-key", map[string]any{"host": ""}), nil)
	e.Must(http.StatusUnprocessableEntity, e.Do("POST", "/api/v1/connectors/sftp/host-key", map[string]any{"host": "127.0.0.1", "port": 1}), nil)
	var test struct {
		OK      bool
		Folders map[string]map[string]any
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/connectors/instances/"+inst.ID+"/sftp/test", nil), &test)
	if !test.OK || test.Folders["inboundDir"]["error"] == nil {
		t.Fatalf("test: %+v", test)
	}

	// Outbound: shipping events are dropped as an EPCIS file; commissioning is filtered out.
	epcs := packs(t, e, "09506000134352", []string{"NL"}, 2, "s1")
	capture(t, e, commission(epcs, "L1"))
	capture(t, e, lib.Event{Type: lib.ObjectEvent, Action: "OBSERVE", BizStep: "shipping", Disposition: "in_transit", EventTime: time.Now().UTC(), EventTimeZoneOffset: "+00:00", EPCList: epcs})
	rs := settle(t, e, 1)
	if len(rs) != 1 || rs[0].Status != "accepted" || rs[0].Kind != "epcis-document" || !strings.HasPrefix(rs[0].SubmissionRef, "/outbound/openspoor-") {
		t.Fatalf("outbound: %+v", rs)
	}
	ep, _ := sftpdrop.EndpointFromConfig(connector.Config(cfg))
	sess, err := sftpdrop.Dial(context.Background(), ep)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	b, err := sess.Read(rs[0].SubmissionRef, 1<<20)
	if err != nil || !strings.Contains(string(b), `"bizStep": "shipping"`) || strings.Contains(string(b), "commissioning") {
		t.Fatalf("dropped file: %v %s", err, b)
	}

	// Inbound: one EPCIS file, one X12 purchase order, one broken file.
	recv := lib.Event{Type: lib.ObjectEvent, Action: "OBSERVE", BizStep: "receiving", Disposition: "in_progress", EventTime: time.Now().UTC(), EventTimeZoneOffset: "+00:00", EPCList: epcs}
	doc, _ := lib.WriteJSON(lib.NewDocument([]lib.Event{recv}))
	order := edi.X12(edi.Document{Type: "order", Number: "PO-77", Date: time.Now().UTC(), Buyer: edi.Party{Name: "B", GLN: "9506000000016"}, Seller: edi.Party{Name: "S"},
		Lines: []edi.Line{{GTIN: "09506000134352", Quantity: 5, UnitPrice: 2}}}, edi.Envelope{SenderID: "B", ReceiverID: "S", Control: 1})
	for name, data := range map[string]string{"a-receipt.json": string(doc), "b-order.x12": order, "c-broken.xml": "<nope"} {
		if _, err := sess.Put("inbound", name, []byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	var res connectors.PollResult
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/connectors/instances/"+inst.ID+"/sftp/poll", nil), &res)
	if res.Processed != 2 || res.Failed != 1 {
		t.Fatalf("poll: %+v", res)
	}
	byName := map[string]connectors.SFTPFile{}
	for _, f := range res.Files {
		byName[f.Name] = f
	}
	if f := byName["a-receipt.json"]; f.Format != "epcis" || f.CaptureID == nil || f.MovedTo != "/archive/a-receipt.json" {
		t.Fatalf("epcis file: %+v", f)
	}
	if f := byName["b-order.x12"]; f.Format != "x12" || f.DocumentID == "" || f.MovedTo != "/archive/b-order.x12" {
		t.Fatalf("edi file: %+v", f)
	}
	if f := byName["c-broken.xml"]; f.Status != "failed" || f.MovedTo != "/error/c-broken.xml" || f.Error == "" {
		t.Fatalf("broken file: %+v", f)
	}
	if left, _ := sess.List("inbound"); len(left) != 0 {
		t.Fatalf("inbound not emptied: %+v", left)
	}
	if as := alerts(t, e, "integration.sftp"); len(as) != 1 || as[0].Detail["file"] != "c-broken.xml" || as[0].Detail["suggestedFix"] == "" {
		t.Fatalf("alerts: %+v", as)
	}
	var files struct{ Items []connectors.SFTPFile }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/connectors/instances/"+inst.ID+"/files?status=failed", nil), &files)
	if len(files.Items) != 1 {
		t.Fatalf("files: %+v", files.Items)
	}
	var docs struct {
		Items []struct{ Number, Direction string }
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/b2b/documents", nil), &docs)
	if len(docs.Items) == 0 || docs.Items[0].Number != "PO-77" {
		t.Fatalf("b2b docs: %+v", docs.Items)
	}
	var audit struct{ Items []struct{ Action string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?entity=sftp_file", nil), &audit)
	actions := map[string]int{}
	for _, a := range audit.Items {
		actions[a.Action]++
	}
	if actions["sftp.inbound.processed"] != 2 || actions["sftp.inbound.failed"] != 1 {
		t.Fatalf("audit: %+v", actions)
	}

	// A duplicate EPCIS capture is idempotent; the scheduled poll picks the file up.
	if _, err := sess.Put("inbound", "a-receipt.json", doc); err != nil {
		t.Fatal(err)
	}
	mod := findConnectors(e)
	if err := mod.Schedules()[0].Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if arch, _ := sess.List("archive"); len(arch) != 3 {
		t.Fatalf("archive: %+v", arch)
	}

	// A changed host key is refused and raises a connection alert naming the presented key.
	other, _ := mockhub.NewSFTPServer("partner", "pw", nil)
	defer other.Close()
	wrong := map[string]string{"hostKeyFingerprint": other.Fingerprint}
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/connectors/instances/"+inst.ID, map[string]any{"config": merge(inst.Config, wrong)}), nil)
	e.Must(http.StatusUnprocessableEntity, e.Do("POST", "/api/v1/connectors/instances/"+inst.ID+"/sftp/poll", nil), nil)
	var conn []alertItem
	for _, a := range alerts(t, e, "integration.sftp") {
		if strings.Contains(a.Title, "connection failed") {
			conn = append(conn, a)
		}
	}
	if len(conn) != 1 || !strings.Contains(conn[0].Detail["suggestedFix"].(string), srv.Fingerprint) {
		t.Fatalf("connection alert: %+v", conn)
	}
	var tst struct {
		OK                   bool
		PresentedFingerprint string
	}
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/connectors/instances/"+inst.ID+"/sftp/test", nil), &tst)
	if tst.OK || tst.PresentedFingerprint != srv.Fingerprint {
		t.Fatalf("test: %+v", tst)
	}
	if err := mod.Schedules()[0].Run(context.Background()); err == nil {
		t.Fatal("scheduled poll must report the failing instance")
	}
	// Fixing the pin resolves the connection alert on the next successful poll.
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/connectors/instances/"+inst.ID, map[string]any{"config": merge(inst.Config, map[string]string{"hostKeyFingerprint": srv.Fingerprint})}), nil)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/connectors/instances/"+inst.ID+"/sftp/poll", nil), nil)
	for _, a := range alerts(t, e, "integration.sftp") {
		if strings.Contains(a.Title, "connection failed") {
			t.Fatalf("connection alert not resolved: %+v", a)
		}
	}

	// Disabled instances and non-SFTP instances cannot be polled.
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/connectors/instances/"+inst.ID, map[string]any{"enabled": false}), nil)
	e.Must(http.StatusConflict, e.Do("POST", "/api/v1/connectors/instances/"+inst.ID+"/sftp/poll", nil), nil)
	var other2 struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/connectors/instances", map[string]any{"connector": "eg-eda"}), &other2)
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/connectors/instances/"+other2.ID+"/sftp/test", nil), nil)
	e.Must(http.StatusBadRequest, e.Do("GET", "/api/v1/connectors/instances/nope/files", nil), nil)
}

func TestSFTPInboundOnlyAndCaptureErrors(t *testing.T) {
	e := testkit.New(t)
	srv, err := mockhub.NewSFTPServer("partner", "pw", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	cfg := sftpConfig(srv)
	cfg["outboundDir"] = "" // inbound only: no reports are created
	cfg["errorDir"] = ""    // failed files stay in place
	var inst struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/connectors/instances", map[string]any{"connector": "sftp", "config": cfg}), &inst)
	ep, _ := sftpdrop.EndpointFromConfig(connector.Config(cfg))
	sess, err := sftpdrop.Dial(context.Background(), ep)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	// An event for serials that were never commissioned fails business validation.
	ghost := lib.Event{Type: lib.ObjectEvent, Action: "ADD", BizStep: "commissioning", EventTime: time.Now().UTC(), EventTimeZoneOffset: "+00:00",
		EPCList: []string{"urn:epc:id:sgtin:9506000.013435.GHOST1"}}
	doc, _ := lib.WriteJSON(lib.NewDocument([]lib.Event{ghost}))
	_, _ = sess.Put("inbound", "ghost.jsonld", doc)
	_, _ = sess.Put("inbound", "bad.edi", []byte("UNB+UNOC:3+X+Y'"))
	var res connectors.PollResult
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/connectors/instances/"+inst.ID+"/sftp/poll", nil), &res)
	if res.Failed != 2 || res.Processed != 0 {
		t.Fatalf("poll: %+v", res)
	}
	for _, f := range res.Files {
		if f.MovedTo != "" || f.Error == "" {
			t.Fatalf("file: %+v", f)
		}
	}
	if left, _ := sess.List("inbound"); len(left) != 2 {
		t.Fatalf("failed files must stay without an error folder: %+v", left)
	}
	// Re-polling the unchanged files does not duplicate alerts.
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/connectors/instances/"+inst.ID+"/sftp/poll", nil), &res)
	if as := alerts(t, e, "integration.sftp"); len(as) != 2 {
		t.Fatalf("alerts: %+v", as)
	}
	capture(t, e, lib.Event{Type: lib.ObjectEvent, Action: "OBSERVE", BizStep: "shipping", EventTime: time.Now().UTC(), EventTimeZoneOffset: "+00:00",
		EPCList: []string{"urn:epc:id:sscc:9506000.0000000001"}})
	e.Drain()
	var rs struct{ Items []connectors.Report }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/reports", nil), &rs)
	if len(rs.Items) != 0 {
		t.Fatalf("inbound-only instance must not drop files: %+v", rs.Items)
	}
	// Without inboundDir the poll is a no-op.
	cfg["inboundDir"] = ""
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/connectors/instances/"+inst.ID, map[string]any{"config": cfg}), nil)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/connectors/instances/"+inst.ID+"/sftp/poll", nil), &res)
	if len(res.Files) != 0 {
		t.Fatalf("poll: %+v", res)
	}
}

func merge(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
