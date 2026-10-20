# Ticket admin API

Requires Go 1.22+, PostgreSQL, and an existing database. Apply migrations in order before starting:

```sh
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f 001_tickets.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f 002_admin.sql
```

Set `DATABASE_URL` and `TOKEN_SECRET` (at least 32 random bytes). Do not commit passwords, hashes, or secrets. `LISTEN_ADDR` defaults to `:8080` (all interfaces, for container networking); set it to `127.0.0.1:8080` for local-only access. Run `go run .`; run `go test ./...` for tests. Set `TEST_DATABASE_URL` to a reachable PostgreSQL database to additionally run the first-admin integration test; it uses a session-local temporary table and does not alter persistent admin records.

For initial registration, set `ENABLE_LOCAL_SETUP=true` **only while the server is reachable from a trusted local network**. `GET /api/setup/status` returns `needsSetup`; `POST /api/setup` accepts JSON `{"username":"admin","password":"at-least-12-characters"}` and creates the sole admin exactly once. A second registration returns 409. Disable `ENABLE_LOCAL_SETUP` after registration and restart the server; with setup disabled, registration returns 403 and setup status reports `needsSetup: false` regardless of whether an admin exists. Do not expose setup to the public Internet, and keep the password confidential. Existing deployments must create an admin through this flow after applying `002_admin.sql`; legacy `ADMIN_USERNAME` and `ADMIN_PASSWORD_HASH` environment variables are not used.

`POST /api/login` accepts JSON username/password and returns a bearer token valid for eight hours. Keep the token confidential. `GET /api/tickets`, `POST /api/tickets`, `PUT /api/tickets/{id}`, and `DELETE /api/tickets/{id}` require `Authorization: Bearer <token>`. DELETE marks a ticket cancelled; cancelled tickets cannot be edited. Ticket fields are passenger, flight, origin, destination, departure (RFC3339 timestamp), and status. Create always starts booked. Listing is capped at 500 newest tickets. `/healthz` checks process availability; `/readyz` checks database connectivity and that both migration tables exist; it returns 503 if either is missing. Serve the built frontend and proxy `/api` to this server at the same origin in production; configure TLS at the serving layer. Rotating `TOKEN_SECRET` invalidates all outstanding tokens.

## Backend delivery to dev

Pull requests run application tests, GitOps tooling tests, an ARM64 image
build and container health checks. They do not publish or propose deployments.

A successful push to `dev`:

1. Exports the tested image without rebuilding it.
2. Publishes it to ECR with a run/attempt-specific tag.
3. Selects the exact repository manifest digest from Docker RepoDigests.
4. Uses a separate job to propose that digest in GitOps.

The proposal changes only `image.digest` in
`AncientGear/dealengine-argo-ops/environments/dev/values.yaml`.
It uses a unique branch per workflow run and attempt and opens a PR
against `main`. If the digest is already selected, no PR is created.

## Authentication and deployment control

The backend repository requires:

- Repository variable `GITOPS_APP_ID`.
- Repository Secret `GITOPS_APP_PRIVATE_KEY`.

The GitHub App must be installed only on the GitOps repository with
Contents and Pull requests write permissions. Installation tokens are
temporary and revoked when the job finishes. Never print or commit keys
or tokens.

The pipeline does not approve or merge PRs and has no cluster credentials.
Merging the GitOps PR triggers Argo auto-sync in dev.

Proposal jobs are serialized, but chronological deployment order is not
guaranteed. Review the source commit, build and digest before merging;
an older proposal must not accidentally replace a newer intended release.

If an API operation fails, the job stops and may leave a branch without
a PR. Inspect the failed run before retrying. No force-push or automatic
cleanup is performed.

## Scope and limitations

Automatic proposals currently support dev only.
Staging and production require separately reviewed configuration.

Digest-selection failure stops the proposal, even if the image was
already published. Docker image IDs are not registry manifest digests.
The current selection mechanism is intended for this single-platform
ARM64 publication flow.

Local tests simulate GitHub and validate file mutation; they do not prove
live permissions or deployment readiness. Rollback follows the manual
GitOps PR procedure documented in the GitOps chart README.

## Local GitOps tooling checks

Requires Python 3 and PyYAML 6.0.3. Run from this repository:

    python3 scripts/tests/test_select_image_digest.py -v
    python3 scripts/tests/test_update_dev_digest.py -v
    python3 scripts/tests/test_propose_dev.py -v

These tests do not access AWS or GitHub and do not modify live GitOps files.