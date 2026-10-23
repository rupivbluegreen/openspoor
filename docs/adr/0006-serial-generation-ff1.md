# ADR-006: Random serials via FF1 format-preserving encryption over a pool counter

- Status: accepted
- Date: 2026-10-22

## Decision
Each pool holds a counter and a 128-bit key (sealed with AES-256-GCM under the installation master key). Serial = FF1(key, tweak=GTIN+market, counter written in the pool alphabet's radix). FF1 is a permutation of alphabet^length, so distinct counters give distinct serials: uniqueness holds by construction, with no lookups. Allocation is one `UPDATE … SET next_counter = next_counter + n` plus an idempotency row; serial strings are computed in parallel after commit. Commissioned counters are kept in a Roaring bitmap per pool for O(1) double-commission checks; serials are reversible to counters by FF1 decryption. Default alphabet excludes I and O. Pools require alphabet^length ≥ 10^6 (NIST minimum domain).

## Consequences
Allocation cost is independent of pool history; measured ~260k–560k serials/s on 4 vCPUs (see bench-results). Serials are unpredictable without the key, which meets EU FMD randomisation guidance.
