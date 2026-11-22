# Release checklist

Releases are prepared through a pull request, then tagged on the merged commit
on `main`. Publishing a GitHub Release does not deploy the application to the VPS.

## Versioning

Use `MAJOR.MINOR.PATCH` versions with a `v` prefix on Git tags:

- `v0.1.0`: first versioned application baseline
- `v0.1.1`: compatible fixes and small maintenance updates
- `v0.2.0`: new functionality or significant changes

While the project is below `v1.0.0`, compatibility may change. Document breaking
changes and migration steps explicitly; use a minor version bump for breaking
changes during this period. Reserve `v1.0.0` for a deliberate stable-compatibility
commitment, not simply because the app is deployed.

A `0.x` version is not automatically a GitHub pre-release. Use a pre-release tag
such as `v0.2.0-rc.1` and GitHub's pre-release checkbox for a release candidate.

## 1. Prepare the release PR

- Start a short-lived release-preparation branch from current `main`.
- Choose an unused version and review all changes since the previous release.
  For the first release, use the historical milestones in `CHANGELOG.md` as context.
- Move the relevant `Unreleased` entries into the version's changelog section.
- Leave `Unreleased` in place for subsequent work.
- Replace the pending heading with the version and actual planned release date,
  for example `## 0.1.0 - YYYY-MM-DD`, and remove the preparation-only note.
  Do this before merging; if publication is delayed, correct the date through a PR
  before tagging.
- Include known limitations and any configuration, database, or deployment
  migration steps. Do not imply that an image or binary is published unless it is.
- Run `make ci`, or confirm the complete CI workflow passes on the PR.
  Tests must not require production credentials or run a production sync locally.
- Merge the PR only after review and green CI.

For the first release, the prepared `0.1.0` changelog section summarizes the
current application; the historical milestones remain as background rather than
being retroactively assigned invented release versions.

## 2. Tag the exact merged commit

From a clean working tree:

```sh
git switch main
git pull --ff-only
git status --short
git log -5 --oneline
git tag --list
```

Stop if the working tree is not clean. Identify the release-preparation PR's
merged commit and confirm its CI checks passed. If later PRs have already merged,
do not tag the latest commit by accident.

Create an annotated tag for that exact commit. Replace `RELEASE_COMMIT_SHA` with
the full verified commit SHA before running these commands:

```sh
git show --no-patch --format=fuller RELEASE_COMMIT_SHA
git tag -a v0.1.0 RELEASE_COMMIT_SHA -m "Release v0.1.0"
git show --no-patch v0.1.0
git push origin v0.1.0
```

Use the chosen version instead of `v0.1.0` for future releases. Push only the
intended tag, not every local tag. Never move or force-update a published release
tag; issue a new patch release for corrections to released code.

## 3. Publish the GitHub Release

1. Open the repository's **Releases** page and choose **Draft a new release**.
2. Select the existing tag `v0.1.0`; do not create a different tag from the UI.
3. Use `v0.1.0` as the release title.
4. Copy the matching version's changelog notes, including known limitations and
   migration guidance. Do not copy the historical milestones as additional releases.
5. Leave **Set as a pre-release** unchecked for this baseline release.
6. Review and publish the release.

This process publishes release notes and GitHub's source archives only. It does
not upload compiled binaries, publish a container image, or deploy to the VPS.

## 4. Deploy separately

Follow [the deployment runbook](deployment.md) for state handling, verification,
and operational safety. To deploy this exact release from the repo on the VPS:

```sh
git fetch origin --tags
git switch --detach v0.1.0
docker compose up -d --build
docker compose ps
curl --fail http://127.0.0.1:8080/healthz
docker compose logs --tail=100 ynabup
```

Checking out a tag leaves Git in detached HEAD state; `git pull` is not the next
update command in this state. To return to branch-based deployment, use
`git switch main` followed by `git pull --ff-only` before rebuilding.

Keep the production `.env` and SQLite state volume. Before database-changing
releases, arrange a consistent backup and check whether older code can read the
new schema. Rolling code back does not undo database changes or transactions
already written to YNAB.

A successful health request confirms the endpoint is reachable, not that every
sync operation succeeded. Inspect reconciliation logs and `last_synced_at` too.
