# Wire octavertex-growth into OutReachCRM

## One-time setup

1. Restart OutReachCRM so the latest seed is loaded (`make run` / redeploy).
2. Sign in → **Campaigns** → **Seed playbooks** (campaigns + templates only — **no dummy leads**).
3. Confirm campaign **`OVM · Manufacturing Lead Platform (₹1.25L+)`** appears (5 steps: Day 0 · 2 · 5 · 10 · 21).
4. Confirm templates named **`OVM · Mfg …`** under `/templates`.
5. Connect send account + pass `/deliverability` before volume.

Idempotent: re-clicking seed skips existing campaign/template names. Seed also purges leftover `example.*` demo leads.

## Import real ICP (Week 1 goal: 100)

1. Copy [ICP-IMPORT-TEMPLATE.csv](ICP-IMPORT-TEMPLATE.csv) (or download `/static/ovm-manufacturing-icp.csv` from the app).
2. Replace sample rows with researched accounts (see [4-CLIENTS-PER-MONTH.md](4-CLIENTS-PER-MONTH.md) §6).
3. Keep header:

```
name,email,company,title,website,phone,source,notes
```

4. **Leads → Bulk CSV import** → upload.
5. Enroll into **`OVM · Manufacturing Lead Platform (₹1.25L+)`** (daily limit 20).

### Column guide

| Column | Required | Notes |
|--------|----------|-------|
| name | yes | Decision-maker |
| email | yes | Deduped on import |
| company | recommended | Used in `{{company}}` merge tags |
| title | optional | Owner / Sales Director / … |
| website | recommended | First-line personalization + `{{website}}` |
| phone | optional | WhatsApp warm intros |
| source | optional | e.g. `icp-mfg-mumbai` (default `csv`) |
| notes | optional | Trigger + observation for personalization |

Legacy files `name,email,website,phone` (no header) still work.

## Daily ops

| Activity | Where |
|----------|--------|
| 20 emails | Campaign queue (limit 20/day) |
| 20 LinkedIn | Manual; log in lead notes |
| 10 follow-ups | Sequence handles email cadence |
| Replies → discovery | Inbox / HITL → book Calendly |
| Proposals | [PROPOSAL-TEMPLATE.md](PROPOSAL-TEMPLATE.md) |
| Scoreboard | `/analytics` vs [4-CLIENTS-PER-MONTH.md](4-CLIENTS-PER-MONTH.md) §9 |

## Offer discipline

- Default: **Lead Generation Platform ≥ ₹1.25L**
- Copy source of truth: [OUTREACH-SCRIPTS.md](OUTREACH-SCRIPTS.md)
- Do **not** enroll manufacturing ICPs into the USD MVP campaigns unless they are founders buying Discovery/Starter.
