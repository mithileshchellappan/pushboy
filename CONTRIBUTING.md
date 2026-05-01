# Contributing

Thanks for taking the time to improve Pushboy.

Pushboy is still pre-1.0, so contributions that clarify behavior, harden operations, improve API safety, add tests, or make deployment easier are especially valuable.

## Ground Rules

- Keep changes narrow and behavior-focused.
- Do not commit secrets, device tokens, APNS `.p8` files, Firebase service-account JSON, `.env` files, database dumps, or production payloads.
- Do not include real user data in issues, pull requests, tests, fixtures, screenshots, or logs.
- Prefer explicit docs when behavior is intentionally limited. For example, the current runtime is Postgres-backed and single-node, with in-process worker queues.
- Open security reports privately. See [SECURITY.md](SECURITY.md).

## Local Setup

Requirements:

- Go 1.24+
- Postgres
- Docker Desktop or a compatible Docker runtime if you want to test containers

Clone the repo and copy the example environment:

```bash
git clone https://github.com/mithileshchellappan/pushboy.git
cd pushboy
cp .env.example .env
```

Set `DATABASE_URL` to a local Postgres database. Provider credentials are optional for API and worker smoke tests, but real APNS/FCM sends require valid credentials mounted outside git.

Run the app locally:

```bash
go run ./cmd/pushboy
```

Check the server:

```bash
curl http://localhost:8080/v1/ping
```

## Docker Setup

Run Pushboy with Postgres through Compose:

```bash
docker compose up --build
```

Check the server:

```bash
curl http://localhost:8080/v1/ping
```

Stop and remove the local Compose stack:

```bash
docker compose down -v
```

## Verification

Run these before opening a pull request:

```bash
go mod tidy -diff
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/pushboy
ruby -e "require 'yaml'; YAML.load_file('docs/openapi.yaml')"
git diff --check
```

The repository currently has no committed test files. If your change touches scheduling, token lifecycle, dispatch, provider error handling, Live Activity lifecycle, storage, or API behavior, add focused tests as part of the change.

For Docker-related changes, also run:

```bash
docker build -t pushboy:dev .
docker compose up --build
curl http://localhost:8080/v1/ping
docker compose down -v
```

## API Changes

If you change request or response behavior:

- Update [docs/openapi.yaml](docs/openapi.yaml).
- Update README examples when the public workflow changes.
- Keep current wire behavior explicit, even when it is imperfect.
- Call out compatibility breaks in the pull request.

## Database Changes

Database changes must include migrations under `db/migrations/postgres`.

Migration expectations:

- Use sequential migration numbers.
- Include both `.up.sql` and `.down.sql` files unless a rollback is not possible.
- Keep migrations readable by the non-root Docker user.
- Test migrations through `docker compose up --build` from a clean volume.

## Pull Request Checklist

Before requesting review:

- The change is scoped to one clear problem.
- Documentation and OpenAPI updates are included when public behavior changes.
- Verification commands and results are listed in the PR description.
- No secrets or real user data are present.
- Docker still builds if deployment files changed.
- Known limitations are documented instead of hidden.

## Good First Contributions

Useful starter areas:

- Tests for config parsing, Live Activity option parsing, scheduled date validation, and pipeline close behavior.
- CI workflow for Go checks, OpenAPI parsing, and Docker build.
- `SECURITY.md` follow-up once GitHub private vulnerability reporting is enabled.
- API response DTOs with stable JSON casing.
- Native API key auth.
- Better readiness checks.
- Metrics for queue depth, provider errors, and dispatch latency.
