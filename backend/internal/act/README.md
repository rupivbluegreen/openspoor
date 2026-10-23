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

### Agent evaluation and the autonomy ladder

Each governed agent (`exception-triage`, `onboarding-helper`, `investigation-assistant`, `inspection-agent`,
`mapping-agent`, `reg-watch`) has a golden dataset in `testdata/evals/<agent>.json`: fixture rows inserted into a
throwaway tenant (inside a transaction that is always rolled back, so the caller's data is never seen or changed),
and the proposals the agent should make. The harness (`eval.go`) runs the agent's own `Observe` (for the
request-driven `inspection-agent`, its package builder with the food and tracing modules stubbed) and matches
observed proposals to expected ones by tool and entity, then checks priority, title, reasoning, mapped fields or
checklist statuses where the case states them. A proposal that is present but wrong counts as a false positive and
a false negative. Per agent: precision, recall and score (F1) over all cases, against the pass threshold the
dataset declares. The datasets are synthetic and small (8 to 10 cases); **a score is not an accuracy figure for
real data**. `TestGoldenDatasets` runs them in CI; `openspoor eval-agents` prints the table (`--agent NAME`
limits it, `--tenant SLUG` records the scores and exits non-zero when an agent fails).

Recorded scores (`agent_eval_score`, one row per run, audited as `agent.eval.record`) gate autonomy above
`approve`. Setting a tool to `auto` (`PUT /agents/tools/{name}`) or an agent to `auto_low_risk`
(`PUT /agents/{name}`) is refused with 409 unless every evaluated agent that uses the tool (or the agent itself) has a
latest score at or above the higher of the tenant's minimum (default 0.9, `PUT /agents/evals/policy`, admin or
compliance, audited) and its dataset's threshold. A missing score counts as not meeting it. Refusals are audited
(`agent.tool.autonomy.refused`, `agent.agent.autonomy.refused`). A setting that was accepted stops applying while the
latest score is below the bar: proposals wait for approval, and `GET /agents/tools` lists the reason under
`evalBlocked`. Agents without a dataset are not gated. `suggest` and `approve` are never gated.

| Method | Path | Purpose |
|---|---|---|
| GET | `/agents/evals` | Datasets, latest score per agent, the tenant's minimum (any role) |
| POST | `/agents/evals/run` | Run the datasets (`{agents: [...]}` optional) and record the scores (admin or compliance) |
| PUT | `/agents/evals/policy` | `{minScore}` between 0.5 and 1 (admin or compliance) |

### Document library

`POST /library` takes `{name, mediaType?, content | contentBase64}` (writer roles) and extracts text with
`pkg/doctext` (PDF text layer, CSV, XML, HTML, plain text; at most 5 MiB; no OCR). Only the extracted text is kept,
as passages of about 1,200 bytes in `library_chunk` (per tenant, row-level security) with a `tsvector` column and a
GIN index; the same file cannot be added twice per tenant. `GET /library/search?q=` uses PostgreSQL
`websearch_to_tsquery` (words, "phrases", `or`, `-exclude`) and returns highlighted excerpts with a citation
`library/<id>#<n>`; `GET /library`, `GET /library/{id}` (passages) and `DELETE /library/{id}`. Uploads and removals
are audited (`library.upload`, `library.delete`); Viewer can search and read, not change. The assistant adds the best
three matching passages to its context and cites them.

### Semantic layer

`semantic_catalog.go` is a declarative catalogue: 10 metrics over existing tables (EPCIS events, serial updates,
alerts, cases, agent actions, regulatory reports, shipments, investigations, partner documents, food lot quantity),
each with a fixed aggregate, a period column and a map of dimension id to column expression, plus the KPI library
(`kpi.<id>`, day dimension only), which `internal/app` wires through the `KPIProvider` interface because `act` may not
import `ecosystem`. The planner (`semantic.go`) accepts a metric, an optional dimension (day, week, month or one the
metric declares), up to three equality filters and a period of at most 1,100 days, and emits one parameterised
`select` whose text is assembled only from catalogue entries; the period, filter values and limit are bound
parameters, every statement carries `tenant_id = app_tenant()` on top of row-level security, and it runs in a
savepoint set to read only with a 5 s statement timeout that is always rolled back. No request, and no model output,
contributes SQL. The catalogue is validated at start-up (identifier and expression shapes) and a test plans every
metric with every dimension.

`GET /semantic/catalog` and `POST /semantic/query` (`{metric, dimension?, from?, to?, days?, filters?, limit?}`) answer
"metric by dimension over a period" for every role including Viewer. In `POST /assistant/chat` the question is read by
the deterministic reader (catalogue names and synonyms, "by severity", "last 7 days", "this month"); when it finds no
metric and the question asks for a figure, the configured model may choose a metric from the catalogue, and the choice
is validated by the planner like any request. The result goes into the model's context (or the model-free answer) and
is cited as `metric/<id>`.

## Background work

- Schedule `act.agents` (every 10 min): runs every enabled agent for every tenant.

## Data

`agent_setting`, `agent_action`, `work_case`, `form_def`, `form_submission`, `workflow_def`,
`workflow_run` (migration `00009_act.sql`); `tool_autonomy`, `mapping_sample`, `mapping_case`,
`inspection_package`, `watch_source` (migration `00019_agents.sql`); `agent_eval_score`, `agent_eval_policy`,
`library_document`, `library_chunk` (migration `00027_eval_documents.sql`).

## Depends on

`core/*`, `connectors` (report resubmission), `trace` (serial state), `pkg/epcis`, `pkg/gs1`, `pkg/doctext`. The AI-native
agents reach the link, dscsa and food modules only through interfaces set in `internal/app` (`agents_wiring.go`).

## Spec gaps

`TODO(spec)` items (also in `docs/OPEN_QUESTIONS.md`): the inspection package checklist and the FSMA 204 / tracing
content and format per requester; EDI and XML samples for the mapping agent; the onboarding-helper thresholds; OCR and
search language configuration for the document library; score expiry and dataset binding for agent evaluation; the
semantic catalogue's definitions and permissions.

## Tests

From `backend/`: `eval "$(../scripts/testdb.sh)" && go test ./internal/act`
(integration tests; they skip without the test database). The tests use a fake model.
