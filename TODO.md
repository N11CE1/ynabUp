# TODO

Deferred ideas and known gaps, not urgent yet but worth remembering.

## Sync efficiency
- Bound the Up API fetch with `filter[since]`, keyed off a "last synced at"
  watermark stored in SQLite, instead of re-fetching full transaction history
  every run. Not a problem at current volume, but worth doing before this
  runs unattended on a schedule.
- YNAB's rate limit is undocumented and sends no `Retry-After` header;
  current exponential backoff in `ynab.PostTransaction` only smooths short
  bursts. A larger backfill (e.g. full real-budget history across 5
  accounts) could still exhaust the hourly quota - consider proactively
  throttling/spacing requests if that becomes a real problem.

## Categorisation
- Normalize/clean Up's raw `description` before using it as `payee_name`, so
  the same real-world merchant maps to a consistent string YNAB's payee
  auto-fill can recognise across transactions.
- Consider mapping Up's own transaction categories to YNAB categories as an
  optional pre-fill layer, later, on top of payee auto-fill.

## Pipeline (per ynabUpDesign.d2)
- Nightly cron reconciliation job as a safety net for missed webhooks.
- Deploy behind nginx reverse proxy on the Vultr VPS (Docker).

## BankSync account management
- `BANKSYNC_ACCOUNT_MAP` is a manually-maintained JSON blob in `.env` - if a
  5th CommBank account (or a new bank) is ever connected, its BankSync
  account ID needs to be looked up via the API and added by hand.
- CommBank's CDR consent (via BankSync) expires ~2027-07-16 (12-month
  window) and needs renewing in the BankSync app before then, or the
  connection lapses.

## Cross-account transfers
- Currently a transfer between two of our own tracked accounts (e.g.
  CommBank -> Up) posts as two separate, unlinked transactions instead of a
  proper YNAB transfer, which can distort category/available-to-budget math
  if either side gets categorized. To fix:
  - Match an Up transaction against a BankSync transaction: opposite sign,
    same/close amount, within some time window, between two mapped accounts.
  - On a match, post one side using YNAB's reserved "Transfer: [Account]"
    payee (via `payee_id`, from `GET /budgets/{id}/payees`) so YNAB
    auto-creates the paired transaction, and suppress posting the other
    side independently.
  - Handle the asynchronous-arrival case (one side's webhook/backfill
    landing well before the other) and already-posted-before-matched
    transactions (needs a follow-up PATCH/DELETE).

## Migrating to the real budget
- Before going live: create one manual "opening balance" reconciliation
  transaction per real account (Up + 4 CommBank accounts), dated the day
  before the backfill window starts, for whatever amount makes the YNAB
  running balance match the real bank balance at that point.
