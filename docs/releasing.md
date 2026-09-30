# Releasing

Releases are fully automatic. There is nothing to configure and no manual
steps: no personal access tokens, no secrets, no repository settings. The
whole process lives in the `release` job of
[`.github/workflows/push.yaml`](../.github/workflows/push.yaml).

## How it works

Every merge to `master` that touches Go code, `go.mod`/`go.sum`, the
`Dockerfile`, or the workflow itself runs the CI pipeline. Once tests pass
and the multi-arch image is published, the `release` job:

1. Takes the latest `X.Y` tag and bumps it by one: `1.8` → `1.9` → `1.10`.
   No `v` prefix, matching the existing tag scheme.
2. Adds the new version tag to the already-published container image on
   GHCR. The image is not rebuilt — the version tag points at the same
   digest as `latest`, so its build provenance attestation still applies.
3. Creates the git tag and a GitHub release with auto-generated notes
   (the list of merged pull requests since the previous release).

Everything runs with the workflow's own `GITHUB_TOKEN`; the permissions it
needs (`contents: write`, `packages: write`) are declared on the job itself
and override the repository default, so the "Workflow permissions" setting
can stay read-only.

## What does and does not trigger a release

- A merge touching code, dependencies, the `Dockerfile`, or the workflow:
  **releases**.
- A merge touching only documentation or other files outside the paths
  filter: **does not release** (the pipeline does not run at all).
- A pushed tag: **does not release itself** — it takes the existing manual
  tag path (hermetic no-cache build, image published under that tag), and
  the auto-release job is skipped, so nothing recurses.

## Making a major version bump

Automation only ever bumps the minor. To start a new major, create a
release for e.g. tag `2.0` manually — either via the GitHub UI ("Draft a
new release", type the new tag) or `git tag 2.0 && git push origin 2.0`
plus a release for it. Automation picks it up from there: the next merge
becomes `2.1`.

## Failure modes

- A run superseded by a newer push to `master` is cancelled; the next
  successful run self-heals (the version is recomputed from tags, and
  re-tagging the image is idempotent).
- Two runs racing for the same version cannot both succeed: creating an
  existing release fails loudly instead of double-tagging.
- If tag rulesets are ever added to the repository, `github-actions[bot]`
  must stay allowed to create `X.Y` tags, or the release job will fail.
