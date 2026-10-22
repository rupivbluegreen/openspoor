---
title: How governed agents work
tags: [act, agents, gxp]
---
Agents in Open Spoor **observe and propose; people decide**.

- Each agent reads your data and proposes actions with its reasoning and the expected before/after.
- Every action uses a **tool** with a declared scope (which roles may approve it), a **risk level** and whether it is **reversible**.
- **High-risk** actions need an electronic signature (password re-entry, meaning and reason), in the spirit of 21 CFR Part 11.
- Every proposal, approval, rejection, execution and revert is in the **hash-chained audit trail**.
- You can let low-risk, reversible actions run automatically per agent (`autonomy: auto_low_risk`); everything else always waits for a person.

Built-in agents: exception triage (alerts → prioritized cases), regulatory resubmitter, expiry quarantine and partner watch.
