package trace_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/testkit"
	"github.com/openspoor/openspoor/pkg/gs1"
)

func superExec(t *testing.T, e *testkit.Env, sql string, args ...any) {
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

func glnOfURN(t *testing.T, urn string) string {
	t.Helper()
	g, err := gs1.ParseSGLNURN(urn)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

type recallOut struct {
	ID     string
	Status string
	Scope  struct {
		Packs, OwnStock, AtHolders, NoDestination, NoCustodyRecord int
		Ended                                                      map[string]int
	}
	Holders []struct {
		ID, HolderID, GLN, Name, Status, Channel string
		Packs                                    int
		PartnerOnNetwork                         bool
	}
}

func TestLotRecall(t *testing.T) {
	e := testkit.New(t)
	u := scenario(t, e)
	viewer := userToken(t, e, "acme", "viewer@acme.test", "Viewer")
	globex, gtok := e.NewTenant("globex", "admin@globex.test")

	// Own site at A, partner B on the network, C only in the published directory.
	var org struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/orgs", map[string]any{"name": "Acme Pharma", "kind": "manufacturer"}), &org)
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/sites", map[string]any{"orgId": org.ID, "gln": glnOfURN(t, locA), "name": "Plant A"}), nil)
	var partner struct{ ID string }
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/partners", map[string]any{"name": "Wholesaler B", "gln": glnOfURN(t, locB), "kind": "wholesaler"}), &partner)
	superExec(t, e, `update partner set linked_tenant=$1 where id=$2`, globex.ID, partner.ID)
	superExec(t, e, `insert into directory_listing (tenant_id, name, gln, kind, published) values ($1, 'Directory Pharmacy C', $2, 'pharmacy', true)
		on conflict (tenant_id) do update set name=excluded.name, gln=excluded.gln, published=true`, globex.ID, glnOfURN(t, locC))
	capture(t, e, shipTo(time.Now().Add(-time.Hour), locC, u[5]))

	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/trace/recalls", map[string]any{"gtin": gtin, "lot": "L1"}), nil)
	e.Must(http.StatusBadRequest, e.Do("POST", "/api/v1/trace/recalls", map[string]any{"gtin": gtin, "lot": "L1", "reason": "x", "classification": "IV"}), nil)
	e.Must(http.StatusNotFound, e.Do("POST", "/api/v1/trace/recalls", map[string]any{"gtin": gtin, "lot": "NOPE", "reason": "x"}), nil)
	e.Must(http.StatusForbidden, e.Call(viewer, "POST", "/api/v1/trace/recalls", map[string]any{"gtin": gtin, "lot": "L1", "reason": "x"}), nil)

	var rc recallOut
	e.Must(http.StatusCreated, e.Do("POST", "/api/v1/trace/recalls", map[string]any{"gtin": gtin, "lot": "L1", "reason": "Potency out of specification", "classification": "II"}), &rc)
	if rc.Status != "open" || rc.Scope.Packs != 6 || rc.Scope.OwnStock != 1 || rc.Scope.AtHolders != 4 || rc.Scope.NoDestination != 1 || len(rc.Holders) != 2 {
		t.Fatalf("recall: %+v", rc)
	}
	byName := map[string]int{}
	for _, h := range rc.Holders {
		byName[h.Name] = h.Packs
	}
	if byName["Wholesaler B"] != 3 || byName["Directory Pharmacy C"] != 1 {
		t.Fatalf("holders: %+v", rc.Holders)
	}
	var b, c = rc.Holders[0], rc.Holders[1]
	if b.Name != "Wholesaler B" {
		b, c = c, b
	}
	if b.Status != "sent" || b.Channel != "network" || !b.PartnerOnNetwork || c.Status != "pending" {
		t.Fatalf("notification states: %+v %+v", b, c)
	}
	e.Must(http.StatusConflict, e.Do("POST", "/api/v1/trace/recalls", map[string]any{"gtin": gtin, "lot": "L1", "reason": "again"}), nil)

	// The packs now scan as recalled; the partner on the network has the notice in its inbox.
	var st struct{ Items []struct{ Status string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/trace/state?gtin="+gtin+"&lot=L1&status=recalled", nil), &st)
	if len(st.Items) != 6 {
		t.Fatalf("recalled packs: %d", len(st.Items))
	}
	var al struct {
		Items []struct{ Kind, Title string }
	}
	e.Must(http.StatusOK, e.Call(gtok, "GET", "/api/v1/alerts?kind=recall.received", nil), &al)
	if len(al.Items) != 1 || !strings.Contains(al.Items[0].Title, "ACME") {
		t.Fatalf("partner inbox: %+v", al)
	}
	// The partner notice is not duplicated by a repeated notify.
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/trace/recalls/"+rc.ID+"/notify", nil), nil)
	e.Must(http.StatusOK, e.Call(gtok, "GET", "/api/v1/alerts?kind=recall.received", nil), &al)
	if len(al.Items) != 1 {
		t.Fatalf("duplicate partner notice: %+v", al)
	}

	// Status tracking.
	path := "/api/v1/trace/recalls/" + rc.ID + "/holders/" + c.ID
	e.Must(http.StatusForbidden, e.Call(viewer, "POST", path+"/sent", map[string]any{"channel": "email"}), nil)
	e.Must(http.StatusOK, e.Do("POST", path+"/sent", map[string]any{"channel": "email", "note": "mailed pharmacist"}), &rc)
	e.Must(http.StatusConflict, e.Do("POST", path+"/sent", nil), nil)
	e.Must(http.StatusOK, e.Do("POST", path+"/acknowledge", map[string]any{"note": "phone confirmation"}), &rc)
	e.Must(http.StatusConflict, e.Do("POST", path+"/acknowledge", nil), nil)
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/trace/recalls/"+rc.ID+"/holders/"+b.ID+"/acknowledge", nil), &rc)
	e.Must(http.StatusBadRequest, e.Do("POST", path+"/acknowledge", map[string]any{"note": strings.Repeat("x", 1001)}), nil)
	e.Must(http.StatusNotFound, e.Do("POST", "/api/v1/trace/recalls/"+rc.ID+"/holders/"+rc.ID+"/acknowledge", nil), nil)
	for _, h := range rc.Holders {
		if h.Status != "acknowledged" {
			t.Fatalf("holder not acknowledged: %+v", h)
		}
	}

	// Refresh follows later movements: the pharmacy returns its packs.
	capture(t, e, shipTo(time.Now().Add(-time.Minute), "", u[5]))
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/trace/recalls/"+rc.ID+"/refresh", nil), &rc)
	if rc.Scope.AtHolders != 3 || rc.Scope.NoDestination != 2 {
		t.Fatalf("after refresh: %+v", rc.Scope)
	}

	// Reports: viewers may read; data and helper are separated; response time rules are a TODO.
	var rep struct {
		ReportType string
		Data       struct {
			Recall struct{ Lot string }
			Scope  struct{ Packs int }
		}
		Helper struct {
			Derived struct{ HoldersTotal, Acknowledged int }
		}
		ResponseTime struct {
			Configured bool
			Note       string
		}
		Limitations []string
	}
	r := e.Call(viewer, "GET", "/api/v1/trace/recalls/"+rc.ID+"/report", nil)
	e.Must(http.StatusOK, r, &rep)
	if rep.ReportType != "openspoor.lot_recall_report" || rep.Data.Recall.Lot != "L1" || rep.Data.Scope.Packs != 6 || rep.Helper.Derived.HoldersTotal != 2 ||
		rep.ResponseTime.Configured || !strings.Contains(rep.ResponseTime.Note, "TODO(spec)") || len(rep.Limitations) < 3 || !strings.Contains(r.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("json report: %+v", rep)
	}
	h := e.Call(viewer, "GET", "/api/v1/trace/recalls/"+rc.ID+"/report?format=html", nil)
	body := string(h.Body)
	if h.Status != 200 || !strings.HasPrefix(h.Header.Get("Content-Type"), "text/html") || !strings.Contains(body, "Data <span") || !strings.Contains(body, "Helper <span") ||
		!strings.Contains(body, "Wholesaler B") || !strings.Contains(body, "TODO(spec)") {
		t.Fatalf("html report: %d %s", h.Status, body)
	}
	e.Must(http.StatusBadRequest, e.Do("GET", "/api/v1/trace/recalls/"+rc.ID+"/report?format=pdf", nil), nil)

	// Close.
	e.Must(http.StatusOK, e.Do("POST", "/api/v1/trace/recalls/"+rc.ID+"/close", map[string]any{"reason": "all holders confirmed"}), &rc)
	if rc.Status != "closed" {
		t.Fatalf("close: %+v", rc)
	}
	e.Must(http.StatusConflict, e.Do("POST", "/api/v1/trace/recalls/"+rc.ID+"/close", nil), nil)
	e.Must(http.StatusConflict, e.Do("POST", path+"/acknowledge", nil), nil)
	e.Must(http.StatusConflict, e.Do("POST", "/api/v1/trace/recalls/"+rc.ID+"/refresh", nil), nil)
	var list struct{ Items []struct{ Status string } }
	e.Must(http.StatusOK, e.Call(viewer, "GET", "/api/v1/trace/recalls?status=closed", nil), &list)
	if len(list.Items) != 1 {
		t.Fatalf("list: %+v", list)
	}
	// Another tenant sees nothing of it.
	e.Must(http.StatusNotFound, e.Call(gtok, "GET", "/api/v1/trace/recalls/"+rc.ID, nil), nil)
	e.Must(http.StatusOK, e.Call(gtok, "GET", "/api/v1/trace/recalls", nil), &list)
	if len(list.Items) != 0 {
		t.Fatalf("leak: %+v", list)
	}

	var au struct{ Items []struct{ Action string } }
	e.Must(http.StatusOK, e.Do("GET", "/api/v1/audit?limit=500", nil), &au)
	n := map[string]int{}
	for _, a := range au.Items {
		n[a.Action]++
	}
	want := map[string]int{"trace.recall.create": 1, "trace.recall.notify": 1, "trace.recall.holder.sent": 1, "trace.recall.holder.acknowledged": 2, "trace.recall.refresh": 1, "trace.recall.close": 1}
	for k, v := range want {
		if n[k] != v {
			t.Fatalf("audit %s = %d, want %d (%v)", k, n[k], v, n)
		}
	}
}
