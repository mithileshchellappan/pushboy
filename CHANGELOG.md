# Changelog

All notable changes to Pushboy will be documented in this file.

This project is pre-1.0. Breaking changes can happen between 0.x releases.

## v0.1.0

This preview still uses the shared `/v1` API before tenant authentication. Run it on a trusted network or behind an authenticated gateway.

### Added

- APNs broadcast channels for supported Live Activity starts and CI checks for Go, PostgreSQL, OpenAPI and Docker Compose.
- Versioned Linux and macOS archives for AMD64 and ARM64, bundled Postgres migrations, SHA-256 checksums, and multi-architecture images on GitHub Container Registry.
- `pushboy --version` reports the release tag and commit. The setup script offers the image for the selected release.

### Changed

- Use `GET /v1/notifications/{jobID}` to read a publish job. The former `GET /v1/topics/{topicID}/publish/{jobID}` endpoint was removed.

## v0.0.0 - 2026-05-01

### Added

- MIT license.
- Dockerfile and Docker Compose setup for local Postgres-backed runs.
- Docker-oriented setup script at `scripts/setup.sh`.
- OpenAPI 3.1 spec for the current HTTP API.
- Expanded README with quick start, setup docs, architecture, Live Activity support, comparison table, cost model, and production caveats.
- Security policy and contributing guide.
- Code of conduct, issue templates, and pull request template.

### Changed

- Documented Postgres as the supported runtime database.
- Clarified that the current worker pools are in-process and single-node.

### Removed

- Removed the learning notes directory from the public repo surface while preserving the project origin in the README.

### Known Gaps

- No built-in API auth yet.
- No tenant or app boundary yet.
- No durable distributed queue yet.
- No committed test suite yet.
- No CI workflow yet. CI is planned as a separate follow-up PR.
