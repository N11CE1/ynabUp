# Deployment runbook

This document is the practical runbook for operating `ynabUp` on a VPS.

## Overview

The deployed service is a single Docker Compose application:
- image built from the repo's `Dockerfile`
- env vars supplied via `.env`
- SQLite sync state stored in the named volume `ynabup-data`
- app bound to `127.0.0.1:8080` and expected to sit behind a reverse proxy

The container defaults to `-serve`, so it runs:
- the Up webhook endpoint at `/webhooks/up`
- the `/healthz` endpoint
- the in-process reconciliation loop

## Important safety rule

The VPS is the sole production instance against the real YNAB budget.

Do **not** run `go run .` or `go run . -serve` locally against the real production `.env` at the same time as the VPS deployment. Two instances syncing the same accounts risks duplicate processing and YNAB rate-limit contention.

## Deploy

From the repo directory on the VPS:

```sh
git pull
docker compose up -d --build
```

This will:
- rebuild the image
- recreate/update the container
- keep the SQLite sync state because it lives in the `ynabup-data` named volume

## Post-deploy verification

Check container status:

```sh
docker compose ps
```

Watch startup logs:

```sh
docker compose logs -f ynabup
```

Expected healthy startup signs include:
- `listening on :8080`
- `cron: running Up reconciliation sync`
- a reconciliation summary such as `Done: 0 synced, 0 already synced (skipped), 0 failed`

Check the health endpoint:

```sh
curl http://127.0.0.1:8080/healthz
```

A healthy response looks like:

```json
{"status":"ok","last_synced_at":"2026-11-21T10:15:00Z"}
```

If no successful reconciliation pass has completed yet, `last_synced_at` may be absent.

## Logs

Tail logs live:

```sh
docker compose logs -f ynabup
```

Recent logs only:

```sh
docker compose logs --tail=100 ynabup
```

## Restart

Restart just the service:

```sh
docker compose restart ynabup
```

Rebuild and restart after code changes:

```sh
docker compose up -d --build
```

## State and backups

Sync state is stored in SQLite inside the named Docker volume `ynabup-data`.

That state matters because it tracks:
- which source transactions have already been synced
- stored YNAB transaction IDs used for cleared-status updates and deletions
- the reconciliation watermark

At minimum, know that rebuilding the image does **not** reset sync state, but removing the volume would.

Useful inspection command:

```sh
docker volume inspect ynabup_ynabup-data
```

If you want a backup strategy later, back up the SQLite DB stored in that volume before major operational changes.

## Rollback

If a new deploy behaves badly:

1. identify the last known-good commit
2. check it out on the VPS
3. rebuild/restart the container

Typical flow:

```sh
git log --oneline
git checkout <known-good-commit-or-tag>
docker compose up -d --build
```

If you roll back code, the existing SQLite state volume is still reused unless you explicitly remove it.

## Common checks

### Verify the app is still listening

```sh
curl http://127.0.0.1:8080/healthz
```

### Verify the container is healthy

```sh
docker compose ps
```

### Verify recent webhook/sync activity

```sh
docker compose logs --tail=100 ynabup
```

## Common failure modes

### `no .env file found, falling back to existing environment`

Usually harmless in Docker. Compose injects the environment variables, so the app does not need a literal `.env` file inside the container filesystem.

### `Done: 0 synced, 0 already synced (skipped), 0 failed`

Usually means there were no new Up transactions to process during that reconciliation run.

### `/healthz` is failing

Check:
- the container is running
- the app started cleanly in logs
- the SQLite DB path is still writable
- no recent deploy accidentally removed or replaced the data volume

### Webhooks are not arriving

Check:
- reverse proxy forwarding to `127.0.0.1:8080`
- deployed webhook URL still points at `/webhooks/up`
- `UP_WEBHOOK_SECRET` still matches the `secretKey` from Up

### Transfers are behaving unexpectedly

Check:
- both Up accounts are present in `UP_ACCOUNT_MAP`
- the mapped YNAB accounts exist in the target budget
- the app was able to fetch YNAB accounts at startup

## Useful one-off commands

Run a one-shot sync in the container image locally from the repo:

```sh
docker compose run --rm ynabup
```

Rebuild only:

```sh
docker compose build
```

Show running containers/services:

```sh
docker compose ps
```
