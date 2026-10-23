// Package jobs is a durable PostgreSQL job queue with retries and exponential backoff.
// It runs projections, webhooks, connector submissions and other background work.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Job is a unit of background work.
type Job struct {
	ID          int64           `json:"id"`
	TenantID    *uuid.UUID      `json:"tenantId,omitempty"`
	Kind        string          `json:"kind"`
	Payload     json.RawMessage `json:"payload"`
	Attempts    int             `json:"attempts"`
	MaxAttempts int             `json:"maxAttempts"`
	Status      string          `json:"status"`
	LastError   string          `json:"lastError,omitempty"`
	RunAt       time.Time       `json:"runAt"`
	CreatedAt   time.Time       `json:"createdAt"`
}

// Handler processes one job. Returning an error schedules a retry.
type Handler func(ctx context.Context, j Job) error

// Permanent wraps an error that must not be retried.
type Permanent struct{ Err error }

func (p Permanent) Error() string { return p.Err.Error() }

// Enqueue inserts a job inside tx (transactional outbox: the job exists only if tx commits).
func Enqueue(ctx context.Context, tx pgx.Tx, tenant *uuid.UUID, kind string, payload any, opts ...Opt) (int64, error) {
	o := options{maxAttempts: 8, runAt: time.Now()}
	for _, f := range opts {
		f(&o)
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRow(ctx, `insert into job (tenant_id, kind, payload, run_at, max_attempts, dedupe_key)
		values ($1,$2,$3,$4,$5,nullif($6,'')) on conflict (dedupe_key) where dedupe_key is not null and status in ('queued','running') do update set run_at=least(job.run_at, excluded.run_at)
		returning id`, tenant, kind, b, o.runAt, o.maxAttempts, o.dedupe).Scan(&id)
	if err == nil {
		_, _ = tx.Exec(ctx, "select pg_notify('openspoor_jobs', $1)", kind)
	}
	return id, err
}

type options struct {
	maxAttempts int
	runAt       time.Time
	dedupe      string
}

// Opt customizes an enqueued job.
type Opt func(*options)

// MaxAttempts sets the retry limit.
func MaxAttempts(n int) Opt { return func(o *options) { o.maxAttempts = n } }

// RunAt delays the job.
func RunAt(t time.Time) Opt { return func(o *options) { o.runAt = t } }

// Dedupe coalesces queued jobs with the same key.
func Dedupe(key string) Opt { return func(o *options) { o.dedupe = key } }

// Runner polls and executes jobs.
type Runner struct {
	pool        *pgxpool.Pool
	handlers    map[string]Handler
	concurrency int
	poll        time.Duration
	mu          sync.RWMutex
}

// NewRunner creates a runner.
func NewRunner(pool *pgxpool.Pool, concurrency int) *Runner {
	if concurrency < 1 {
		concurrency = 1
	}
	return &Runner{pool: pool, handlers: map[string]Handler{}, concurrency: concurrency, poll: 200 * time.Millisecond}
}

// Handle registers a handler for a job kind.
func (r *Runner) Handle(kind string, h Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[kind] = h
}

// Kinds returns the registered kinds.
func (r *Runner) Kinds() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.handlers))
	for k := range r.handlers {
		out = append(out, k)
	}
	return out
}

// Run blocks until ctx is done.
func (r *Runner) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for i := 0; i < r.concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				n, err := r.RunOnce(ctx)
				if err != nil && ctx.Err() == nil {
					slog.Error("job poll failed", "err", err)
				}
				if n == 0 {
					select {
					case <-ctx.Done():
					case <-time.After(r.poll):
					}
				}
			}
		}()
	}
	wg.Wait()
}

// RunOnce claims and runs at most one job; returns how many ran.
func (r *Runner) RunOnce(ctx context.Context) (int, error) {
	kinds := r.Kinds()
	if len(kinds) == 0 {
		return 0, nil
	}
	var j Job
	err := r.pool.QueryRow(ctx, `update job set status='running', attempts=attempts+1, updated_at=now()
		where id = (select id from job where status='queued' and run_at <= now() and kind = any($1)
		            order by run_at, id for update skip locked limit 1)
		returning id, tenant_id, kind, payload, attempts, max_attempts, run_at, created_at`, kinds).
		Scan(&j.ID, &j.TenantID, &j.Kind, &j.Payload, &j.Attempts, &j.MaxAttempts, &j.RunAt, &j.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	r.mu.RLock()
	h := r.handlers[j.Kind]
	r.mu.RUnlock()
	runErr := safeRun(ctx, h, j)
	if runErr == nil {
		_, err = r.pool.Exec(ctx, `update job set status='done', last_error='', updated_at=now() where id=$1`, j.ID)
		return 1, err
	}
	var perm Permanent
	permanent := errors.As(runErr, &perm)
	if permanent || j.Attempts >= j.MaxAttempts {
		slog.Warn("job failed permanently", "id", j.ID, "kind", j.Kind, "err", runErr)
		_, err = r.pool.Exec(ctx, `update job set status='failed', last_error=$2, updated_at=now() where id=$1`, j.ID, runErr.Error())
		return 1, err
	}
	backoff := time.Duration(math.Min(float64(time.Second)*math.Pow(2, float64(j.Attempts-1)), float64(30*time.Minute)))
	_, err = r.pool.Exec(ctx, `update job set status='queued', last_error=$2, run_at=now()+$3::interval, updated_at=now() where id=$1`,
		j.ID, runErr.Error(), fmt.Sprintf("%d milliseconds", backoff.Milliseconds()))
	return 1, err
}

// Drain runs jobs until none are ready (used by tests and the seed command).
func (r *Runner) Drain(ctx context.Context) error {
	for {
		n, err := r.RunOnce(ctx)
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
	}
}

func safeRun(ctx context.Context, h Handler, j Job) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = Permanent{fmt.Errorf("panic: %v", p)}
		}
	}()
	if h == nil {
		return Permanent{fmt.Errorf("no handler for %s", j.Kind)}
	}
	return h(ctx, j)
}
