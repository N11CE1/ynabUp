# TODO

Deferred ideas and known gaps, not urgent yet but worth remembering.

## Sync efficiency
- Bound the Up API fetch with `filter[since]`, keyed off a "last synced at"
  watermark stored in SQLite, instead of re-fetching full transaction history
  every run. Not a problem at current volume, but worth doing before this
  runs unattended on a schedule.

## Categorisation
- Normalize/clean Up's raw `description` before using it as `payee_name`, so
  the same real-world merchant maps to a consistent string YNAB's payee
  auto-fill can recognise across transactions.
- Consider mapping Up's own transaction categories to YNAB categories as an
  optional pre-fill layer, later, on top of payee auto-fill.

## Pipeline (per ynabUpDesign.d2)
- Webhook receiver (`up_handler`): verify `X-Up-Authenticity-Signature`
  (HMAC-SHA256), fetch the single transaction by ID, run through the
  existing transform/sync pipeline.
- BankSync webhook handler (`bs_handler`).
- Nightly cron reconciliation job as a safety net for missed webhooks.
- Deploy behind nginx reverse proxy on the Vultr VPS (Docker).

## Housekeeping
- `go.mod` module path has a stray trailing `.git`
  (`github.com/N11CE1/ynabUp.git`) — fix to `github.com/N11CE1/ynabUp`.
