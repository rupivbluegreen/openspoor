package jobs_test

import (
	"context"
	"errors"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/openspoor/openspoor/internal/core/jobs"
	"github.com/openspoor/openspoor/internal/testkit"
)

type jobRow struct {
	status, lastError string
	attempts          int
	runAt             time.Time
}

func setup(t *testing.T) (*testkit.Env, *jobs.Runner) {
	t.Helper()
	e := testkit.New(t)
	return e, jobs.NewRunner(e.App.Deps.DB.Pool, 0)
}

func enqueue(t *testing.T, e *testkit.Env, kind string, payload any, opts ...jobs.Opt) int64 {
	t.Helper()
	var id int64
	err := e.App.Deps.DB.OperatorTx(context.Background(), func(tx pgx.Tx) error {
		var err error
		id, err = jobs.Enqueue(context.Background(), tx, nil, kind, payload, opts...)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func row(t *testing.T, e *testkit.Env, id int64) jobRow {
	t.Helper()
	var r jobRow
	if err := e.App.Deps.DB.Pool.QueryRow(context.Background(), `select status, last_error, attempts, run_at from job where id=$1`, id).
		Scan(&r.status, &r.lastError, &r.attempts, &r.runAt); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRunOnceOutcomes(t *testing.T) {
	e, r := setup(t)
	ctx := context.Background()

	if n, err := r.RunOnce(ctx); n != 0 || err != nil {
		t.Fatalf("no handlers: n=%d err=%v", n, err)
	}

	var gotPayload string
	r.Handle("t.ok", func(_ context.Context, j jobs.Job) error {
		gotPayload = string(j.Payload)
		return nil
	})
	r.Handle("t.retry", func(context.Context, jobs.Job) error { return errors.New("transient") })
	r.Handle("t.perm", func(context.Context, jobs.Job) error { return jobs.Permanent{Err: errors.New("bad input")} })
	r.Handle("t.panic", func(context.Context, jobs.Job) error { panic("kaboom") })
	kinds := r.Kinds()
	sort.Strings(kinds)
	if len(kinds) != 4 || kinds[0] != "t.ok" {
		t.Fatalf("kinds: %v", kinds)
	}

	ok := enqueue(t, e, "t.ok", map[string]int{"a": 1})
	retry := enqueue(t, e, "t.retry", nil, jobs.MaxAttempts(2))
	perm := enqueue(t, e, "t.perm", nil)
	pan := enqueue(t, e, "t.panic", nil)
	later := enqueue(t, e, "t.ok", nil, jobs.RunAt(time.Now().Add(time.Hour)))

	if err := r.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if gotPayload != `{"a": 1}` && gotPayload != `{"a":1}` {
		t.Fatalf("payload: %q", gotPayload)
	}
	if s := row(t, e, ok); s.status != "done" || s.attempts != 1 {
		t.Fatalf("ok job: %+v", s)
	}
	s := row(t, e, retry)
	if s.status != "queued" || s.lastError != "transient" || s.attempts != 1 || !s.runAt.After(time.Now()) {
		t.Fatalf("retry job after first attempt: %+v", s)
	}
	if s := row(t, e, perm); s.status != "failed" || s.lastError != "bad input" || s.attempts != 1 {
		t.Fatalf("permanent job: %+v", s)
	}
	if s := row(t, e, pan); s.status != "failed" || s.lastError != "panic: kaboom" {
		t.Fatalf("panicking job: %+v", s)
	}
	if s := row(t, e, later); s.status != "queued" || s.attempts != 0 {
		t.Fatalf("delayed job ran early: %+v", s)
	}

	// Second attempt hits max_attempts and fails for good.
	if _, err := e.App.Deps.DB.Pool.Exec(ctx, `update job set run_at=now() where id=$1`, retry); err != nil {
		t.Fatal(err)
	}
	if err := r.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if s := row(t, e, retry); s.status != "failed" || s.attempts != 2 {
		t.Fatalf("retry job after max attempts: %+v", s)
	}
}

func TestDedupeAndMissingHandler(t *testing.T) {
	e, r := setup(t)
	ctx := context.Background()
	a := enqueue(t, e, "t.d", nil, jobs.Dedupe("k"), jobs.RunAt(time.Now().Add(time.Hour)))
	b := enqueue(t, e, "t.d", nil, jobs.Dedupe("k"))
	if a != b {
		t.Fatalf("dedupe produced two jobs: %d %d", a, b)
	}
	if s := row(t, e, a); s.runAt.After(time.Now()) {
		t.Fatalf("dedupe did not pull run_at forward: %v", s.runAt)
	}
	if _, err := jobs.Enqueue(ctx, nil, nil, "t.d", make(chan int)); err == nil {
		t.Fatal("unmarshalable payload accepted")
	}

	// A handler registered as nil fails the job permanently.
	r.Handle("t.d", nil)
	if err := r.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if s := row(t, e, a); s.status != "failed" || s.lastError != "no handler for t.d" {
		t.Fatalf("nil handler: %+v", s)
	}
	// Once done, the dedupe key is free again.
	if c := enqueue(t, e, "t.d", nil, jobs.Dedupe("k")); c == a {
		t.Fatal("dedupe key reused after completion")
	}
}

func TestRunProcessesUntilCancelled(t *testing.T) {
	e, r := setup(t)
	var done atomic.Int32
	r.Handle("t.run", func(context.Context, jobs.Job) error {
		done.Add(1)
		return nil
	})
	for i := 0; i < 3; i++ {
		enqueue(t, e, "t.run", i)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(stopped)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for done.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if done.Load() != 3 {
		t.Fatalf("ran %d jobs, want 3", done.Load())
	}
}

func TestRunOnceReportsQueryErrors(t *testing.T) {
	_, r := setup(t)
	r.Handle("t.x", func(context.Context, jobs.Job) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.RunOnce(ctx); err == nil {
		t.Fatal("cancelled context: want error")
	}
	if err := r.Drain(ctx); err == nil {
		t.Fatal("drain with cancelled context: want error")
	}
}
