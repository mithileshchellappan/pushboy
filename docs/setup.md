# Setup Guide

This guide gets Pushboy running locally, then shows how to connect real APNS and FCM credentials when you are ready to send to devices.

Pushboy is designed to run behind your own backend, private network, API gateway, or reverse proxy. Do not expose a fresh Pushboy install directly to the public internet. The current API does not include built-in auth, rate limiting, tenant isolation, or TLS termination.

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
DATABASE_DRIVER=postgres
DATABASE_URL=postgres://user:password@localhost:5432/pushboy?sslmode=disable
```

Run the server:

```bash
go run ./cmd/pushboy
```

Pushboy runs Postgres migrations from `db/migrations/postgres` during startup. If startup succeeds, the database schema is ready.

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
| `DATABASE_DRIVER` | Use `postgres`. |
| `DATABASE_URL` | Postgres connection string. |
| `WORKER_COUNT` | Number of master workers that fan out jobs into token batches. |
| `SENDER_COUNT` | Number of sender workers that call APNS/FCM. |
| `JOB_QUEUE_SIZE` | In-process queue buffer size. |
| `BATCH_SIZE` | Number of tokens loaded per Postgres batch. |
| `MAX_RETRY_NOTIFICATION` | Retry count used by notification outcome paths. |
| `BROADCAST_TOPIC_NAME` | Topic auto-created on startup and assigned to new users. |

See `.env.example` for the complete list.

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
