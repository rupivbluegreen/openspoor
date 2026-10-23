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

func TestAS2ProfileSealedAtRestAndIsolated(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	c, _, err := as2.GenerateStation("PEER-1", time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	certPEM := as2.CertPEM(c)
	var p partner
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", map[string]any{"name": "Peer", "endpoints": map[string]string{"as2Id": "PEER-1", "as2Url": "https://peer.example/as2"}}), &p)
	noID := partner{}
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", map[string]any{"name": "No id"}), &noID)

	// Not configured yet.
	problem(t, e.Do("GET", "/api/v1/partners/"+p.ID+"/as2", nil), 404, "not found")
	problem(t, e.Do("DELETE", "/api/v1/partners/"+p.ID+"/as2", nil), 404, "not found")
	// Validation.
	problem(t, e.Do("PUT", "/api/v1/partners/"+p.ID+"/as2", map[string]any{"certPem": "junk"}), 400, "PEM")
	problem(t, e.Do("PUT", "/api/v1/partners/"+p.ID+"/as2", map[string]any{"certPem": certPEM, "mdn": "later"}), 400, "sync or async")
	problem(t, e.Do("PUT", "/api/v1/partners/"+p.ID+"/as2", map[string]any{"certPem": certPEM, "mdnUrl": "gopher://x"}), 400, "http(s)")
	problem(t, e.Do("PUT", "/api/v1/partners/"+noID.ID+"/as2", map[string]any{"certPem": certPEM}), 409, "as2Id")
	problem(t, e.Do("PUT", "/api/v1/partners/00000000-0000-0000-0000-000000000001/as2", map[string]any{"certPem": certPEM}), 404, "not found")
	problem(t, e.Do("POST", "/api/v1/partners", map[string]any{"name": "Bad", "endpoints": map[string]string{"as2Id": "no spaces"}}), 400, "as2Id")
	expired, _, err := as2.GenerateStation("OLD", time.Hour, time.Now().Add(-48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	problem(t, e.Do("PUT", "/api/v1/partners/"+p.ID+"/as2", map[string]any{"certPem": as2.CertPEM(expired)}), 400, "valid")

	// Defaults, then overrides.
	var prof partners.AS2Profile
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/partners/"+p.ID+"/as2", map[string]any{"certPem": certPEM}), &prof)
	if !prof.Sign || !prof.Encrypt || prof.MDN != "sync" || prof.Fingerprint != as2.Fingerprint(c) || prof.CertPEM != certPEM {
		t.Fatalf("defaults: %+v", prof)
	}
	e.Must(http.StatusOK, e.Do("PUT", "/api/v1/partners/"+p.ID+"/as2", map[string]any{"certPem": certPEM, "sign": false, "encrypt": false, "mdn": "async", "mdnUrl": "https://peer.example/mdn"}), &prof)
	if prof.Sign || prof.Encrypt || prof.MDN != "async" || prof.MDNURL != "https://peer.example/mdn" {
		t.Fatalf("overrides: %+v", prof)
	}

	// The certificate is sealed with the tenant key; the audit entry names it by fingerprint only.
	su, err := pgx.Connect(ctx, e.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	defer su.Close(ctx)
	var sealed []byte
	if err := su.QueryRow(ctx, `select cert_sealed from partner_as2`).Scan(&sealed); err != nil || strings.Contains(string(sealed), "CERTIFICATE") {
		t.Fatalf("certificate stored in clear: %v", err)
	}
	var after string
	if err := su.QueryRow(ctx, `select after::text from audit_entry where action='partner.as2.set' order by seq desc limit 1`).Scan(&after); err != nil || strings.Contains(after, "BEGIN CERTIFICATE") || !strings.Contains(after, prof.Fingerprint) {
		t.Fatalf("audit entry: %v %s", err, after)
	}

	// LoadAS2 and ByAS2ID as the DSCSA transport uses them.
	if err := e.App.Deps.DB.Tx(ctx, e.Tenant.ID, func(tx pgx.Tx) error {
		pt, err := partners.ByAS2ID(ctx, tx, "PEER-1")
		if err != nil || pt.ID.String() != p.ID {
			t.Fatalf("ByAS2ID: %v %+v", err, pt)
		}
		if _, err := partners.ByAS2ID(ctx, tx, "NOBODY"); err == nil {
			t.Fatal("unknown as2 id found")
		}
		_, cert, err := partners.LoadAS2(ctx, tx, e.App.Deps.Keys, e.Tenant.ID, pt.ID)
		if err != nil || !cert.Equal(c) {
			t.Fatalf("LoadAS2: %v", err)
		}
		if _, _, err := partners.LoadAS2(ctx, tx, e.App.Deps.Keys, e.Tenant.ID, noIDParse(t, noID.ID)); err == nil {
			t.Fatal("profile of a partner without one")
		}
		// Another tenant's key cannot open it (crypto-shredding / isolation).
		other, _ := e.NewTenant("other", "o@other.test")
		if _, _, err := partners.LoadAS2(ctx, tx, e.App.Deps.Keys, other.ID, pt.ID); err == nil {
			t.Fatal("sealed certificate opened with another tenant's key")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Another tenant sees nothing of it; delete is audited.
	_, tok := e.NewTenant("third", "t@third.test")
	problem(t, e.Call(tok, "GET", "/api/v1/partners/"+p.ID+"/as2", nil), 404, "not found")
	e.Must(http.StatusNoContent, e.Do("DELETE", "/api/v1/partners/"+p.ID+"/as2", nil), nil)
	problem(t, e.Do("GET", "/api/v1/partners/"+p.ID+"/as2", nil), 404, "not found")
	var n int
	if err := su.QueryRow(ctx, `select count(*) from audit_entry where action='partner.as2.delete'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("delete audit: %d %v", n, err)
	}
}

func noIDParse(t *testing.T, s string) uuid.UUID {
	t.Helper()
	u, err := uuid.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
