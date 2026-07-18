# TODO

Deferred ideas and known gaps, not urgent yet but worth remembering.

## Sync efficiency
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
- Deploy behind nginx reverse proxy on the Vultr VPS (Docker).

## BankSync account management
- `BANKSYNC_ACCOUNT_MAP` is a manually-maintained JSON blob in `.env` - if a
  5th CommBank account (or a new bank) is ever connected, its BankSync
  account ID needs to be looked up via the API and added by hand.
- CommBank's CDR consent (via BankSync) expires ~2027-07-16 (12-month
  window) and needs renewing in the BankSync app before then, or the
  connection lapses.
- Not yet, but worth considering later: CommBank activity is expected to
  drop to roughly one transaction a week as banking moves to Up, small
  enough to enter manually. At that point, dropping the BankSync
  integration entirely (backfill, webhook receiver, account map, the
  monthly cost) would simplify the codebase - remove the `banksync`
  package, its cron pass, and related config once CommBank is no longer
  worth paying BankSync to track.

## Cross-account transfers
- Up-to-Up transfers (Spending <-> Saver <-> 2Up) are done - Up's
  transferAccount relationship makes this a direct lookup, no heuristics
  needed. See pipeline.SyncUpTransaction.
- Cross-provider transfers (e.g. CommBank -> Up) are still descoped: they'd
  post as two separate, unlinked transactions since there's no shared ID to
  match Up and BankSync transactions against each other. Revisit if/when
  BankSync's sync-job bug is resolved and CommBank is still in active use -
  otherwise likely moot given the move to Up-only.
