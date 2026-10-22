package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/openspoor/openspoor/internal/core/kernel"
)

// RunSchedulers runs every module's periodic tasks until ctx is done.
// Each task takes a PostgreSQL advisory lock so only one worker replica runs it at a time.
func (a *App) RunSchedulers(ctx context.Context) {
	for _, m := range a.Modules {
		s, ok := m.(kernel.Scheduled)
		if !ok {
			continue
		}
		for _, sc := range s.Schedules() {
			go a.loop(ctx, sc)
		}
	}
	<-ctx.Done()
}

func (a *App) loop(ctx context.Context, sc kernel.Schedule) {
	t := time.NewTicker(sc.Every)
	defer t.Stop()
	for {
		a.runLocked(ctx, sc)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (a *App) runLocked(ctx context.Context, sc kernel.Schedule) {
	conn, err := a.Deps.DB.Pool.Acquire(ctx)
	if err != nil {
		return
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(ctx, "select pg_try_advisory_lock(hashtextextended($1, 0))", "schedule:"+sc.Name).Scan(&got); err != nil || !got {
		return
	}
	defer func() { _, _ = conn.Exec(context.Background(), "select pg_advisory_unlock(hashtextextended($1, 0))", "schedule:"+sc.Name) }()
	if err := sc.Run(ctx); err != nil && ctx.Err() == nil {
		slog.Warn("scheduled task failed", "task", sc.Name, "err", err)
	}
}
