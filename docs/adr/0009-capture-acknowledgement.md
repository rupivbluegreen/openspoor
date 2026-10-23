# ADR-009: Acknowledge EPCIS capture on the PostgreSQL commit

- Status: accepted (option 3: keep the commit acknowledgement as the default; build the opt-in log-first mode when a deployment needs ingest beyond one PostgreSQL primary)
- Date: 2026-10-23
- Decided: 2026-10-23 by the project owner

## Context
The serialization plan asks for capture to be acknowledged once events are durable in the
Kafka-compatible log (Redpanda), with serial and event state materialized afterwards in batches.
The current implementation validates the whole document, then writes the events, the capture
record and the follow-up jobs in one PostgreSQL transaction, and acknowledges after that commit.
Projection into serial state already runs asynchronously from the job queue.

Measured on 4 vCPU with PostgreSQL on the same host (fsync on): a 1,000-event document is
acknowledged in 121 ms p95, and a 100,000-event document (about 16,400 events/s) in 6.1 s,
which commissions serials at roughly 16,400/s against the 10,000/s single-node target.

## Options
1. **Keep the commit acknowledgement (current).** The response can carry per-event validation
   errors, because deduplication, unknown GTINs, unallocated serials and invalid aggregations
   are checked against the database before anything is stored (rejections are never silently
   dropped). A single node needs only PostgreSQL. Throughput is bounded by one PostgreSQL primary.
2. **Acknowledge on the log.** Validate what can be checked without the database, append to
   Redpanda with acks=all, return 202, and materialize in batches. Ingest scales with log
   partitions (per tenant and GTIN) and stateless ingest pods, which is the path to the 100,000/s
   three-node target. Checks that need state (duplicates, serial status, aggregation) move to the
   consumer, so their errors arrive asynchronously (capture job status, alerts) instead of in the
   response; Redpanda becomes mandatory.
3. **Both, by configuration.** Keep option 1 as the default and add a log-first mode for
   installations that set `OPENSPOOR_KAFKA_BROKERS` and opt in. `bus.ProduceSync` already exists
   for the synchronous append.

## Recommendation
Option 3: keep the commit acknowledgement as the default (it meets every published budget and
keeps synchronous error reports), and build the log-first mode when a deployment needs ingest
beyond one PostgreSQL primary, together with the three-node benchmark that has to prove it.

## Consequences
Until the log-first mode exists, the 100,000 serials/s three-node target is unproven, and the
benchmark page reports single-node numbers only.
