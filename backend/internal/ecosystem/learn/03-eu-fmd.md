---
title: EU FMD safety features
tags: [comply, eu-fmd, europe]
---
Commission Delegated Regulation (EU) 2016/161 requires a **unique identifier** and an **anti-tampering device** on prescription medicines.

- The unique identifier (Art. 4) contains the **product code**, a **serial number** of up to 20 characters whose probability of being guessed is below 1 in 10,000, a **national reimbursement number** where required, the **batch** and the **expiry date**.
- It is encoded in a **Data Matrix** (Art. 5).
- Manufacturers upload the identifiers to the repositories system (EU Hub → national systems) before release; pharmacies verify and decommission at dispensing.

In Open Spoor, random pools use FF1 encryption over a counter, so serials are both unique and unguessable. The **EU FMD connector** uploads pack data on commissioning and status changes on dispensing, destruction or sampling. Hub rejections arrive in the **regulatory alert inbox** with the parsed reason, the affected packs, a suggested fix and one-click resubmit.
