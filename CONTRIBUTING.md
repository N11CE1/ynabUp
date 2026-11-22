# Contributing

This project is primarily maintained as a personal engineering/practice repo, but contributions and clean improvements are welcome.

## Goals

When making changes, prefer:
- small, focused pull requests
- clear commit messages
- explicit validation
- minimal, safe changes to sync logic

The highest-risk areas in this codebase are:
- transfer detection/linking in `pipeline`
- sync-state/idempotency behavior in `store`
- webhook handling and deletion behavior in `webhook`
- YNAB API interaction and transaction updates in `ynab`

## Branching workflow

Use short-lived branches off `main`.

Examples:
- `feat/...`
- `fix/...`
- `docs/...`
- `chore/...`

Typical flow:

```sh
git switch main
git pull
git switch -c <branch-name>
```

Open a pull request back into `main` when the change is ready.

## Pull request scope

Prefer one clear purpose per PR.

Good examples:
- one documentation improvement
- one CI/workflow improvement
- one transfer-sync bug fix
- one deployment/runtime hardening change

Avoid mixing unrelated work into the same PR unless the changes are tightly coupled.

## Local validation

Before opening or merging a PR, run the smallest useful validation for the change.

Common commands:

```sh
make fmt
make vet
make test
make test-race
make ci
```

Guidance:
- docs-only changes do not need full code validation
- code changes should normally pass at least `make test`
- risky pipeline/runtime changes should ideally pass `make ci`

## CI expectations

GitHub Actions runs on:
- pushes to any branch
- pull requests targeting `main`

PRs into `main` should be green before merge.

## Deployment and production safety

Read [`docs/deployment.md`](docs/deployment.md) before making deployment-related changes.

Important: the VPS deployment is the sole production instance against the real YNAB budget.

Do **not** run the app locally against the real production `.env` while the VPS service is also running. Running two syncing instances against the same accounts risks duplicate processing and YNAB rate-limit contention.

For local testing, prefer separate test credentials and a test budget.

## Documentation expectations

If a change affects developer workflow, deployment steps, or behavior that future-you might forget, update the relevant docs in the same PR.

Most likely files to update are:
- `README.md`
- `docs/deployment.md`
- `CHANGELOG.md`
- `HLSD.md` for significant design/architecture changes

## Changelog

Add notable user-visible or maintenance-relevant changes to `CHANGELOG.md`.

## Style

Keep changes consistent with the existing codebase:
- prefer simple solutions
- avoid unnecessary dependencies
- preserve existing naming/patterns unless there is a good reason to change them
- fix the root cause rather than layering on workarounds where possible
