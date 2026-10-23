# testkit

Test helper for integration tests. `testkit.New(t)` creates a fresh PostgreSQL database,
runs all migrations, assembles the full app, serves it with `httptest` and logs in as the
admin of tenant `acme`. Tests then drive the real HTTP API. The database is dropped when
the test finishes.

| API | Purpose |
|---|---|
| `New(t, opts...)` | Start an environment; `opts` can adjust the `config.Config` |
| `Env.NewTenant(slug, email)` | Create another tenant and return an admin token |
| `Env.Call(token, method, path, body, headers...)` | Request as a given token |
| `Env.Do(method, path, body, headers...)` | Request as the default admin |
| `Env.Must(status, resp, v)` | Assert the status and decode the body |
| `Env.Drain()` | Run all queued background jobs |

## Configuration

- `OPENSPOOR_TEST_ADMIN_URL` and `OPENSPOOR_TEST_DATABASE_URL` are required; without them the
  test is skipped. `scripts/testdb.sh` (repo root) starts a throwaway PostgreSQL 16 and
  prints both: `eval "$(../scripts/testdb.sh)"` from `backend/`.
- `OPENSPOOR_TEST_MODE` runs the suite in `single` (default), `saas` or `hub` mode.

## Endpoints, background work, data

None. It owns no routes, jobs or tables.

## Depends on

`app`, `platform`, `core/config`, `core/ids`, `backend/migrations`. Intended for test code only.

## Spec gaps

None marked `TODO(spec)`.

## Tests

No tests of its own; it is used by the `_test.go` files of the other modules.
