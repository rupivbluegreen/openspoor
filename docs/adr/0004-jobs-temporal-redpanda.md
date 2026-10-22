# ADR-004: Durable PostgreSQL job queue first; Redpanda for the event log; Temporal for long-running workflows

- Status: accepted
- Date: 2026-10-22

## Context
Capture must enqueue follow-up work (projections, webhooks, regulatory submissions) atomically with the data it depends on. Self-hosted single-node installs should not need three extra clusters to start.

## Decision
- `internal/core/jobs` is a transactional-outbox job queue in PostgreSQL (`FOR UPDATE SKIP LOCKED`, exponential backoff, permanent failures, dedupe keys). Jobs are enqueued in the same transaction as the business write, so they exist exactly when the data does.
- Domain events go on the in-process bus and, when `OPENSPOOR_KAFKA_BROKERS` is set, are mirrored to Redpanda (franz-go, acks=all) for external consumers and analytics.
- Temporal (Go SDK) remains the target for multi-day human-in-the-loop workflows (recalls, batch release). Connector submissions use the job queue's retry/backoff; the connector SDK keeps workflow state in the `regulatory_report` table so moving a connector to a Temporal workflow does not change its interface. Temporal is in the dev compose file; wiring it is tracked in OPEN_QUESTIONS.md.

## Consequences
A single PostgreSQL is enough to run everything; Redpanda and Temporal add scale and observability when present.
