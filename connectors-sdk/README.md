# Open Spoor connector SDK

Build a country connector (regulator, national hub, repository) in Go:

```go
type Connector interface {
	Info() Info                                              // id, country, triggers, config fields, spec
	Map(ctx, Event) ([]Payload, error)                       // pure: EPCIS event -> hub payloads (stable idempotency keys)
	Submit(ctx, Config, Payload) (Submission, error)         // send one payload
	PollStatus(ctx, Config, Submission) (Status, error)      // pending | accepted | rejected
	HandleRejection(ctx, Rejection) Alert                    // parsed reason + suggested fix for the alert inbox
}
```

The host owns retries with exponential backoff, idempotency, the audit trail, the
regulatory alert inbox and one-click resubmission. Your connector stays small.

Optional:
- `BatchMapper.MapBatch(ctx, []Event)` — one payload per capture instead of per event (file drops).
- `Info.Triggers = ["*"]` (`AllBizSteps`) — receive every event, including those without packs.
- `Event.Parent` carries an aggregation's parent; `Event.Raw` the event as captured (EPCIS 2.0
  JSON-LD) for integration connectors that forward it unchanged.

## Contract tests
```go
func TestContract(t *testing.T) {
	hub := httptest.NewServer(myMockHub())
	contracttest.Run(t, myconnector.New(), connector.Config{"baseUrl": hub.URL}, []contracttest.Case{
		{Name: "commissioning", Event: sampleCommissioning, WantPayloads: 1, WantState: connector.Accepted},
		{Name: "bad batch", Event: missingBatch, WantPayloads: 1, WantState: connector.Rejected},
	})
}
```
Reference connectors (EU FMD / EMVS and Saudi RSD) live in `backend/internal/connectors`
with mock hubs built from public specifications. Real hub endpoints and fields that are
not public are marked `TODO(spec)` — never guessed.

## Rules
- Implement from public specifications only; cite them in `Info().Spec`.
- `Map` must be deterministic and must not do I/O.
- Never log secrets; the host passes decrypted config values only to your calls.
