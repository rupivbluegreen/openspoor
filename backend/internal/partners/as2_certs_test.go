package partners_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/partners"
	"github.com/openspoor/openspoor/internal/testkit"
	"github.com/openspoor/openspoor/pkg/as2"
)

func newCert(t *testing.T, id string, valid time.Duration, from time.Time) (string, string) {
	t.Helper()
	c, _, err := as2.GenerateStation(id, valid, from)
	if err != nil {
		t.Fatal(err)
	}
	return as2.CertPEM(c), as2.Fingerprint(c)
}

// as2Partner creates a partner with an AS2 identifier and a profile with a 200 day certificate.
func as2Partner(t *testing.T, e *testkit.Env, token, name, as2ID string) (id, certPEM, fp string) {
	t.Helper()
	var p partner
	e.Must(http.StatusCreated, e.Call(token, "POST", "/api/v1/partners", map[string]any{"name": name, "endpoints": map[string]string{"as2Id": as2ID, "as2Url": "https://x.example/as2"}}), &p)
	certPEM, fp = newCert(t, as2ID, 200*24*time.Hour, time.Now())
	e.Must(http.StatusOK, e.Call(token, "PUT", "/api/v1/partners/"+p.ID+"/as2", map[string]any{"certPem": certPEM}), nil)
	return p.ID, certPEM, fp
}

func actionCount(t *testing.T, e *testkit.Env, action string) int {
	t.Helper()
	var aud struct{ Total int }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?limit=500&action="+action, nil), &aud)
	return aud.Total
}

func alertCount(t *testing.T, e *testkit.Env, kind, status string) int {
	t.Helper()
	var l struct {
		Items []struct{ Kind, Status string }
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?status=all", nil), &l)
	n := 0
	for _, a := range l.Items {
		if a.Kind == kind && (status == "" || a.Status == status) {
			n++
		}
	}
	return n
}

func TestAS2OptionsDefaultsValidationAndLegacy(t *testing.T) {
	e := testkit.New(t)
	id, certPEM, fp := as2Partner(t, e, e.Token, "Peer", "OPT-PEER")
	var prof partners.AS2Profile
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/partners/"+id+"/as2", nil), &prof)
	if prof.SignatureAlgorithm != "sha-256" || prof.MDNSignatureAlgorithm != "sha-256" || prof.Encryption != "aes-256-cbc" || prof.Compress || prof.Legacy {
		t.Fatalf("defaults: %+v", prof)
	}
	// The certificate details are parsed and stored.
	if prof.Fingerprint != fp || !strings.Contains(prof.Subject, "OPT-PEER") || prof.NotBefore == nil || prof.NotBefore.After(time.Now()) || prof.NotAfter.Before(time.Now().Add(190*24*time.Hour)) {
		t.Fatalf("certificate details: %+v", prof)
	}

	put := func(body map[string]any) testkit.Response {
		body["certPem"] = certPEM
		return e.Do("PUT", "/api/v1/partners/"+id+"/as2", body)
	}
	// Negative cases: unknown values, SHA-1 without the explicit flag, legacy cannot be dropped while SHA-1 is configured.
	problem(t, put(map[string]any{"signatureAlgorithm": "md5"}), 400, "signatureAlgorithm")
	problem(t, put(map[string]any{"signatureAlgorithm": "SHA256"}), 400, "signatureAlgorithm")
	problem(t, put(map[string]any{"mdnSignatureAlgorithm": "sha-224"}), 400, "mdnSignatureAlgorithm")
	problem(t, put(map[string]any{"encryption": "3des-cbc"}), 400, "encryption")
	problem(t, put(map[string]any{"encryption": "aes-256-gcm"}), 400, "encryption")
	problem(t, put(map[string]any{"signatureAlgorithm": "sha-1"}), 400, "legacy")
	problem(t, put(map[string]any{"mdnSignatureAlgorithm": "sha-1"}), 400, "legacy")
	problem(t, put(map[string]any{"signatureAlgorithm": "sha-1", "legacy": false}), 400, "legacy")
	problem(t, put(map[string]any{"bogus": true}), 400, "invalid JSON body")
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/partners/"+id+"/as2", nil), &prof)
	if prof.SignatureAlgorithm != "sha-256" || prof.Legacy {
		t.Fatalf("rejected requests changed the profile: %+v", prof)
	}
	if actionCount(t, e, "partner.as2.legacy.set") != 0 || alertCount(t, e, "dscsa.as2_legacy", "") != 0 {
		t.Fatal("legacy audit / alert without the flag")
	}

	// Each option on its own; absent options keep their value across a certificate re-PUT.
	e.Must(http.StatusOK, put(map[string]any{"signatureAlgorithm": "sha-384", "mdnSignatureAlgorithm": "sha-512", "encryption": "aes-128-cbc", "compress": true}), &prof)
	if prof.SignatureAlgorithm != "sha-384" || prof.MDNSignatureAlgorithm != "sha-512" || prof.Encryption != "aes-128-cbc" || !prof.Compress || prof.Legacy {
		t.Fatalf("options: %+v", prof)
	}
	e.Must(http.StatusOK, put(map[string]any{}), &prof)
	if prof.SignatureAlgorithm != "sha-384" || !prof.Compress {
		t.Fatalf("options lost on re-PUT: %+v", prof)
	}

	// SHA-1 needs the explicit legacy flag, which is audited and raises an alert.
	e.Must(http.StatusOK, put(map[string]any{"signatureAlgorithm": "sha-1", "legacy": true}), &prof)
	if prof.SignatureAlgorithm != "sha-1" || !prof.Legacy {
		t.Fatalf("legacy: %+v", prof)
	}
	if actionCount(t, e, "partner.as2.legacy.set") != 1 || alertCount(t, e, "dscsa.as2_legacy", "open") != 1 {
		t.Fatal("legacy flag not audited / alerted")
	}
	// Setting it again changes nothing and is not audited again.
	e.Must(http.StatusOK, put(map[string]any{"legacy": true}), &prof)
	if actionCount(t, e, "partner.as2.legacy.set") != 1 || alertCount(t, e, "dscsa.as2_legacy", "") != 1 {
		t.Fatal("unchanged legacy flag audited again")
	}
	// The overview warns.
	ctx := context.Background()
	if err := e.App.Deps.DB.Tx(ctx, e.Tenant.ID, func(tx pgx.Tx) error {
		ov, err := partners.AS2Overview(ctx, tx, e.App.Deps.Keys, e.Tenant.ID, time.Now())
		if err != nil || len(ov) != 1 || !ov[0].Options.Legacy {
			t.Fatalf("overview: %+v %v", ov, err)
		}
		found := false
		for _, w := range ov[0].Warnings {
			found = found || w.Code == "legacy"
		}
		if !found {
			t.Fatalf("no legacy warning: %+v", ov[0].Warnings)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// The options endpoint: only admin / integration write; unknown profile is 404; dropping the
	// flag while SHA-1 is configured is refused; dropping both clears the alert.
	viewer := login(t, e, "viewer@acme.test", "Viewer")
	ops := login(t, e, "ops@acme.test", "Ops")
	problem(t, e.Call(viewer, "PUT", "/api/v1/partners/"+id+"/as2/options", map[string]any{"compress": false}), 403, "missing role")
	problem(t, e.Call(ops, "PUT", "/api/v1/partners/"+id+"/as2/options", map[string]any{"compress": false}), 403, "missing role")
	problem(t, e.Do("PUT", "/api/v1/partners/00000000-0000-0000-0000-000000000001/as2/options", map[string]any{"compress": false}), 404, "not found")
	problem(t, e.Do("PUT", "/api/v1/partners/"+id+"/as2/options", map[string]any{"legacy": false}), 400, "legacy")
	problem(t, e.Do("PUT", "/api/v1/partners/"+id+"/as2/options", map[string]any{"encryption": "des"}), 400, "encryption")
	problem(t, e.Do("PUT", "/api/v1/partners/zz/as2/options", map[string]any{}), 400, "invalid id")
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/partners/"+id+"/as2/options", map[string]any{"signatureAlgorithm": "sha-256", "mdnSignatureAlgorithm": "sha-256", "legacy": false, "compress": false}), &prof)
	if prof.SignatureAlgorithm != "sha-256" || prof.Legacy || prof.Compress || prof.Encryption != "aes-128-cbc" {
		t.Fatalf("options endpoint: %+v", prof)
	}
	if actionCount(t, e, "partner.as2.legacy.set") != 2 || actionCount(t, e, "partner.as2.options.set") != 1 || alertCount(t, e, "dscsa.as2_legacy", "open") != 0 {
		t.Fatal("legacy flag cleared without audit / alert resolution")
	}
	// The database rejects SHA-1 without the flag even if the API were bypassed.
	su, err := pgx.Connect(ctx, e.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	defer su.Close(ctx)
	if _, err := su.Exec(ctx, `update partner_as2 set signature_alg='sha-1' where legacy=false`); err == nil {
		t.Fatal("database accepted SHA-1 without legacy")
	}
	// Deleting the profile resolves the legacy alert too.
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/partners/"+id+"/as2/options", map[string]any{"signatureAlgorithm": "sha-1", "legacy": true}), nil)
	e.Must(http.StatusNoContent, e.Do("DELETE", "/api/v1/partners/"+id+"/as2", nil), nil)
	if alertCount(t, e, "dscsa.as2_legacy", "open") != 0 {
		t.Fatal("legacy alert survives the profile")
	}
}

func TestAS2RotationFlow(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	id, certPEM, fp := as2Partner(t, e, e.Token, "Peer", "ROT-PEER")
	uid := uuid.MustParse(id)
	url := "/api/v1/partners/" + id + "/as2/next"

	nextPEM, nextFP := newCert(t, "ROT-PEER", 300*24*time.Hour, time.Now())
	later := time.Now().Add(10 * 24 * time.Hour).UTC().Truncate(time.Second)

	// Validation.
	problem(t, e.Do("PUT", url, map[string]any{"certPem": "junk"}), 400, "PEM")
	problem(t, e.Do("PUT", url, map[string]any{"certPem": certPEM}), 400, "current certificate")
	expired, _ := newCert(t, "OLD", time.Hour, time.Now().Add(-48*time.Hour))
	problem(t, e.Do("PUT", url, map[string]any{"certPem": expired}), 400, "expired")
	future, _ := newCert(t, "FUT", 24*time.Hour, time.Now().Add(72*time.Hour))
	problem(t, e.Do("PUT", url, map[string]any{"certPem": future, "switchAt": time.Now().Add(24 * time.Hour)}), 400, "not valid yet")
	short, _ := newCert(t, "SHORT", 5*24*time.Hour, time.Now())
	problem(t, e.Do("PUT", url, map[string]any{"certPem": short, "switchAt": later}), 400, "before the switch date")
	problem(t, e.Do("PUT", url, map[string]any{"certPem": nextPEM, "switchAt": time.Now().Add(-time.Hour)}), 400, "past")
	problem(t, e.Do("PUT", url, map[string]any{"certPem": nextPEM, "other": 1}), 400, "invalid JSON body")
	problem(t, e.Do("PUT", "/api/v1/partners/00000000-0000-0000-0000-000000000001/as2/next", map[string]any{"certPem": nextPEM}), 404, "not found")
	problem(t, e.Do("DELETE", url, nil), 404, "not found")
	viewer := login(t, e, "viewer@acme.test", "Viewer")
	problem(t, e.Call(viewer, "PUT", url, map[string]any{"certPem": nextPEM}), 403, "missing role")
	problem(t, e.Call(viewer, "DELETE", url, nil), 403, "missing role")

	// Set the next certificate for a future date: both verify, the current one still encrypts.
	var prof partners.AS2Profile
	e.Must(http.StatusOK, e.Do("PUT", url, map[string]any{"certPem": nextPEM, "switchAt": later}), &prof)
	if prof.Next == nil || prof.Next.Fingerprint != nextFP || !prof.Next.SwitchAt.Equal(later) || prof.Fingerprint != fp || !strings.Contains(prof.Next.Subject, "ROT-PEER") {
		t.Fatalf("next: %+v", prof)
	}
	if actionCount(t, e, "partner.as2.next.set") != 1 {
		t.Fatal("next certificate not audited")
	}
	var audit struct {
		Items []struct{ After map[string]any }
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=partner.as2.next.set", nil), &audit)
	if b, _ := e.Do("GET", "/api/v1/audit?action=partner.as2.next.set", nil).Body, 0; strings.Contains(string(b), "BEGIN CERTIFICATE") {
		t.Fatal("audit entry carries the certificate")
	}
	load := func(at time.Time) (partners.AS2Profile, partners.AS2Certs) {
		var p partners.AS2Profile
		var c partners.AS2Certs
		if err := e.App.Deps.DB.Tx(ctx, e.Tenant.ID, func(tx pgx.Tx) error {
			var err error
			p, c, err = partners.LoadAS2Certs(ctx, tx, e.App.Deps.Keys, e.Tenant.ID, uid, at)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return p, c
	}
	_, c := load(time.Now())
	if as2.Fingerprint(c.Active) != fp || as2.Fingerprint(c.Other) != nextFP || c.Peer("ROT-PEER").Other == nil {
		t.Fatal("before the switch: current is active, next is accepted")
	}
	// After the switch date the next certificate is active and the current one is still accepted.
	p, c := load(later.Add(time.Minute))
	if as2.Fingerprint(c.Active) != nextFP || as2.Fingerprint(c.Other) != fp || !p.SwitchDue(later.Add(time.Minute)) {
		t.Fatal("after the switch: next is active")
	}
	// LoadAS2 follows the clock the same way (no rotation: the one certificate).
	// Replacing the next certificate while one is pending is allowed (and audited); the current one is refused again.
	nextPEM2, nextFP2 := newCert(t, "ROT-PEER", 300*24*time.Hour, time.Now())
	e.Must(http.StatusOK, e.Do("PUT", url, map[string]any{"certPem": nextPEM2, "switchAt": later}), &prof)
	if prof.Next.Fingerprint != nextFP2 {
		t.Fatalf("replace next: %+v", prof.Next)
	}

	// Cancel, then set again for "now" (no switchAt): active immediately, promoted by the job.
	e.Must(http.StatusNoContent, e.Do("DELETE", url, nil), nil)
	var after partners.AS2Profile
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/partners/"+id+"/as2", nil), &after)
	if after.Next != nil || actionCount(t, e, "partner.as2.next.cancel") != 1 {
		t.Fatalf("cancel: %+v", after)
	}
	prof = partners.AS2Profile{}
	e.Must(http.StatusOK, e.Do("PUT", url, map[string]any{"certPem": nextPEM}), &prof)
	if prof.Next == nil || time.Since(prof.Next.SwitchAt) > time.Minute || prof.Next.SwitchAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("immediate switch: %+v", prof.Next)
	}
	problem(t, e.Do("PUT", url, map[string]any{"certPem": nextPEM2}), 409, "already due")
	_, c = load(time.Now().Add(time.Second))
	if as2.Fingerprint(c.Active) != nextFP {
		t.Fatal("immediate switch not active")
	}
	if err := module(e).Schedules()[1].Run(ctx); err != nil {
		t.Fatal(err)
	}
	var promoted partners.AS2Profile
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/partners/"+id+"/as2", nil), &promoted)
	if promoted.Fingerprint != nextFP || promoted.Next != nil || promoted.CertPEM != nextPEM {
		t.Fatalf("promotion: %+v", promoted)
	}
	if actionCount(t, e, "partner.as2.next.promote") != 1 {
		t.Fatal("promotion not audited")
	}
	var promo struct{ Items []struct{ Actor string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=partner.as2.next.promote", nil), &promo)
	if len(promo.Items) != 1 || promo.Items[0].Actor != "system" {
		t.Fatalf("promotion actor: %+v", promo)
	}
	// A second run changes nothing.
	if err := module(e).Schedules()[1].Run(ctx); err != nil || actionCount(t, e, "partner.as2.next.promote") != 1 {
		t.Fatalf("second run: %v", err)
	}

	// Another tenant cannot see or change it.
	_, other := e.NewTenant("globex", "admin@globex.test")
	problem(t, e.Call(other, "PUT", url, map[string]any{"certPem": nextPEM2}), 404, "not found")
	problem(t, e.Call(other, "DELETE", url, nil), 404, "not found")
}

func TestAS2ExpiryAlerts(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	id, _, fp := as2Partner(t, e, e.Token, "Peer", "EXP-PEER")
	run := func() {
		t.Helper()
		if err := module(e).Schedules()[1].Run(ctx); err != nil {
			t.Fatal(err)
		}
	}
	setExpiry := func(d time.Duration) {
		superExec(t, e, `update partner_as2 set not_after=$1`, time.Now().Add(d))
	}
	count := func() int { return alertCount(t, e, "dscsa.as2_cert_expiring", "") }

	// 200 days left: nothing. Each threshold raises one alert, once.
	run()
	if count() != 0 {
		t.Fatal("alert for a certificate far from expiry")
	}
	for i, tc := range []struct {
		left     time.Duration
		stage    string
		severity string
	}{{59 * 24 * time.Hour, "60d", "warning"}, {29 * 24 * time.Hour, "30d", "warning"}, {6 * 24 * time.Hour, "7d", "error"}, {-time.Hour, "expired", "error"}} {
		setExpiry(tc.left)
		run()
		run() // the second run must not raise it again
		var l struct {
			Items []struct {
				Kind, Severity, Title, Status string
				Detail                        map[string]any
			}
		}
		e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?status=all", nil), &l)
		if count() != i+1 {
			t.Fatalf("stage %s: %d alerts", tc.stage, count())
		}
		got := false
		for _, a := range l.Items {
			if a.Kind == "dscsa.as2_cert_expiring" && a.Detail["stage"] == tc.stage {
				got = a.Severity == tc.severity && a.Detail["fingerprint"] == fp && a.Detail["subjectId"] == id && a.Title != ""
			}
		}
		if !got {
			t.Fatalf("stage %s: %+v", tc.stage, l.Items)
		}
	}
	if actionCount(t, e, "as2.cert.expiry_alert") != 4 {
		t.Fatal("expiry alerts not audited")
	}

	// A resolved alert is not raised again by the next run.
	var l struct {
		Items []struct{ ID, Kind, Status string }
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?status=all", nil), &l)
	for _, a := range l.Items {
		if a.Kind == "dscsa.as2_cert_expiring" && a.Status == "open" {
			e.Must(http.StatusOK, e.Do("POST", "/api/v1/alerts/"+a.ID+"/resolve", nil), nil)
		}
	}
	run()
	if alertCount(t, e, "dscsa.as2_cert_expiring", "open") != 0 || count() != 4 {
		t.Fatalf("resolved alert came back: open %d total %d", alertCount(t, e, "dscsa.as2_cert_expiring", "open"), count())
	}

	// Replacing the certificate closes the open alerts of the old one and starts afresh.
	setExpiry(5 * 24 * time.Hour)
	run() // 7d alert for the (unchanged fingerprint) is already tracked: nothing new
	newPEM, _ := newCert(t, "EXP-PEER", 200*24*time.Hour, time.Now())
	superExec(t, e, `update alert set status='open', resolved_at=null where kind='dscsa.as2_cert_expiring' and detail->>'stage'='expired'`)
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/partners/"+id+"/as2", map[string]any{"certPem": newPEM}), nil)
	if alertCount(t, e, "dscsa.as2_cert_expiring", "open") != 0 {
		t.Fatal("replacing the certificate left its alerts open")
	}

	// The next certificate has its own alerts, and a cancelled one is closed.
	nextPEM, nextFP := newCert(t, "EXP-PEER", 20*24*time.Hour, time.Now())
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/partners/"+id+"/as2/next", map[string]any{"certPem": nextPEM, "switchAt": time.Now().Add(24 * time.Hour)}), nil)
	run()
	var nl struct {
		Items []struct {
			Kind, Status string
			Detail       map[string]any
		}
	}
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?status=all", nil), &nl)
	seen := false
	for _, a := range nl.Items {
		if a.Kind == "dscsa.as2_cert_expiring" && a.Detail["fingerprint"] == nextFP && a.Status == "open" && a.Detail["scope"] == partners.ScopePartnerNext {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("no alert for the next certificate: %+v", nl.Items)
	}
	e.Must(http.StatusNoContent, e.Do("DELETE", "/api/v1/partners/"+id+"/as2/next", nil), nil)
	for _, a := range func() []struct{ Kind, Status string } {
		var x struct {
			Items []struct{ Kind, Status string }
		}
		e.Must(http.StatusOK, e.Do("GET", "/api/v1/alerts?status=all", nil), &x)
		return x.Items
	}() {
		if a.Kind == "dscsa.as2_cert_expiring" && a.Status == "open" {
			t.Fatal("cancelling the next certificate left its alert open")
		}
	}
}

func TestSwitchDateAndWarnings(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	if d, err := partners.SwitchDate(nil, now); err != nil || !d.Equal(now) {
		t.Fatalf("nil: %v %v", d, err)
	}
	zero := time.Time{}
	if d, err := partners.SwitchDate(&zero, now); err != nil || !d.Equal(now) {
		t.Fatalf("zero: %v %v", d, err)
	}
	soon := now.Add(-30 * time.Second)
	if d, err := partners.SwitchDate(&soon, now); err != nil || !d.Equal(now) {
		t.Fatalf("slack: %v %v", d, err)
	}
	past := now.Add(-time.Hour)
	if _, err := partners.SwitchDate(&past, now); err == nil {
		t.Fatal("past date accepted")
	}
	fut := now.Add(48 * time.Hour)
	if d, err := partners.SwitchDate(&fut, now); err != nil || !d.Equal(fut) {
		t.Fatalf("future: %v %v", d, err)
	}

	view := func(left time.Duration) partners.AS2CertView {
		return partners.CertView("CN=x", now.Add(-time.Hour), now.Add(left), "ff", now)
	}
	for _, tc := range []struct {
		left time.Duration
		code string
		sev  string
	}{{100 * 24 * time.Hour, "", ""}, {50 * 24 * time.Hour, "expiring_60d", "warning"}, {20 * 24 * time.Hour, "expiring_30d", "warning"}, {3 * 24 * time.Hour, "expiring_7d", "error"}, {-time.Hour, "expired", "error"}} {
		ws := partners.CertWarnings(view(tc.left), "certificate")
		if tc.code == "" {
			if len(ws) != 0 {
				t.Fatalf("%v: %+v", tc.left, ws)
			}
			continue
		}
		if len(ws) != 1 || ws[0].Code != tc.code || ws[0].Severity != tc.sev || ws[0].Message == "" {
			t.Fatalf("%v: %+v", tc.left, ws)
		}
	}
}

func TestAS2OverviewBackfillsOldRows(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	id, _, fp := as2Partner(t, e, e.Token, "Old row", "OLD-ROW")
	// A row stored before the subject and notBefore columns existed is read from the certificate.
	superExec(t, e, `update partner_as2 set subject='', not_before=null where partner_id=$1`, id)
	if err := e.App.Deps.DB.Tx(ctx, e.Tenant.ID, func(tx pgx.Tx) error {
		ov, err := partners.AS2Overview(ctx, tx, e.App.Deps.Keys, e.Tenant.ID, time.Now())
		if err != nil || len(ov) != 1 || ov[0].Current.Fingerprint != fp || !strings.Contains(ov[0].Current.Subject, "OLD-ROW") || ov[0].Current.NotBefore.IsZero() || ov[0].Active != "current" {
			t.Fatalf("overview: %+v %v", ov, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
