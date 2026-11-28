# Setup Guide

This guide gets Pushboy running locally, then shows how to connect real APNS and FCM credentials when you are ready to send to devices.

Tenant user management, direct user sends, and receipt reads require an app-specific server key. Legacy routes, including topic and Live Activity sends, still accept unauthenticated requests. Run Pushboy on a private network or behind an authenticated gateway. The deployment handles TLS termination and rate limiting.

## Requirements

- Go 1.24+ for source builds
- Postgres 16 or compatible Postgres server
- Docker and Docker Compose, if you want the containerized path
- APNS `.p8` key for iOS sends
- Firebase service-account JSON for FCM sends

APNS and FCM credentials are optional for local API setup. If neither provider is configured, Pushboy still starts and lets you create users, topics, tokens, and jobs, but provider sends cannot be delivered.

## Option 1: Docker Compose

Docker Compose is the fastest way to try Pushboy with Postgres.

Use the setup script:

```bash
curl -fsSL https://raw.githubusercontent.com/mithileshchellappan/pushboy/main/scripts/setup.sh | sh
```

Run the command printed by the installer. Set `PUSHBOY_VERSION` to a release tag to select its checked-out configuration and matching image. The installer saves that selection in `.env` for future `docker compose up` commands. Installations that link to the same settings file share its release selection. The release notes also list the image digest for deployments that pin by digest.

Set `PUSHBOY_VERSION=main` in the installer to clear the release image selection and build from source. You can also clone the repository manually:

```bash
git clone https://github.com/mithileshchellappan/pushboy.git
cd pushboy
mkdir -p keys
docker compose up --build
```

Check the server:

```bash
curl http://localhost:8080/v1/ping
```

Expected response:

```text
pong
```

List topics:

```bash
curl http://localhost:8080/v1/topics/
```

The Compose file creates a local Postgres database and mounts `./keys` into the Pushboy container as read-only credentials.
On Linux, grant the container user (UID and GID `10001`) permission to read the mounted credential files and access their directory.

## Option 2: Local Go Process

Use this path when you already have Postgres running locally.

```bash
git clone https://github.com/mithileshchellappan/pushboy.git
cd pushboy
cp .env.example .env
```

Create a database:

```bash
createdb pushboy
```

Set `DATABASE_URL` in `.env` to your local database:

```dotenv
DATABASE_URL=postgres://user:password@localhost:5432/pushboy?sslmode=disable
```

Run the server:

```bash
go run ./cmd/pushboy
```

Pushboy runs Postgres migrations from `db/migrations/postgres` during startup. If startup succeeds, the database schema is ready.

## Install a release binary

Download the archive for your OS and CPU, along with `SHA256SUMS`. Release archives use `darwin` for macOS and `arm64` for Apple silicon. Verify the archive before extracting it:

```bash
archive=pushboy_v0.1.0_darwin_arm64.tar.gz
awk -v archive="$archive" '$2 == archive' SHA256SUMS | shasum -a 256 -c &&
  mkdir -p pushboy &&
  tar -xzf "$archive" -C pushboy &&
  (cd pushboy && ./pushboy --version)
```

On Linux you can use `sha256sum -c` in place of `shasum -a 256 -c`. Set `DATABASE_URL` in the environment or in `.env`. Start `./pushboy` from the extracted directory so it can find the bundled migrations.

## Upgrade and rollback

Keep the current release and take a Postgres backup before starting an upgrade. Start the new binary with the migrations from its archive, or start the new image with the existing Postgres volume. Check `/v1/ping` and the API calls your clients use before resuming traffic.

Rolling back across a schema migration requires a database compatible with the older release. To restore the pre-upgrade database, stop Pushboy, restore the backup and then start the old release with its matching migrations. Restoring that backup discards any database changes made after it was taken.

## Tenant bootstrap

Set `DATABASE_URL` to the intended PostgreSQL database before creating the app and its first key.

```bash
go run ./cmd/pushboy bootstrap "Example App"
```

If you started Pushboy with Docker Compose, run the bootstrap command inside the service:

```bash
docker compose exec pushboy /app/pushboy bootstrap "Example App"
```

Bootstrap stores the tenant and its first server key. Save the printed key in your backend's secret manager. For the examples below, set `PUSHBOY_SERVER_KEY` in your shell to the printed key.

## Tenant user API

Create an identified user from a verified backend account with a key that has `users:write` scope:

```bash
curl -fsS -X POST http://localhost:8080/v1/users \
  -H "Authorization: Bearer $PUSHBOY_SERVER_KEY" \
  -H "Content-Type: application/json" \
  -d '{"identity":{"namespace":"account","externalId":"abc"},"requestId":"32a9bc2e-22fa-4a69-9a67-a3a198984ccb"}'
```

For an anonymous user, send `{}` instead. Both requests return HTTP 201 with a Pushboy-generated `uid` and a `kind` of `identified` or `anonymous`. A separate create request for an existing identity in the same tenant returns HTTP 409.

To retry creation, supply a nonzero, lowercase hyphenated UUID as `requestId`. Repeating the same identity or anonymous request with that ID in the same tenant returns HTTP 201 and the original UID. Reusing the ID with a different valid identity or user kind returns HTTP 409. Each tenant has its own request IDs. Deleting an unused user releases its request ID, so a later retry creates a new user. Omit `requestId` or set it to `null` to create without replay.

For a specific user, set `PUSHBOY_USER_UID` in your shell to the returned `uid`. List or fetch users with a key that has `read` scope:

```bash
curl -fsS "http://localhost:8080/v1/users?limit=20" \
  -H "Authorization: Bearer $PUSHBOY_SERVER_KEY"

curl -fsS "http://localhost:8080/v1/users/$PUSHBOY_USER_UID" \
  -H "Authorization: Bearer $PUSHBOY_SERVER_KEY"
```

The list returns `items` in newest-first order, plus `next_cursor` and `has_more`. When `has_more` is true, pass `next_cursor` as the next request's `cursor` query parameter with a key for the same tenant.

Clean up an unused user with a key that has `users:write` scope:

```bash
curl -fsS -X DELETE "http://localhost:8080/v1/users/$PUSHBOY_USER_UID" \
  -H "Authorization: Bearer $PUSHBOY_SERVER_KEY"
```

Deletion returns HTTP 204, or HTTP 409 if retained installations or notification job history still reference the user. Fetch and delete return HTTP 404 for an invalid UID or a user outside the authenticated tenant. A missing or invalid bearer key returns HTTP 401. A valid key without the required scope returns HTTP 403. [Full account retirement is tracked in issue #49](https://github.com/mithileshchellappan/pushboy/issues/49).

Use a key with `send` for `POST /v1/users/{uid}/send` and a key with `read` for `GET /v1/notifications/{jobID}` or `GET /v1/notifications/{jobID}/receipts`. Pushboy snapshots recipients for scheduled and immediate sends when it accepts the job. It skips installations whose ownership, registration or route changed before submission. A lost provider reply produces an unknown outcome and the worker avoids replaying that attempt. If your database contains encrypted tenant provider credentials, retain their key IDs and values in a separate secret store for as long as backups reference them.

Tenant installation setup, push registration, and encrypted provider provisioning use the backend storage layer. The legacy token registration example below creates a user in the legacy user namespace.

## Tenant migration rollback

Drain and stop pre-25 API processes, schedulers and workers before accepting tenant user sends. Earlier releases can read or claim jobs belonging to a tenant. Migration 25 preserves the creation time for first registrations and rotations with no later confirmation. If the device reconfirmed the same endpoint, schema 24 lacks that confirmation time, so migration 25 uses the upgrade time. Subsequent registrations record their actual confirmation time.

Migration 24 replaces the legacy token uniqueness index while holding a table lock. Measure the lock window on a restored database with representative data before scheduling the migration. Drain and stop the old processes and workers before starting the new release. An older binary cannot start against schema 24 with its pre-migration bundle.

Before downgrading, drain HTTP traffic and queued work, then stop every Pushboy API process, worker, scheduler, and automatic migration runner. Use a migration bundle that includes the installed schema version. Start the release compatible with the target schema only after the down migration completes. The following migrations require their listed tables or records to be empty before they can be rolled back.

| Migration | Tables or records that must be empty |
| --- | --- |
| 25 | Tenant-scoped rows in `publish_jobs` and `delivery_receipts` |
| 24 | Tenant rows in `tokens` and all rows in `tenant_push_registration_operations` |
| 23 | `tenant_live_activity_channels` |
| 22 | `tenant_device_ownership_operations` |
| 21 | `tenant_devices` |
| 20 | `provider_configs` |
| 19 | Saved user creation replay records |
| 18 | `tenant_users`, `tenant_user_identities`, and `tenant_topics` |
| 17 | `tenants` and `tenant_api_keys` |

If the database holds state required by a migration, keep that migration installed and use a compatible binary and migration bundle, or follow the backup recovery procedure above. Verify the target schema version is clean before starting an older image.

A blocked down migration records the previous version as dirty. Verify the newer schema and its data are intact, then use the migration tool's `force` command with the newer version number before restarting the application. For a blocked migration 25 down, that command is `force 25`.

## Provider Credentials

Create a local `keys` directory for provider credentials:

```bash
mkdir -p keys
```

The `keys` directory is ignored by git. Do not commit APNS keys or Firebase service-account JSON.

### APNS

Create an APNS auth key in the Apple Developer portal, then put the `.p8` file under `keys`.

Set these values in `.env`:

```dotenv
APNS_KEY_ID=YOUR_KEY_ID
APNS_TEAM_ID=YOUR_TEAM_ID
APNS_BUNDLE_ID=com.example.app
APNS_KEY_PATH=keys/AuthKey_YOUR_KEY_ID.p8
APNS_USE_SANDBOX=true
```

Use `APNS_USE_SANDBOX=true` for development tokens and `false` for production tokens. Live Activities use the same bundle id with Apple-specific Live Activity push headers handled by Pushboy.

Live Activity channels are available whenever APNS is configured. A channel is
created lazily by the first topic-scoped Live Activity start, or explicitly
through the channel management endpoint for a manual-start flow. A capable
client must still opt into channel-based starts. Channel management and
broadcast publishing reuse the existing Live Activity APNs client and follow
`APNS_USE_SANDBOX`; there is no separate channel worker or connection-pool
configuration. Run separate Pushboy deployments for sandbox and production
credentials, tokens, and channels.

Allow outbound HTTP/2 and TLS connections to Apple's channel-management
endpoint for the selected environment:

- sandbox: `api-manage-broadcast.sandbox.push.apple.com:2195`;
- production: `api-manage-broadcast.push.apple.com:2196`.

Broadcast publishing uses `api-broadcast.sandbox.push.apple.com:443` or
`api-broadcast.push.apple.com:443`, respectively. Direct device pushes use the
standard APNs sandbox or production endpoint on port `443`.

### FCM

Download a Firebase service-account JSON file and place it at:

```text
keys/service-account.json
```

Set this value in `.env`:

```dotenv
FCM_KEY_PATH=keys/service-account.json
```

Pushboy reads the Firebase `project_id` from the service-account JSON.

## Configuration

The main runtime settings are:

| Variable | Purpose |
| --- | --- |
| `SERVER_PORT` | HTTP bind address. Default is `:8080`. |
| `DATABASE_URL` | Postgres connection string. |
| `WORKER_COUNT` | Number of master workers that fan out jobs into token batches. |
| `SENDER_COUNT` | Number of sender workers that call APNS/FCM. |
| `JOB_QUEUE_SIZE` | Pending push-job buffer size. |
| `TASK_QUEUE_SIZE` | Buffer between push fanout and senders. |
| `DLQ_QUEUE_SIZE` | Buffer between push senders and outcome persistence. |
| `LA_JOB_QUEUE_SIZE` | Pending Live Activity job buffer size. |
| `LA_TASK_QUEUE_SIZE` | Buffer between Live Activity fanout and senders. |
| `LA_DLQ_QUEUE_SIZE` | Buffer between Live Activity senders and outcome persistence. |
| `BATCH_SIZE` | Number of tokens loaded per Postgres batch. |
| `OUTCOME_BATCH_SIZE` | Outcomes persisted per database flush. |
| `OUTCOME_FLUSH_SECONDS` | Maximum seconds between outcome flushes. |
| `SHUTDOWN_TIMEOUT_SECONDS` | Maximum seconds spent draining in-memory work on shutdown. |
| `APNS_CLIENT_POOL` | Number of push-lane APNs HTTP/2 client transports. |
| `APNS_MAX_CONCURRENT` | Push-lane APNs in-flight cap; `0` derives from the pool size. |
| `LA_APNS_CLIENT_POOL` | Live Activity APNs transports; defaults to `APNS_CLIENT_POOL`. |
| `LA_APNS_MAX_CONCURRENT` | Live Activity APNs in-flight cap; defaults to `APNS_MAX_CONCURRENT`. |
| `FCM_CLIENT_POOL` | Number of push-lane FCM HTTP client transports. |
| `FCM_MAX_CONCURRENT` | Push-lane FCM in-flight cap; `0` derives from the pool size. |
| `LA_FCM_CLIENT_POOL` | Live Activity FCM transports; defaults to `FCM_CLIENT_POOL`. |
| `LA_FCM_MAX_CONCURRENT` | Live Activity FCM in-flight cap; defaults to `FCM_MAX_CONCURRENT`. |
| `MAX_RETRY_NOTIFICATION` | Provider retries after the initial APNS or FCM attempt. |
| `BROADCAST_TOPIC_NAME` | Topic auto-created on startup and assigned to new users. |

See `.env.example` for the complete list.

### Production throughput profile

The non-secret overrides currently validated on the production Pushboy VM are
versioned in `deploy/production-throughput.env.example`. Merge those values
into the server's existing `.env`; do not replace database or provider
credentials. The profile keeps deployment-specific capacity choices separate
from conservative application defaults.

## Verify API Setup

Health check:

```bash
curl http://localhost:8080/v1/ping
```

To verify authenticated user management, follow the [tenant user API examples](#tenant-user-api).

### Legacy token registration and topic publishing

Register a token to create a legacy user with the supplied legacy user ID:

```bash
curl -X POST http://localhost:8080/v1/users/tokens \
  -H "Content-Type: application/json" \
  -d '{
    "id": "user-123",
    "platform": "fcm",
    "token": "device-token"
  }'
```

Publish to the default broadcast topic:

```bash
curl -X POST http://localhost:8080/v1/topics/broadcast/publish \
  -H "Content-Type: application/json" \
  -d '{
    "title": "Hello",
    "body": "Pushboy is running."
  }'
```

If provider credentials are missing, the job can be created but real device delivery cannot happen.

## Common Issues

### The server cannot connect to Postgres

Check `DATABASE_URL`, confirm Postgres is running, and confirm the database exists. In Docker Compose, the app uses the internal host name `postgres`, not `localhost`.

### Migrations cannot be found

Run Pushboy from the repository root when using `go run ./cmd/pushboy`. The migration path is `db/migrations/postgres`.

### APNS sends fail

Confirm the `.p8` file exists at `APNS_KEY_PATH`, `APNS_KEY_ID` matches the key, `APNS_TEAM_ID` matches your Apple team, and `APNS_BUNDLE_ID` matches the app that produced the device token. Development tokens require `APNS_USE_SANDBOX=true`.

### FCM sends fail

Confirm `FCM_KEY_PATH` points to a valid service-account JSON and that the target device token belongs to the Firebase project in that JSON.

### Docker cannot read credentials

The Compose file mounts `./keys` into `/app/keys` as read-only. Put `AuthKey_*.p8` and `service-account.json` under the local `keys` directory before starting the stack.
