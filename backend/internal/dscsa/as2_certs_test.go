package dscsa_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/dscsa"
	"github.com/openspoor/openspoor/internal/testkit"
	"github.com/openspoor/openspoor/pkg/as2"
)

func superSQL(t *testing.T, e *testkit.Env, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	c, err := pgx.Connect(ctx, e.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	if _, err := c.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}

func problemIs(t *testing.T, r testkit.Response, status int, want string) {
	t.Helper()
	if r.Status != status || !strings.Contains(string(r.Body), want) {
		t.Fatalf("want HTTP %d containing %q, got %d: %s", status, want, r.Status, r.Body)
	}
}

func dscsaModule(t *testing.T, e *testkit.Env) *dscsa.Module {
	t.Helper()
	for _, m := range e.App.Modules {
		if d, ok := m.(*dscsa.Module); ok {
			return d
		}
	}
	t.Fatal("dscsa module missing")
	return nil
}

func runSchedule(t *testing.T, e *testkit.Env, name string) {
	t.Helper()
	for _, s := range dscsaModule(t, e).Schedules() {
		if s.Name == name {
			if err := s.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("schedule %s missing", name)
}

type stationView struct {
	As2Id, Fingerprint, Subject, Active, CertPem string
	NotBefore, NotAfter                          time.Time
	Next                                         *struct {
		Fingerprint, CertPem, Subject string
		SwitchAt, NotAfter            time.Time
	}
}

func getStation(t *testing.T, p *as2Pair, token string) stationView {
	t.Helper()
	var st stationView
	p.e.Must(http.StatusOK, p.e.Call(token, "GET", "/api/v1/as2/station", nil), &st)
	return st
}

func TestAS2StationRotation(t *testing.T) {
	p := newAS2Pair(t)
	e := p.e
	url := "/api/v1/as2/station/next"

	st := getStation(t, p, p.acme.token)
	if st.Next != nil || st.Active != "current" || !strings.Contains(st.Subject, "ACME-AS2") || st.NotBefore.IsZero() || st.Fingerprint != as2.Fingerprint(p.acme.st.Cert) {
		t.Fatalf("station: %+v", st)
	}

	// Validation and roles.
	_, other := e.NewTenant("initech", "admin@initech.test")
	problemIs(t, e.Call(other, "PUT", url, map[string]any{"generate": true}), 409, "set this node's AS2 station first")
	problemIs(t, e.Call(other, "DELETE", url, nil), 404, "not found")
	_, cp, kp := stationPEMs(t, "ACME-AS2")
	_, otherCert, _ := stationPEMs(t, "ACME-AS2")
	problemIs(t, e.Call(p.acme.token, "PUT", url, map[string]any{"certPem": "junk", "keyPem": kp}), 400, "PEM")
	problemIs(t, e.Call(p.acme.token, "PUT", url, map[string]any{"certPem": cp, "keyPem": "junk"}), 400, "private key")
	problemIs(t, e.Call(p.acme.token, "PUT", url, map[string]any{"certPem": otherCert, "keyPem": kp}), 400, "does not match")
	problemIs(t, e.Call(p.acme.token, "PUT", url, map[string]any{"generate": true, "switchAt": time.Now().Add(-time.Hour)}), 400, "past")
	problemIs(t, e.Call(p.acme.token, "PUT", url, map[string]any{"generate": true, "extra": 1}), 400, "invalid JSON body")
	st = getStation(t, p, p.acme.token)
	if st.Next != nil {
		t.Fatal("rejected requests stored a next certificate")
	}
	viewer := userToken(t, e, "viewer-rot@acme.test", "Viewer")
	ops := userToken(t, e, "ops-rot@acme.test", "Ops")
	for _, tok := range []string{viewer, ops} {
		problemIs(t, e.Call(tok, "PUT", url, map[string]any{"generate": true}), 403, "missing role")
		problemIs(t, e.Call(tok, "DELETE", url, nil), 403, "missing role")
	}
	problemIs(t, e.Call(p.acme.token, "DELETE", url, nil), 404, "not found")

	// Set a next certificate for a date two days away. Until then the current one signs.
	switchAt := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	st = stationView{}
	e.Must(http.StatusOK, e.Call(p.acme.token, "PUT", url, map[string]any{"generate": true, "switchAt": switchAt}), &st)
	if st.Next == nil || st.Active != "current" || !st.Next.SwitchAt.Equal(switchAt) || st.Next.Fingerprint == st.Fingerprint || !strings.Contains(st.Next.Subject, "ACME-AS2") || st.CertPem == "" {
		t.Fatalf("next: %+v", st)
	}
	nextPEM := st.Next.CertPem
	nextCert, err := as2.ParseCertPEM(nextPEM, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(e.Call(p.acme.token, "GET", "/api/v1/as2/station", nil).Body), "PRIVATE KEY") {
		t.Fatal("the station response carries a private key")
	}
	// The same certificate again is refused (it is the pending next, not the current).
	problemIs(t, e.Call(p.acme.token, "PUT", url, map[string]any{"certPem": as2.CertPEM(p.acme.st.Cert), "keyPem": kp}), 400, "does not match")
	if hasAction(t, e, p.acme.token, "as2.station.next.set") != 1 {
		t.Fatal("next certificate not audited")
	}
	before := p.shipments(t, p.globex.token, "inbound")
	p.sendOK(t, "PO-ROT-1", p.epcs[:1]) // signed by the current certificate, which Globex has pinned
	if got := p.shipments(t, p.globex.token, "inbound"); len(got) != len(before)+1 {
		t.Fatalf("inbound: %+v", got)
	}

	// Switch date reached (the date is moved into the past): Acme signs with the next certificate.
	// Globex does not know it yet, so the message is rejected ...
	superSQL(t, e, `update as2_station set switch_at = now() - interval '1 minute' where as2_id = 'ACME-AS2'`)
	st = getStation(t, p, p.acme.token)
	if st.Active != "next" {
		t.Fatalf("active after switch: %+v", st)
	}
	p.sendFails(t, "PO-ROT-2", p.epcs[1:2], "")
	// ... until Globex holds it as the partner's next certificate: both are accepted.
	gl := "/api/v1/partners/" + p.globex.partnerID + "/as2/next"
	e.Must(http.StatusOK, e.Call(p.globex.token, "PUT", gl, map[string]any{"certPem": nextPEM, "switchAt": time.Now().Add(24 * time.Hour)}), nil)
	p.sendOK(t, "PO-ROT-3", p.epcs[2:3])

	// Promotion by the schedule: the next certificate becomes the current one, audited as the system.
	runSchedule(t, e, dscsa.JobAS2StationCerts)
	st = getStation(t, p, p.acme.token)
	if st.Next != nil || st.Fingerprint != as2.Fingerprint(nextCert) || st.Active != "current" || st.CertPem != nextPEM {
		t.Fatalf("promotion: %+v", st)
	}
	var aud struct{ Items []struct{ Actor string } }
	e.Must(http.StatusOK, e.Call(p.acme.token, "GET", "/api/v1/audit?action=as2.station.next.promote", nil), &aud)
	if len(aud.Items) != 1 || aud.Items[0].Actor != "system" {
		t.Fatalf("promotion audit: %+v", aud)
	}
	runSchedule(t, e, dscsa.JobAS2StationCerts)
	if hasAction(t, e, p.acme.token, "as2.station.next.promote") != 1 {
		t.Fatal("second run promoted again")
	}
	p.sendOK(t, "PO-ROT-4", p.epcs[3:4]) // keeps working with the promoted key

	// Cancel a pending rotation.
	st = stationView{}
	e.Must(http.StatusOK, e.Call(p.acme.token, "PUT", url, map[string]any{"generate": true, "switchAt": time.Now().Add(72 * time.Hour)}), &st)
	if st.Next == nil {
		t.Fatal("no next to cancel")
	}
	e.Must(http.StatusNoContent, e.Call(p.acme.token, "DELETE", url, nil), nil)
	st = getStation(t, p, p.acme.token)
	if st.Next != nil || hasAction(t, e, p.acme.token, "as2.station.next.cancel") != 1 {
		t.Fatalf("cancel: %+v", st)
	}
	// Providing the certificate and key instead of generating them; "switchAt" absent means now.
	nc, nk, err := as2.GenerateStation("ACME-AS2", time.Hour*24*100, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	kpem, _ := as2.EncodeKeyPEM(nk)
	st = stationView{}
	e.Must(http.StatusOK, e.Call(p.acme.token, "PUT", url, map[string]any{"certPem": as2.CertPEM(nc), "keyPem": string(kpem)}), &st)
	if st.Next == nil || st.Next.Fingerprint != as2.Fingerprint(nc) || st.Active != "next" {
		t.Fatalf("immediate: %+v", st)
	}
	problemIs(t, e.Call(p.acme.token, "PUT", url, map[string]any{"generate": true}), 409, "already due")
	// Another tenant never sees it.
	var ov struct{ Station *struct{ Next *struct{} } }
	e.Must(http.StatusOK, e.Call(other, "GET", "/api/v1/as2/certificates", nil), &ov)
	if ov.Station != nil {
		t.Fatalf("another tenant sees a station: %+v", ov)
	}
}

func TestAS2StationDecryptsWithBothKeys(t *testing.T) {
	p := newAS2Pair(t)
	e := p.e
	var st stationView
	e.Must(http.StatusOK, e.Call(p.globex.token, "PUT", "/api/v1/as2/station/next", map[string]any{"generate": true, "switchAt": time.Now().Add(24 * time.Hour)}), &st)
	next, err := as2.ParseCertPEM(st.Next.CertPem, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	// Acme learned Globex's next certificate and encrypts to it before the switch date: Globex decrypts it.
	id := p.shipment(t, "PO-ROT-ENC", p.epcs[:2])
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/shipments/"+id+"/tits", nil), nil)
	doc := fetchTITS(t, p, id)
	w, err := as2.Build(p.acme.st, as2.Peer{ID: p.globex.id, Cert: next}, as2.Outgoing{MessageID: "enc-next@acme", ContentType: "application/ld+json", Payload: doc,
		Sign: true, Encrypt: true, MDN: as2.MDNSync, Filename: "t.json"})
	if err != nil {
		t.Fatal(err)
	}
	code, h, b := post(t, p.receive, w)
	m, err := as2.ParseMDN(h, b, as2.Peer{ID: p.globex.id, Cert: p.globex.st.Cert}, true, time.Now())
	if code != 200 || err != nil || !m.Processed {
		t.Fatalf("encrypted to the next certificate: %d %v %+v", code, err, m)
	}
	// Once the rotation is cancelled the key is gone again: the same kind of message is refused.
	e.Must(http.StatusNoContent, e.Call(p.globex.token, "DELETE", "/api/v1/as2/station/next", nil), nil)
	w2, _ := as2.Build(p.acme.st, as2.Peer{ID: p.globex.id, Cert: next}, as2.Outgoing{MessageID: "enc-next2@acme", ContentType: "application/ld+json", Payload: doc, Sign: true, Encrypt: true, MDN: as2.MDNSync})
	code, h, b = post(t, p.receive, w2)
	m, err = as2.ParseMDN(h, b, as2.Peer{ID: p.globex.id, Cert: p.globex.st.Cert}, true, time.Now())
	if code != 200 || err != nil || m.Processed || m.ErrorCode != as2.ErrDecryption {
		t.Fatalf("after cancel: %d %v %+v", code, err, m)
	}
}

func TestAS2StationExpiryAlertsAndOverview(t *testing.T) {
	p := newAS2Pair(t)
	e := p.e
	type overview struct {
		Station *struct {
			As2Id, Active string
			Current       struct{ Stage, Subject, Fingerprint string }
			Next          *struct{ Stage string }
			Warnings      []struct{ Code, Severity string }
		}
		Partners []struct {
			Name, As2Id string
			Current     struct{ Stage, Subject string }
			Next        *struct{ Stage string }
			Active      string
			Options     struct {
				SignatureAlgorithm, Encryption string
				Legacy                         bool
			}
			Warnings []struct{ Code string }
		}
		Summary        struct{ Partners, Expired, Expiring, Legacy, Rotations int }
		ThresholdsDays []int
	}
	var ov overview
	e.Must(http.StatusOK, e.Call(p.acme.token, "GET", "/api/v1/as2/certificates", nil), &ov)
	// Certificates made by the test last 24 hours, so both are inside the 7 day window already.
	if ov.Station == nil || ov.Station.As2Id != "ACME-AS2" || ov.Station.Current.Stage != "7d" || len(ov.Partners) != 1 || ov.Partners[0].Current.Stage != "7d" ||
		ov.Partners[0].Options.SignatureAlgorithm != "sha-256" || ov.Partners[0].Options.Encryption != "aes-256-cbc" || fmt.Sprint(ov.ThresholdsDays) != "[60 30 7]" {
		t.Fatalf("overview: %+v", ov)
	}
	if ov.Summary.Partners != 1 || ov.Summary.Expiring != 2 || ov.Summary.Expired != 0 {
		t.Fatalf("summary: %+v", ov.Summary)
	}

	// The schedule raises the station alert for the current stage, once.
	runSchedule(t, e, dscsa.JobAS2StationCerts)
	runSchedule(t, e, dscsa.JobAS2StationCerts)
	var al struct {
		Items []struct {
			Kind, Severity, Status string
			Detail                 map[string]any
		}
	}
	e.Must(http.StatusOK, e.Call(p.acme.token, "GET", "/api/v1/alerts?kind=dscsa.as2_cert_expiring", nil), &al)
	if len(al.Items) != 1 || al.Items[0].Detail["scope"] != "station" || al.Items[0].Detail["stage"] != "7d" || al.Items[0].Severity != "error" {
		t.Fatalf("alerts: %+v", al)
	}
	// Expired: a new alert, and the overview reports it.
	superSQL(t, e, `update as2_station set not_after = now() - interval '1 hour' where as2_id='ACME-AS2'`)
	runSchedule(t, e, dscsa.JobAS2StationCerts)
	e.Must(http.StatusOK, e.Call(p.acme.token, "GET", "/api/v1/alerts?kind=dscsa.as2_cert_expiring", nil), &al)
	if len(al.Items) != 2 {
		t.Fatalf("expired alert: %+v", al)
	}
	e.Must(http.StatusOK, e.Call(p.acme.token, "GET", "/api/v1/as2/certificates", nil), &ov)
	if ov.Station.Current.Stage != "expired" || ov.Summary.Expired != 1 {
		t.Fatalf("expired overview: %+v", ov)
	}
	// Replacing the certificate closes the alerts of the old one.
	_, certPEM, keyPEM := stationPEMs(t, "ACME-AS2")
	e.Must(http.StatusOK, e.Call(p.acme.token, "PUT", "/api/v1/as2/station", map[string]any{"as2Id": "ACME-AS2", "certPem": certPEM, "keyPem": keyPEM}), nil)
	e.Must(http.StatusOK, e.Call(p.acme.token, "GET", "/api/v1/alerts?kind=dscsa.as2_cert_expiring", nil), &al)
	if len(al.Items) != 0 {
		t.Fatalf("alerts after replacement: %+v", al)
	}

	// A pending rotation shows in the overview with its warning; the partner's next certificate too.
	sc, sk, err := as2.GenerateStation("ACME-AS2", 3*24*time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	skp, _ := as2.EncodeKeyPEM(sk)
	e.Must(http.StatusOK, e.Call(p.acme.token, "PUT", "/api/v1/as2/station/next", map[string]any{"certPem": as2.CertPEM(sc), "keyPem": string(skp), "switchAt": time.Now().Add(24 * time.Hour)}), nil)
	runSchedule(t, e, dscsa.JobAS2StationCerts) // the next certificate has 3 days left: its own alert
	e.Must(http.StatusOK, e.Call(p.acme.token, "GET", "/api/v1/alerts?kind=dscsa.as2_cert_expiring", nil), &al)
	if len(al.Items) != 2 {
		t.Fatalf("alerts for the node's certificates: %+v", al)
	}
	nextAlert := false
	for _, a := range al.Items {
		nextAlert = nextAlert || a.Detail["scope"] == "station_next"
	}
	if !nextAlert {
		t.Fatalf("no alert for the next node certificate: %+v", al)
	}
	gc, _, err := as2.GenerateStation("GLOBEX-AS2", time.Hour*24*400, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	e.Must(http.StatusOK, e.Call(p.acme.token, "PUT", "/api/v1/partners/"+p.acme.partnerID+"/as2/next", map[string]any{"certPem": as2.CertPEM(gc), "switchAt": time.Now().Add(48 * time.Hour)}), nil)
	e.Must(http.StatusOK, e.Call(p.acme.token, "PUT", "/api/v1/partners/"+p.acme.partnerID+"/as2/options", map[string]any{"signatureAlgorithm": "sha-1", "legacy": true}), nil)
	// A Viewer reads the overview (read-only); a role without access does not.
	viewer := userToken(t, e, "viewer-ov@acme.test", "Viewer")
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/as2/certificates", nil), &ov)
	if ov.Station.Next == nil || ov.Summary.Rotations != 2 || ov.Summary.Legacy != 1 || ov.Partners[0].Next == nil || ov.Partners[0].Active != "current" || !ov.Partners[0].Options.Legacy {
		t.Fatalf("rotation overview: %+v", ov)
	}
	problemIs(t, e.Call(userToken(t, e, "ser-ov@acme.test", "Serialization"), "GET", "/api/v1/as2/certificates", nil), 403, "missing role")
	problemIs(t, e.Call("", "GET", "/api/v1/as2/certificates", nil), 401, "")
	// The other side's overview is its own: no leakage across tenants.
	var gov overview
	e.Must(http.StatusOK, e.Call(p.globex.token, "GET", "/api/v1/as2/certificates", nil), &gov)
	if gov.Station == nil || gov.Station.As2Id != "GLOBEX-AS2" || len(gov.Partners) != 1 || gov.Partners[0].As2Id != "ACME-AS2" || gov.Station.Next != nil || gov.Summary.Legacy != 0 || gov.Partners[0].Next != nil {
		t.Fatalf("globex overview: %+v", gov)
	}
}
