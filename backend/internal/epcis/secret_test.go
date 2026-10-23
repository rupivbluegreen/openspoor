package epcis_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/openspoor/openspoor/internal/epcis"
	"github.com/openspoor/openspoor/internal/testkit"
)

func subscriptionRow(t *testing.T, e *testkit.Env, id string) (clear *string, sealed []byte) {
	t.Helper()
	c, err := pgx.Connect(context.Background(), e.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	if err := c.QueryRow(context.Background(), `select secret, secret_sealed from epcis_subscription where id=$1`, id).Scan(&clear, &sealed); err != nil {
		t.Fatal(err)
	}
	return
}

func TestWebhookSecretsAreSealedAtRest(t *testing.T) {
	e := testkit.New(t)
	var mu sync.Mutex
	sigs := map[string]string{}
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		sigs[r.Header.Get("X-OpenSpoor-Subscription")] = r.Header.Get("X-OpenSpoor-Signature") + "|" + string(b)
		mu.Unlock()
	}))
	defer hook.Close()

	var sub struct{ ID, Secret string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/epcis/subscriptions", map[string]any{"dest": hook.URL, "secret": "my-webhook-secret"}), &sub)
	if sub.Secret != "my-webhook-secret" {
		t.Fatal("the secret is shown once, at creation")
	}
	clear, sealed := subscriptionRow(t, e, sub.ID)
	if clear != nil || len(sealed) < 28 || strings.Contains(string(sealed), "my-webhook-secret") {
		t.Fatalf("stored in clear: clear=%v sealed=%d bytes", clear, len(sealed))
	}

	// A row written before sealing existed: signed with its clear secret, then sealed by the delivery.
	var legacy struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/epcis/subscriptions", map[string]any{"dest": hook.URL, "secret": "tmp"}), &legacy)
	c, err := pgx.Connect(context.Background(), e.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(context.Background(), `update epcis_subscription set secret='old-clear-secret', secret_sealed=null where id=$1`, legacy.ID); err != nil {
		t.Fatal(err)
	}
	c.Close(context.Background())

	setupProduct(t, e)
	shipping := ev("ObjectEvent", "OBSERVE", "urn:epcglobal:cbv:bizstep:shipping", "", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	shipping.EPCList = []string{sscc}
	x := doc(shipping)
	e.Must(http.StatusAccepted, e.Do("POST", "/api/v1/epcis/capture", x, "Content-Type", "application/ld+json"), nil)
	e.Drain()
	mu.Lock()
	for id, secret := range map[string]string{sub.ID: "my-webhook-secret", legacy.ID: "old-clear-secret"} {
		sig, body, _ := strings.Cut(sigs[id], "|")
		if body == "" || sig != epcis.Sign(secret, []byte(body)) {
			t.Errorf("subscription %s: signature does not match its secret", id)
		}
	}
	mu.Unlock()
	clear, sealed = subscriptionRow(t, e, legacy.ID)
	if clear != nil || len(sealed) == 0 {
		t.Fatal("delivery should seal the legacy secret")
	}

	// SealLegacySecrets handles what no delivery has touched yet.
	c, _ = pgx.Connect(context.Background(), e.SuperURL)
	_, _ = c.Exec(context.Background(), `update epcis_subscription set secret='again-clear', secret_sealed=null where id=$1`, legacy.ID)
	c.Close(context.Background())
	var mod *epcis.Module
	for _, m := range e.App.Modules {
		if x, ok := m.(*epcis.Module); ok {
			mod = x
		}
	}
	n, err := mod.SealLegacySecrets(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("sealed %d: %v", n, err)
	}
	if n, err = mod.SealLegacySecrets(context.Background()); err != nil || n != 0 {
		t.Fatalf("second run sealed %d: %v", n, err)
	}
	var a struct{ Items []struct{ Action string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?action=epcis.subscription", nil), &a)
	var sealedAudits int
	for _, it := range a.Items {
		if it.Action == "epcis.subscription.secret_sealed" {
			sealedAudits++
		}
	}
	if sealedAudits != 2 {
		t.Fatalf("every sealing is audited: %d", sealedAudits)
	}
}
