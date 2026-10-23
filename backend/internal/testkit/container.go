package testkit

import (
	"context"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// One PostgreSQL 16 container serves every test in the process; each test still gets its own
// database. The container is removed when the test binary exits (testcontainers' reaper).
var (
	containerOnce             sync.Once
	containerAdmin, container string
	containerErr              error
)

// containerURLs starts (once) a PostgreSQL 16 container through Testcontainers and returns a
// superuser URL and a URL for the non-superuser application role, so row-level security applies.
func containerURLs() (adminURL, appURL string, err error) {
	containerOnce.Do(func() {
		defer func() {
			if p := recover(); p != nil { // no Docker: some provider lookups panic instead of erroring
				containerErr = fmt.Errorf("docker unavailable: %v", p)
			}
		}()
		ctx := context.Background()
		c, err := postgres.Run(ctx, "postgres:16-alpine",
			postgres.WithDatabase("postgres"), postgres.WithUsername("postgres"), postgres.WithPassword("postgres"),
			testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)))
		if err != nil {
			containerErr = err
			return
		}
		admin, err := c.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			containerErr = err
			return
		}
		conn, err := pgx.Connect(ctx, admin)
		if err != nil {
			containerErr = err
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		if _, err := conn.Exec(ctx, `create role openspoor login password 'openspoor' createdb`); err != nil {
			containerErr = err
			return
		}
		u, _ := url.Parse(admin)
		u.User = url.UserPassword("openspoor", "openspoor")
		containerAdmin, container = admin, u.String()
	})
	return containerAdmin, container, containerErr
}
