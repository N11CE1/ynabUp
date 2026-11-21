# Changelog

All notable changes to this project will be documented in this file.

The format is inspired by [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added
- `CHANGELOG.md`

## [Pre-release]

### Added
- initial Up Bank transaction fetcher and MVP one-shot sync into YNAB
- YNAB transformer/client integration and SQLite-backed sync-state tracking
- configurable SQLite database path via `DB_PATH`
- Up webhook receiver alongside the one-shot sync path
- package split into `up`, `ynab`, `store`, `pipeline`, and `webhook`
- linked Up-to-Up transfer detection and real YNAB transfer posting
- persisted `filter[since]` watermarking and in-process cron reconciliation in `-serve` mode
- Docker deployment artifacts (`Dockerfile`, `.dockerignore`, `docker-compose.yml`)
- architecture documentation in `HLSD.md` and embedded architecture diagram in the README
- savings-category funding for linked transfers landing in configured savings accounts
- delete handling that retracts synced YNAB transactions when Up reports a source transaction deleted
- cleared-status updates for already-synced transactions, including both sides of linked transfers
- GitHub Actions CI for formatting, vet, tests, race tests, and Docker image builds
- `.env.example` for easier local and deployed configuration
- `Makefile` for common local development and validation workflows
- Dependabot configuration for GitHub Actions and Go modules
- MIT `LICENSE`

### Changed
- fixed `go.mod` module path housekeeping and other early project cleanup
- bounded Up transaction fetches to avoid unbounded historical re-imports
- fixed one-sided transfer handling (for example, Up Round Up) by constructing the missing outflow instead of silently losing the movement
- hardened the deployment layout by binding the container port to `127.0.0.1` only and documenting deployed behavior more accurately
- removed the BankSync/CommBank integration after it became unnecessary
- reworked the README with quick start, local run instructions, deployment guidance, health checks, and troubleshooting
- expanded local and CI validation with race-enabled test coverage
- updated CI to run on pushes to any branch and on pull requests targeting `main`
- hardened the Docker/runtime setup with pinned base images, a health check, and unprivileged app execution
- updated `.gitignore` to exclude local Go temp build/test directories
