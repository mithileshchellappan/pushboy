# Pushboy

Pushboy is a Go/PostgreSQL notification backend with separate normal-push and Live Activity delivery lanes. Keep the implementation small, explicit, and predictable.

## Code cleanliness

- Solve the requested behavior with the smallest coherent change. Include the related contract, storage, worker, and documentation changes; leave unrelated cleanup separate.
- Prefer direct, idiomatic Go and existing dependencies. Justify new packages, interfaces, configuration options, and dependencies with a current need.
- Keep short, straightforward code inline. Extract a helper when it names substantial logic or centralizes a real shared rule. Do not add wrappers that only rename a call, split code just to shorten functions, or extract production helpers solely to make private functions testable.
- Keep related rules together. HTTP handlers decode, authorize, call the relevant operation, and map results. Account ownership and token lifecycle must not be independently reimplemented by each handler or worker.
- Use explicit types and ordinary control flow. Return contextual errors with `%w`; use `errors.Is`/`errors.As` and typed provider errors where appropriate instead of matching error text.
- Remove obsolete helpers and duplicate paths when replacing behavior. Do not retain speculative abstractions or commented-out implementations.
- Comments explain constraints, invariants, and external quirks that the code cannot express. Do not narrate obvious steps or maintain implementation histories in comments.

## Ownership and security

- Treat a tenant as one app. Scope reads, writes, credentials, jobs, and relationships to the authenticated tenant; a caller-supplied tenant or user ID is not authorization.
- A device ID represents an app installation. Device metadata and provider tokens belong to the installation; external identities, entitlements, and account preferences belong to the user.
- Token refresh must not change account ownership. Login/logout are explicit, atomic operations, protected against stale requests. Keep normal and Live Activity token stores and delivery lanes separate.
- Keep server keys, installation credentials, administrator access, and APNs/FCM credentials distinct. Never embed a privileged server key in a client or commit secrets, raw tokens, real user data, or provider payloads.
- Redact credentials, raw tokens, and sensitive payloads before writing logs, receipts, fixtures, or issues. HTTP errors can contain a token in the request URL.
- Production investigations are read-only unless the user authorizes mutation. Never send a Live Activity `end` as a test or cleanup without explicit authorization.

## Testing

- Test a meaningful observable behavior or regression that existing tests do not already protect. Name what would break before adding a test.
- Add a focused reproducing test for a bug fix and cover significant new behavior. Every important tenant, authorization, ownership, lifecycle, and migration rule needs coverage at the most useful level; a coverage percentage is not the objective.
- Prefer a few strong tests and table-driven cases over repeated suites. Do not test constants, field assignments, standard-library behavior, pass-through mocks, or private helpers already covered through their callers.
- Use real disposable PostgreSQL databases for foreign keys, transactions, concurrent updates, backfills, and migration behavior. Mocked SQL expectations do not prove these properties. Required integration tests must actually run before calling a database change ready.
- Stub provider HTTP calls locally. Live provider tests are opt-in and require appropriate authorization; never use production credentials or data in the normal suite.
- Keep tests deterministic: use explicit synchronization and controllable clocks/delays instead of sleeps. Isolate environment variables and temporary files.
- Run focused checks while iterating, then the relevant full tests, race checks, vet, build, and contract checks for the final change, following CONTRIBUTING.md and CI. Reuse passing results while their inputs remain unchanged. Documentation-only changes need proportionate checks.
- Fix valid review findings and verify their regressions. Check automated review claims against source and primary documentation before changing behavior.

## Migrations and compatibility

- Prefer additive schema changes, a resumable backfill, a verified cutover, and later removal of obsolete fields. Preserve existing token IDs, delivery history, invalidation, expiry, and subscription eligibility.
- Initially map each legacy token row to its own provisional device. Do not infer shared hardware from a user ID, token similarity, or metadata. Do not fabricate missing device metadata or last-seen times.
- Create a provisional device and attach its token in one transaction. Re-running or resuming a backfill must not create extra devices or resurrect inactive tokens.
- Existing identifiers must remain resolvable during the compatibility period. Later device reconciliation moves only authenticated, explicitly reported bindings and preserves the user's other devices.
- Rehearse against representative existing data. A migration must not enqueue notifications, replay historical jobs, or end running activities.
- Keep long data backfills out of application startup. Document the cutover and rollback limits; an old binary is not a safe rollback after it stops understanding tenant/ownership changes.

## Documentation and delivery

- Update OpenAPI and user examples when their observable contract changes. Document migration procedures and durable decisions; avoid field catalogs, control-flow narration, and duplicate planning documents.
- Track the overhaul and dependencies in GitHub issues. Keep each PR focused on a complete, verifiable behavior and include the issue reference and decisive validation results.
- The approved overhaul order and migration contract live in [roadmap #47](https://github.com/mithileshchellappan/pushboy/issues/47).
- Report what changed, what was verified, and any material remaining limitation concisely. Do not equate queued work or provider acceptance with device delivery.
