# Changelog

All notable changes to Pushboy will be documented in this file.

This project is pre-1.0. Until the first tagged release, changes are tracked on `main` and in pull request history.

## Unreleased

### Added

- MIT license.
- Dockerfile and Docker Compose setup for local Postgres-backed runs.
- OpenAPI 3.1 spec for the current HTTP API.
- Expanded README with quick start, architecture, Live Activity support, comparison table, and production caveats.
- Security policy and contributing guide.
- Code of conduct, issue templates, pull request template, and CI workflow.

### Changed

- Documented Postgres as the supported runtime database.
- Clarified that the current worker pools are in-process and single-node.
- Removed the learning notes directory from the public repo surface while preserving the project origin in the README.

### Known Gaps

- No built-in API auth yet.
- No tenant or app boundary yet.
- No durable distributed queue yet.
- No committed test suite yet.
- No native API auth or durable queue CI coverage yet.
