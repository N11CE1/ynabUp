# ynabUp — High-Level Software Design

A Go service that syncs Up Bank transactions into YNAB in real time. This
document describes the architecture as built and the reasoning — including
mistakes made along the way — worth carrying into future changes.

See [ynabUpDesign.d2](ynabUpDesign.d2) / [ynabUpDesign.svg](ynabUpDesign.svg)
for the visual architecture diagram this document expands on.

## Goals

- Every settled transaction on the tracked accounts appears in YNAB exactly
  once, without manual entry.
- Transfers between the user's own tracked accounts (e.g. Up Spending ↔
  Saver) post as real linked YNAB transfers, not duplicate unlinked
  transactions on each side.
- New transactions show up close to real time where the source supports it
  (Up webhooks); everything else is caught by a periodic reconciliation pass
  so a missed webhook or a down-for-maintenance source never causes
  permanent drift.
- Runs unattended on a small VPS with no external scheduler — a single
  process handles serving, catch-up, and reconciliation.

## Non-goals

- Categorisation or payee normalisation beyond what YNAB's own auto-fill
  provides from raw payee names.
- Multi-user/multi-budget support. Config is one process, one YNAB budget,
  one set of source credentials, via a single `.env`.

## Components

```
up/         Up Bank REST client (fetch transactions, pagination, filter[since])
webhook/    Up webhook handler (signature verification, single-txn fetch)
ynab/       YNAB REST client (post transaction, fetch accounts, backoff)
pipeline/   Core sync/transform/dedup logic, shared by every entry point
store/      SQLite-backed sync-state tracking (synced IDs + watermark)
main.go     CLI entrypoint: one-shot sync / -serve
```

Every entry point (webhook, one-shot poll) converges on
`pipeline.SyncTransaction`, so dedup and posting logic exists in exactly one
place regardless of how a transaction was discovered.

## Data flow

**Fast path (Up only):** Up POSTs a signed `TRANSACTION_SETTLED` webhook to
`/webhooks/up` → handler verifies `X-Up-Authenticity-Signature`
(HMAC-SHA256) → fetches the single referenced transaction by ID → runs it
through the pipeline.

**Reconciliation path (in-process cron):** a background goroutine started by
`-serve` runs immediately on startup and then every `CRON_INTERVAL` (default
1h): `pipeline.RunOneShotSync` — incremental fetch via `filter[since]` and a
persisted watermark, not a full refetch. Acts as the safety net for missed
webhooks.

## Why an in-process cron loop, not OS cron

Considered and rejected: a system cron job invoking a one-shot binary.
Rejected because the deployment target is Docker, and a container doesn't
have a cron daemon by default — adding one would mean either a second
process in the container (fighting Docker's one-process-per-container
convention) or a sidecar, both more moving parts than a goroutine with a
`time.Ticker` inside the same `-serve` process that's already running.

## Why linked transfers need special-casing

YNAB has no concept of "this transaction is the other half of that one"
unless you tell it explicitly via `payee_id` set to the destination
account's transfer payee ID. Posting both sides as normal transactions
(payee name only) produces two real but unlinked transactions — technically
correct balances, but doubled transaction count and no cross-reference.

Up's API exposes a `transferAccount` relationship, which made detecting
*candidate* transfers a direct lookup rather than a heuristic (e.g. matching
on amount + date across accounts, which is fragile). But `transferAccount`
being set is **not sufficient** to conclude "this is a linkable two-sided
transfer" — see the Round Up bug below for why that distinction mattered in
practice.

`pipeline.SyncUpTransaction` handles three cases:
1. No `transferAccount` → normal post.
2. `transactionType == "Transfer"` (a real two-sided transfer — both
   accounts have their own matching Up record) → post only the outflow
   side, as a linked transfer; suppress the inflow side as already-handled,
   since YNAB creates it automatically once the linked outflow posts.
3. `transferAccount` set but `transactionType != "Transfer"` (a one-sided
   attribution, e.g. Round Up) → construct the missing outflow synthetically
   from the inflow record's own data and post it as a linked transfer on
   the source account's behalf. See below for why this case exists at all.

## Sync-state and idempotency

A local SQLite file (`store`) tracks every `import_id` already posted to
YNAB, checked before every post regardless of entry point. This exists
because:
- Up transactions can arrive via both the webhook (fast path) and the cron
  safety net (reconciliation path) — without dedup, a transaction synced by
  the webhook would be re-posted by the next cron pass.
- YNAB's own `import_id` deduplication (`ynab.ErrDuplicateTransaction`) is
  the backstop, not the primary mechanism — the local check saves the round
  trip and the transaction, and stays authoritative even if YNAB's own
  behavior around `import_id` scoping ever changes.
- `import_id` is capped at 36 chars by YNAB, scoped per account. Longer
  source IDs are hashed via `ynab.SafeImportID` rather than truncated, to
  avoid silent collisions between two different transactions that happen to
  share a truncated prefix.

`RunOneShotSync`'s watermark (`up_last_synced_at` in the `settings` table)
only advances when a run has **zero** failures, so a partial failure
(network blip mid-batch, one bad transaction) gets retried from the same
starting point next run instead of silently skipping whatever failed.

## Errors and lessons learned

These are worth reading before touching the pipeline or the sync-state
logic — each was a real production incident, not a hypothetical.

**Up's Round Up feature is a one-sided ledger entry.** When Round Up is
enabled, a purchase transaction on Spending carries a `roundUp` attribute
and a `transferAccount` pointing at the Saver, and a separate transaction on
the Saver shows the inflow — but `transactionType` on that inflow is *not*
`"Transfer"`, and there is no matching outflow record anywhere in Up's API.
Early code treated any `transferAccount`-bearing transaction as a linkable
transfer and suppressed the "other side" waiting for it to arrive — which,
for Round Up, never happens, so the money silently vanished from YNAB
entirely. The fix required checking `transactionType == "Transfer"` before
treating a transaction as having a real matching counterpart, and — for the
one-sided case — constructing the missing outflow synthetically instead of
just posting the inflow as a normal (unlinked) transaction, which would
have fixed the vanishing-money bug but left Spending's YNAB balance
permanently understated relative to reality.

**YNAB's cached `balance`/`cleared_balance` account fields can be stale.**
While reconciling the real budget, a balance discrepancy that looked like a
sync bug turned out to be stale cache, not a real mismatch. Any reconciliation
logic must verify against the actual summed transaction list for an account,
never trust the account resource's cached balance fields as ground truth.

**Unbounded fetch on first run, twice.** `RunOneShotSync` originally had no
date bound at all; the first real-budget run pulled Up's entire history back
to 2024 and double-posted against an already-established reconciliation
baseline. Fixed via `filter[since]` plus a persisted watermark — but the fix
shipped alongside pagination-following (`Links.Next`) in the same change,
and the very first run under the new code had no watermark yet either, so it
followed pagination through full history again and re-triggered the same
class of duplicate-posting bug a second time, from a different cause. Both
incidents required manually finding and deleting the resulting duplicate
YNAB transactions via the API. Lesson: a bound and its escape hatch
(pagination, "first run ever" with no watermark) need to be reasoned about
together, not fixed one at a time.

**A background goroutine must never call `log.Fatalf`.**
`RunOneShotSync` originally did, which is fine for the CLI one-shot path but
would have killed the entire `-serve` process — including live webhook
handling — the first time a reconciliation pass hit a transient error.
Changed to return `error`; the CLI path still exits on error, the cron
goroutine just logs and continues.

**A stray local `-serve` process ran against the real budget with stale
config** during VPS deployment testing — an orphaned background process
from earlier in the session, invisible until `ps aux` was checked. No data
corruption resulted, but it could have raced the VPS's own instance against
the same accounts. There is no code-level guard against this (two processes
pointed at the same `.env` will both happily sync); it's a deployment
discipline issue, not a bug — see the explicit warning in
[TODO.md](TODO.md).

## Deployment

Single Docker container (multi-stage build, `golang:1.26-alpine` →
`alpine:latest`, no CGO via `modernc.org/sqlite`), `docker-compose.yml` with
an `env_file` for secrets and a named volume for the SQLite state file.
Port bound to `127.0.0.1` only; nginx reverse-proxies the service's domain
(Let's Encrypt via Certbot) to the container, matching the existing pattern
of other services on the same VPS. DNS is deliberately not proxied through
any CDN/edge network, since there's no need for that in front of a
single-user webhook endpoint.

## Known gaps / deferred work

See [TODO.md](TODO.md) for the live backlog (rate-limit throttling, payee
normalisation). Not duplicated here since that file is kept current and
this one isn't meant to be.
