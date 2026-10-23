# act

Governed agents and work management. The module owns the agent framework (agents propose
actions; a tool runs an action only after approval, or automatically for low-risk reversible
tools when the tenant opts in), the control tower (monitors and a prioritized case queue),
no-code forms and workflows with a built-in process library, and a read-only assistant over
the tenant's own data. High-risk actions need an electronic signature; every decision is
audited and reversible tools can be reverted.

Built-in agents: `exception-triage`, `regulatory-resubmitter`, `expiry-quarantine`,
`partner-watch`, and three watchers that flag counterfeit and diversion signals (`verification-spike`, `reappeared-serial`, `duplicate-verification`; fixed thresholds in `watchers.go`). Built-in tools: `case.open`, `report.resubmit`, `serials.set_status`.

AI-native agents (below) follow one rule: a deterministic core decides, agents do the work around it.
They propose through the same Proposal / approval flow, call the same typed functions as the console
(so the same validators apply), and run without a language model. A configured model (`act.LLM`) may only
reword a proposal's summary; the deterministic reasoning is kept, and the model name, prompt and output go
into the proposal's audit entry. Nothing becomes a record without a person's approval or signature.
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

## AI-native agents

| Agent | Tool(s) | What it does |
|---|---|---|
| `mapping-agent` | `mapping.save`, `case.open` | Reads uploaded sample partner files (table `mapping_sample`; CSV with delimiter detection, JSON), proposes the mapping studio's header-based suggestion with a confidence and reasoning per field, and generates up to 5 regression cases (sample row to expected canonical values). Approving saves the mapping through the link module's validators after re-running every case, and stores the cases (`mapping_case`). XML/EDI samples become a case for a person. Confidence: half the share of columns mapped plus half the share of sample rows that map cleanly (at most 0.95). |
| `onboarding-helper` | `case.open` | For partners whose onboarding checklist has had no progress for 7 days, or a failing test for 2 days, proposes the next step as a case that cites the open checklist item or the failing test. |
| `investigation-assistant` | `investigation.assemble_answer`, `case.open` | For open DSCSA investigations due within 24 h (or overdue), or without evidence after 24 h, proposes assembling the tracing answer (dscsa module function) and lists what is missing. It never closes, confirms or signs; when the answer exists it only opens a case listing the gaps. |
| `inspection-agent` | `inspection.assemble` | On request, assembles the FSMA 204 spreadsheet (food module) or a tracing answer (dscsa module) as a draft `inspection_package` with a checklist. Releasing needs every checklist item satisfied and the caller's e-signature (`entity=inspection_package`, given after the last change). Nothing is sent to any authority. |
| `reg-watch` | `case.open` | Fetches the tenant's `watch_source` pages (https only, port 443, 2 MiB, 10 s, no private/loopback/link-local addresses at dial time and on redirects), hashes the normalized text and, on a change, proposes the case "Regulatory source changed: <name>" with a unified diff excerpt and the products whose target markets include the source's market. The first fetch is a baseline. At most every 6 hours per source. |

`POST /agents/inspection/assemble` records the request as an agent action decided by the requester, so the
framework's audit entries (propose, execute, revert) apply. The tools use `act.MappingEngine`,
`act.TracingAssembler` and `act.FSMAExporter`, which `internal/app` wires to the link, dscsa and food modules.
`Module.InsecureWatch` (tests only) lets the watcher use plain http and loopback.

### Autonomy ladder per action type

`GET /agents/tools` lists the tools with risk, reversibility and the tenant's mode; `PUT /agents/tools/{name}`
(admin or compliance) sets `suggest` (a person makes the change; approving is refused), `approve` (default) or
`auto`. `auto` is accepted only for low-risk reversible tools; an auto-applied action is audited, records
`result.undo` and can be reverted. A tool setting wins over the per-agent setting (`propose` / `auto_low_risk`).
Tools run in a savepoint, so a failing tool leaves the action recorded as failed without side effects.

### Further endpoints

| Method | Path | Purpose |
|---|---|---|
| GET, POST | `/agents/mapping/samples` | List / upload sample files (`docType`, optional `format`, `source`, `error`) |
| POST | `/agents/mapping/samples/{id}/dismiss` | Dismiss a sample |
| GET | `/agents/mapping/cases?mappingId=` | Regression cases of a mapping |
| POST | `/agents/mapping/regression` | Re-run a mapping's cases against the saved mapping |
| POST | `/agents/inspection/assemble` | `{kind: fsma204 \| tracing, scope}` builds a draft package |
| GET | `/agents/inspection/packages`, `/{id}`, `/{id}/content` | List, get, download (audited) |
| POST | `/agents/inspection/packages/{id}/checklist` \| `release` \| `void` | Confirm an item; release with `signatureId`; void with a reason |
| GET, POST | `/watch-sources` | List / add watched sources |
| PUT, DELETE | `/watch-sources/{id}` | Update (a new URL or selector restarts the baseline) / remove |
| GET | `/agents/tools` | Tools and their autonomy |
| PUT | `/agents/tools/{name}` | Set a tool's autonomy |

Writes need a non-Viewer role; releasing and voiding packages need admin or compliance.

## Background work

- Schedule `act.agents` (every 10 min): runs every enabled agent for every tenant.

## Data

`agent_setting`, `agent_action`, `work_case`, `form_def`, `form_submission`, `workflow_def`,
`workflow_run` (migration `00009_act.sql`); `tool_autonomy`, `mapping_sample`, `mapping_case`,
`inspection_package`, `watch_source` (migration `00019_agents.sql`).

## Depends on

`core/*`, `connectors` (report resubmission), `trace` (serial state), `pkg/epcis`, `pkg/gs1`. The AI-native
agents reach the link, dscsa and food modules only through interfaces set in `internal/app` (`agents_wiring.go`).

## Spec gaps

`TODO(spec)` items (also in `docs/OPEN_QUESTIONS.md`): the inspection package checklist and the FSMA 204 / tracing
content and format per requester; EDI and XML samples for the mapping agent; the onboarding-helper thresholds.

## Tests

From `backend/`: `eval "$(../scripts/testdb.sh)" && go test ./internal/act`
(integration tests; they skip without the test database). The tests use a fake model.
