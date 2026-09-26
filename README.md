# Ticket admin API

Requires Go 1.22+, PostgreSQL, and an existing database. Apply migrations in order before starting:

```sh
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f 001_tickets.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f 002_admin.sql
```

Set `DATABASE_URL` and `TOKEN_SECRET` (at least 32 random bytes). Do not commit passwords, hashes, or secrets. `LISTEN_ADDR` defaults to `:8080` (all interfaces, for container networking); set it to `127.0.0.1:8080` for local-only access. Run `go run .`; run `go test ./...` for tests. Set `TEST_DATABASE_URL` to a reachable PostgreSQL database to additionally run the first-admin integration test; it uses a session-local temporary table and does not alter persistent admin records.

For initial registration, set `ENABLE_LOCAL_SETUP=true` **only while the server is reachable from a trusted local network**. `GET /api/setup/status` returns `needsSetup`; `POST /api/setup` accepts JSON `{"username":"admin","password":"at-least-12-characters"}` and creates the sole admin exactly once. A second registration returns 409. Disable `ENABLE_LOCAL_SETUP` after registration and restart the server; with setup disabled, registration returns 403 and setup status reports `needsSetup: false` regardless of whether an admin exists. Do not expose setup to the public Internet, and keep the password confidential. Existing deployments must create an admin through this flow after applying `002_admin.sql`; legacy `ADMIN_USERNAME` and `ADMIN_PASSWORD_HASH` environment variables are not used.

`POST /api/login` accepts JSON username/password and returns a bearer token valid for eight hours. Keep the token confidential. `GET /api/tickets`, `POST /api/tickets`, `PUT /api/tickets/{id}`, and `DELETE /api/tickets/{id}` require `Authorization: Bearer <token>`. DELETE marks a ticket cancelled; cancelled tickets cannot be edited. Ticket fields are passenger, flight, origin, destination, departure (RFC3339 timestamp), and status. Create always starts booked. Listing is capped at 500 newest tickets. `/healthz` checks process availability; `/readyz` checks database connectivity and that both migration tables exist; it returns 503 if either is missing. Serve the built frontend and proxy `/api` to this server at the same origin in production; configure TLS at the serving layer. Rotating `TOKEN_SECRET` invalidates all outstanding tokens.
