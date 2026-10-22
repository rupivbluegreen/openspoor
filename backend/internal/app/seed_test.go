package app_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/openspoor/openspoor/internal/testkit"
)

func TestSeedDemo(t *testing.T) {
	e := testkit.New(t)
	if err := e.App.Seed(context.Background(), 600, testkit.Password); err != nil {
		t.Fatal(err)
	}
	if err := e.App.Seed(context.Background(), 600, testkit.Password); err != nil {
		t.Fatal("seed must be idempotent:", err)
	}
	var tok struct{ Token string }
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "harbor", "email": "admin@harbor.example", "password": testkit.Password}), &tok)
	var st struct{ Serials int }
	e.Must(http.StatusOK, e.Call(tok.Token, "GET", "/api/v1/trace/stats", nil), &st)
	if st.Serials == 0 {
		t.Fatal("wholesaler received nothing")
	}
	e.Must(http.StatusOK, e.Call("", "POST", "/api/v1/auth/login", map[string]string{"tenant": "nordwind", "email": "admin@nordwind.example", "password": testkit.Password}), &tok)
	var reps struct{ Items []struct{ Status string } }
	e.Must(http.StatusOK, e.Call(tok.Token, "GET", "/api/v1/reports", nil), &reps)
	statuses := map[string]int{}
	for _, r := range reps.Items {
		statuses[r.Status]++
	}
	t.Logf("demo reports: %v", statuses)
	var acts struct{ Items []any }
	e.Must(http.StatusOK, e.Call(tok.Token, "GET", "/api/v1/agents/actions?status=proposed", nil), &acts)
	if len(acts.Items) == 0 {
		t.Fatal("no agent proposals in demo")
	}
}
