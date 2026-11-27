# Setup Guide

This guide gets Pushboy running locally, then shows how to connect real APNS and FCM credentials when you are ready to send to devices.

Pushboy is designed to run behind your own backend, private network, API gateway, or reverse proxy. Do not expose a fresh Pushboy install directly to the public internet. The `/v2` key, user, and topic routes authenticate tenant server keys. Legacy `/v1` routes remain unauthenticated and globally scoped. Rate limiting and TLS termination remain deployment responsibilities.

## Requirements

- Go 1.24+
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
cd ~/pushboy
docker compose up --build
```

Or clone the repository manually:

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

Create a smoke-test user:

```bash
curl -X POST http://localhost:8080/v1/users/ \
  -H "Content-Type: application/json" \
  -d '{"id":"demo-user"}'
```

List topics:

```bash
curl http://localhost:8080/v1/topics/
```

The Compose file creates a local Postgres database and mounts `./keys` into the Pushboy container as read-only credentials.

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

## Tenant bootstrap

Set `DATABASE_URL` to the intended PostgreSQL database before creating the app and its first key.

```bash
go run ./cmd/pushboy bootstrap "Example App"
```

On success, save the server key from stdout in your backend's secret manager. If the command prints a complete key and reports a commit error, verify that the key authenticates before retrying. App names must be unique. Keep server keys on trusted backends and send them in the Authorization header over HTTPS outside local development. Redact credentials in proxy and tracing logs.

To rotate a bootstrap key, load it into `PUSHBOY_SERVER_KEY` and issue a replacement with `keys:manage`.

```bash
OLD_KEY_ID=${PUSHBOY_SERVER_KEY%%.*}
curl -X POST http://localhost:8080/v2/keys \
  -H "Authorization: Bearer $PUSHBOY_SERVER_KEY" \
  -H "Content-Type: application/json" \
  -d '{"scopes":["read","send","users:write","devices:write","topics:write","keys:manage","providers:manage"]}'
```

Save the new `key` from the response immediately. For a scoped issuer, request only scopes it has and supply an `expiresAt` within its lifetime if it expires. Once the backend has switched, load the replacement into `PUSHBOY_SERVER_KEY` and revoke the old key.

```bash
curl -X DELETE "http://localhost:8080/v2/keys/$OLD_KEY_ID" \
  -H "Authorization: Bearer $PUSHBOY_SERVER_KEY"
```

## Tenant users and topics

Use a tenant server key with `users:write` and `topics:write` for the corresponding create requests.

```bash
curl -X POST http://localhost:8080/v2/users \
  -H "Authorization: Bearer $PUSHBOY_SERVER_KEY" \
  -H "Content-Type: application/json" \
  -d '{"identity":{"namespace":"accounts","externalId":"account-42"}}'

curl -X POST http://localhost:8080/v2/topics \
  -H "Authorization: Bearer $PUSHBOY_SERVER_KEY" \
  -H "Content-Type: application/json" \
  -d '{"id":"releases","name":"Release notes"}'
```

Send `{}` to create an anonymous user. With a key that has `read`, use `GET /v2/users/{uid}` with the returned UUID or `GET /v2/topics/{topicId}` with the percent-encoded topic ID. Migration 18 adds separate tables for these routes and leaves legacy data in place.

To retry user creation after a lost response, generate and save a UUID before sending it in the `Idempotency-Key` header. Send the same key and identity on the retry to receive the original `201` and UID.

## Tenant installation ownership

Enroll an installation with `PUT /v2/devices/{installationUUID}`. To identify or log out that installation, send a new `operationId` and the current `expectedOwnershipVersion` to the corresponding `/identify` or `/logout` endpoint from your trusted backend. Every distinct successful logout advances the ownership version, including when the installation keeps its existing anonymous UID.

Retry a lost response using the same operation ID and body. If a newer operation has succeeded or the expected version is stale, the server returns `409`. Use an empty enrollment PUT to read the current UID and version. Before issuing a new operation, check whether the current authenticated session still calls for it. A delayed identify request can otherwise undo a later logout if the backend retries it with a refreshed version.

## Tenant provider routes

Set `PROVIDER_ENCRYPTION_ACTIVE_KEY_ID` to a versioned ID such as `v1`. Inject `PROVIDER_ENCRYPTION_KEYS` from your deployment secret manager as a JSON object mapping key IDs to standard base64-encoded 32-byte random keys. Generate each key independently, for example with `openssl rand -base64 32`. Keep the JSON out of source control, logs, and database backups. Docker Compose passes both variables through to Pushboy when they are set.

Each tenant can configure separate APNs sandbox and production routes and FCM production routes. APNs bundle comparisons ignore case and the route retains the registered bundle spelling. The bearer key determines the tenant. Use a key with `providers:manage` to create a route. Send the APNs key ID and team ID associated with the downloaded signing key.

```bash
jq -n --rawfile privateKey keys/AuthKey_YOUR_KEY_ID.p8 \
  '{provider:"apns",environment:"sandbox",applicationId:"com.example.app",
    credentials:{keyId:"YOUR_KEY_ID",teamId:"YOUR_TEAM_ID",privateKey:$privateKey}}' |
  curl -fsS -X POST http://localhost:8080/v2/providers \
    -H "Authorization: Bearer $PUSHBOY_SERVER_KEY" \
    -H "Content-Type: application/json" --data-binary @-
```

For FCM, set `provider` to `fcm`, `environment` to `production`, and `applicationId` to the Firebase project that will receive the send request. A service account from another project needs permission to send messages in the target project. Supply the JSON object in `credentials`. Pushboy validates Google's OAuth token endpoint and the RSA signing key. Use HTTPS outside local development and exclude provider request bodies and authorization headers from logs and traces.

With a key that has `read`, use `GET /v2/providers` or `GET /v2/providers/{providerId}` to check each route's `ready` flag. An unready route reports a reason such as `encryption_key_unavailable` or `credential_unreadable`. Readiness covers local decryption and signer or token source setup. Apple and Google authorization are verified through provider responses during delivery. These tenant routes currently support configuration and local readiness. Notification sends, Live Activity sends, and channel management continue through the legacy deployment configuration.

Provider, environment, and application ID define the stable route namespace. A duplicate create returns `409`. To rotate a provider credential, send `PUT /v2/providers/{providerId}` with the current `credentialVersion` and the replacement `credentials`. The route keeps its ID and advances the credential version. A stale version returns `409`. Read the route after a lost response before issuing a new rotation. To disable a route, send its current `credentialVersion` and `enabled: false` in the PUT body. Re-enable it by uploading credentials with `enabled: true`.

Back up every encryption key ID and value in a secret store independently of PostgreSQL. A database restore needs the keys referenced by the restored rows. Test recovery with a restored database and keyring on an isolated deployment, then verify readiness for each enabled route. Missing keys make their routes unready. Pushboy skips malformed encryption key entries and logs a diagnostic without including key material. If the active key is missing or malformed, credential writes receive `503` and stored routes can still be checked with their valid retained keys.

To rotate the deployment encryption key, distribute the new and old keys to every instance first. Then set the new active key ID everywhere and re-upload each route's credential with its current version. Check readiness for each enabled route. In PostgreSQL, verify that every row in `provider_configs` uses the new `credential_key_id` before retiring the old key from the running keyring. Retain old keys for backups that reference them.

## Tenant migration rollback

Pause tenant writes and migration runners before rolling back. Migration 20 requires an empty `provider_configs` table. Use a pre-upgrade database backup or a compatible application and migration bundle when provider configurations already exist. Migration 19 requires that no saved user creation replay records exist. Migration 18 requires empty `tenant_users`, `tenant_user_identities`, and `tenant_topics` tables. Migration 17 requires empty `tenants` and `tenant_api_keys` tables. Keep populated migrations installed and verify that any rollback binary and its migration bundle support the installed schema.

A blocked down records the previous version as dirty. Verify the newer schema and its data are intact, then restore the recorded version with the corresponding `force 20`, `force 19`, `force 18`, or `force 17` before restarting the application.

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

Create a user:

```bash
curl -X POST http://localhost:8080/v1/users/ \
  -H "Content-Type: application/json" \
  -d '{"id":"user-123"}'
```

Register a token:

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
