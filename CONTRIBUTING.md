# Contributing to Open Spoor

Thanks for helping regulated supply chains run on open software.

## Ground rules
- Implement only from public standards (GS1 EPCIS 2.0, CBV 2.0, Digital Link, Lightweight Messaging for VRS, W3C VC, OCI). Never copy a proprietary vendor's code, documentation, names, UI or branding.
- Never invent regulatory requirements. If a spec detail is unknown, stub it behind an interface, add `TODO(spec):` and list it in `docs/OPEN_QUESTIONS.md`.
- Every write must produce an audit entry; serial numbers are never reissued; every tenant table has RLS.

## Development
```
docker compose -f deploy/docker-compose.yml up -d     # or scripts/testdb.sh for Postgres only
make -C backend migrate seed run                       # API on :8080
pnpm -C frontend install && pnpm -C frontend dev:console
make -C backend test-integration                       # Go tests against a throwaway Postgres 16
```

## Pull requests
- Small commits with conventional-commit messages (`feat(epcis): …`, `fix(dscsa): …`).
- Add tests (unit + integration against PostgreSQL). Aim for 80% line coverage on domain code.
- Update `docs/CHANGELOG.md` for user-visible changes.
- Country rule packs live in `rulepacks/*.yaml`; cite the public source document for every rule.

By contributing you agree your contribution is licensed under Apache-2.0.
