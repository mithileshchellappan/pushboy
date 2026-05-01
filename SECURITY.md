# Security Policy

Pushboy is self-hosted notification infrastructure. Treat it like an internal backend service, not a public unauthenticated API.

## Supported Status

The current codebase is pre-1.0. Security fixes are handled on the default branch until tagged releases begin.

| Version | Supported |
| --- | --- |
| `main` | Yes |
| Tagged releases | Not published yet |

## Current Security Model

Pushboy currently does not include built-in API authentication, tenant isolation, rate limiting, TLS termination, or dashboard roles.

Do not expose a Pushboy instance directly to the public internet. Put it behind one of:

- Your own trusted backend.
- A private network.
- An API gateway with authentication and rate limiting.
- A reverse proxy that terminates TLS and enforces access control.

The current data model is single-tenant. Users, device tokens, topics, jobs, delivery receipts, APNS credentials, and FCM credentials belong to one deployment boundary.

## Sensitive Data

Pushboy stores device tokens, notification payloads, Live Activity payloads, delivery receipts, and job metadata in Postgres. These values may be user data in your application.

Recommended deployment controls:

- Use a private Postgres instance.
- Encrypt database storage at rest.
- Restrict direct database access.
- Rotate APNS and FCM credentials if they are ever copied into logs, tickets, issue comments, or shell history.
- Mount APNS `.p8` files and Firebase service-account JSON as runtime secrets, not committed files.
- Keep `.env`, `keys/`, `.p8` files, service-account JSON, database dumps, and local SQLite files out of git.

## Reporting A Vulnerability

Please do not report security vulnerabilities in public GitHub issues.

Preferred flow after this repository is public:

1. Use GitHub private vulnerability reporting for this repository if it is enabled.
2. Include the affected route, deployment assumptions, reproduction steps, and expected impact.
3. Avoid including real device tokens, APNS keys, Firebase service accounts, database URLs, or production payloads.

If private vulnerability reporting is not enabled yet, contact the maintainer through their GitHub profile and ask for a private disclosure channel.

## What To Report

Security reports are especially useful for:

- Authentication or authorization bypasses once native auth is added.
- Cross-user or cross-tenant data access.
- Device-token exfiltration or unauthorized token deletion.
- Unauthorized notification or Live Activity dispatch.
- Secret leakage in images, logs, errors, examples, or release artifacts.
- SQL injection, request smuggling, SSRF, path traversal, or unsafe file access.
- Denial-of-service issues against the API, worker pools, or database.

## Known Gaps

These are known limitations and should not be reported as new vulnerabilities unless you have a concrete exploit path beyond the documented behavior:

- No built-in API auth yet.
- No tenant or app boundary yet.
- No native TLS listener; terminate TLS before Pushboy.
- No built-in rate limiting yet.
- In-process queues are not durable across crashes or suitable for multi-replica shared dispatch.
- `/v1/ping` is a liveness check, not a full readiness or dependency check.

## Maintainer Checklist Before Public Launch

- Enable GitHub private vulnerability reporting.
- Keep secret scanning enabled.
- Require CI before merge once CI is added.
- Build release images from a clean checkout.
- Run a full history scan before changing repository visibility.
- Avoid publishing example credentials, real device tokens, production payloads, or database dumps.
