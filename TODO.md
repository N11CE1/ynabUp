# TODO

## Soon
- Normalize/clean Up's raw `description` before using it as `payee_name`, so
  the same real-world merchant maps to a consistent string YNAB's payee
  auto-fill can recognise across transactions.

## Standing reminder
- The VPS is the sole production instance against the real budget. Don't
  run `go run .` / `-serve` locally against the real `.env` at the same
  time - two instances syncing the same accounts risks duplicate
  processing and YNAB rate-limit contention. Local runs should stick to the
  test budget unless deliberately debugging production.
