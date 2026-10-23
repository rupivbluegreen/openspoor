# act

Governed agents and work management. The module owns the agent framework (agents propose
actions; a tool runs an action only after approval, or automatically for low-risk reversible
tools when the tenant opts in), the control tower (monitors and a prioritized case queue),
no-code forms and workflows with a built-in process library, and a read-only assistant over
the tenant's own data. High-risk actions need an electronic signature; every decision is
audited and reversible tools can be reverted.

Built-in agents: `exception-triage`, `regulatory-resubmitter`, `expiry-quarantine`,
`partner-watch`, and three watchers that flag counterfeit and diversion signals (`verification-spike`, `reappeared-serial`, `duplicate-verification`; fixed thresholds in `watchers.go`). Built-in tools: `case.open`, `report.resubmit`, `serials.set_status`.
Built-in processes: `po-to-receipt`, `batch-release`, `order-to-cash`.

The assistant uses an optional language model (configured with `ANTHROPIC_API_KEY` and
`OPENSPOOR_ASSISTANT_MODEL`). Without them it answers from database lookups only.

## Endpoints (`/api/v1`, authenticated)

| Method | Path | Purpose |
|---|---|---|
| GET | `/agents` | List agents and their tenant settings |
| PUT | `/agents/{name}` | Configure an agent (admin) |
| POST | `/agents/{name}/run` | Run an agent now |
| GET | `/agents/actions`, `/agents/actions/{id}` | List / get proposed actions |
| POST | `/agents/actions/{id}/approve` \| `reject` \| `revert` | Decide on or undo an action |
| GET | `/control-tower` | Monitors, case queue, pending action count |
| GET, POST | `/cases` | List / open work cases |
| PATCH | `/cases/{id}` | Update a case |
| GET, POST | `/forms` | List / create forms (create: admin) |
| POST | `/forms/{id}/submit` | Submit form data (validated against the form schema) |
| GET | `/forms/{id}/submissions` | List submissions |
| GET, POST | `/workflows` | List / create workflows (create: admin) |
| GET | `/processes` | Built-in process library |
| POST | `/workflows/{key}/start`, `/processes/{key}/start` | Start a run |
| GET | `/workflow-runs`, `/workflow-runs/{id}` | List / get runs |
| POST | `/workflow-runs/{id}/advance` | Complete the current step |
| POST | `/assistant/chat` | Ask the assistant a question |

## Background work

- Schedule `act.agents` (every 10 min): runs every enabled agent for every tenant.

## Data

`agent_setting`, `agent_action`, `work_case`, `form_def`, `form_submission`, `workflow_def`,
`workflow_run` (migration `00009_act.sql`).

## Depends on

`core/*`, `connectors` (report resubmission), `trace` (serial state), `pkg/epcis`, `pkg/gs1`.

## Spec gaps

None marked `TODO(spec)`.

## Tests

From `backend/`: `eval "$(../scripts/testdb.sh)" && go test ./internal/act`
(integration tests; they skip without the test database). The tests use a fake model.
