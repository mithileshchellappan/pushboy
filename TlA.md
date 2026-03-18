# TLA+ for Pushboy

This document explains what TLA+ is, why it is worth using in Pushboy, and how to apply it without turning the project into a research exercise.

## Short version

Pushboy is a strong TLA+ candidate because it is not just an HTTP API. It is an asynchronous delivery system with:

- persistent jobs
- scheduled jobs
- in-memory queueing
- concurrent workers
- per-job sender fanout
- delivery receipts
- shutdown and restart behavior

Those are exactly the kinds of systems where normal unit tests are necessary but not sufficient.

TLA+ helps answer questions like:

- Can a job be written to the database and then get stranded forever?
- Can a scheduled job be promoted twice?
- Can a job become `COMPLETED` while receipts are still missing?
- Can shutdown or restart leave jobs in an invalid state?
- Can counters and job status disagree with receipt history?

## What TLA+ is

TLA+ is a formal specification language for describing a system as:

- a set of state variables
- an initial state
- allowed state transitions
- properties that must always hold

You do not use it to replace Go code. You use it to model the important behavior of the system before or alongside implementation.

The usual workflow is:

1. Write a small abstract model of the system.
2. Define invariants and liveness properties.
3. Run the TLC model checker.
4. Let TLC explore thousands of possible interleavings and edge cases.
5. Fix the design or code when TLC finds a counterexample.

TLA+ is good at finding design bugs in concurrent and stateful systems. It is not useful for UI details, HTTP serialization, or formatting logic.

## Why Pushboy is a good fit

Pushboy already has a clear delivery pipeline:

- request handlers create jobs in [`internal/server/server.go`](./internal/server/server.go)
- job creation and validation happen in [`internal/service/service.go`](./internal/service/service.go)
- scheduled jobs are promoted by [`internal/scheduler/scheduler.go`](./internal/scheduler/scheduler.go)
- workers and senders execute jobs in [`internal/worker/pool.go`](./internal/worker/pool.go) and [`internal/pipeline/pipeline.go`](./internal/pipeline/pipeline.go)
- durable state is stored via [`internal/storage/storage.go`](./internal/storage/storage.go) and [`internal/storage/postgres.go`](./internal/storage/postgres.go)

That means Pushboy already has the ingredients for subtle bugs:

- a job may exist in the database but not in the in-memory queue
- two components may disagree about valid job states
- crashes may break assumptions about eventual processing
- receipt insertion may fail after dispatch attempts already happened
- scheduled jobs may race with workers or restarts

TLA+ is useful here because those are system-level correctness questions, not line-level questions.

## Why it would help this repo specifically

### 1. Delivery state machine correctness

Pushboy effectively has a job lifecycle:

- `SCHEDULED`
- `QUEUED`
- `IN_PROGRESS`
- `COMPLETED`
- `FAILED`

That state machine exists across multiple files, not in one explicit place. TLA+ would force it into one model and one set of rules.

### 2. Crash and restart reasoning

Immediate jobs are persisted first, then submitted to the worker pool from the HTTP layer. That means there is a real design question:

- what happens if the process dies after the database write but before in-memory submission?

This is exactly the sort of gap formal modeling is good at exposing early.

### 3. Scheduled promotion semantics

Scheduled jobs are promoted by the scheduler and updated in storage. TLA+ can verify properties like:

- once due, a scheduled job is eventually promoted
- a scheduled job is never promoted twice
- promotion does not skip required intermediate states

### 4. Receipt and status consistency

Pushboy records delivery receipts in batches and updates job status separately. That raises correctness questions like:

- can a job be `COMPLETED` while receipts are still missing?
- can receipt insertion fail without causing the job to fail?
- can `success_count + failure_count` diverge from actual recorded receipts?

### 5. Existing consistency smells

The current storage interface includes `FetchPendingJobs()`, but the visible runtime path mainly uses `QUEUED` jobs and scheduler promotion. There is also a status mismatch in the storage layer where `FetchPendingJobs()` queries `PENDING` while the rest of the code uses `QUEUED`, `SCHEDULED`, `IN_PROGRESS`, `COMPLETED`, and `FAILED`.

This is exactly the kind of thing TLA+ helps surface: not because the syntax is wrong, but because the system has not been modeled as one coherent state machine.

## What TLA+ would not help much with

TLA+ is probably not worth using for:

- APNs payload formatting
- FCM request serialization
- HTTP handler validation details
- Postman examples
- simple CRUD paths like topic creation or token deletion

Normal Go tests are better for those.

## The first model to write

Do not model the whole codebase. Model one thing:

`PublishJobLifecycle`

That model should focus on:

- jobs in durable storage
- jobs in the in-memory queue
- jobs owned by workers
- receipts recorded so far
- whether the process is running or has crashed
- whether a scheduled job is due

### Suggested state variables

- `jobs`
  - map of job ID to job state and metadata
- `queue`
  - in-memory queue of job IDs
- `inFlight`
  - jobs currently being processed by workers
- `receipts`
  - recorded delivery receipts
- `now`
  - abstract time
- `systemUp`
  - whether the process is running

### Suggested actions

- `CreateImmediateJob`
- `CreateScheduledJob`
- `EnqueueImmediateJob`
- `PromoteScheduledJob`
- `WorkerStartsJob`
- `DispatchToToken`
- `RecordReceipt`
- `CompleteJob`
- `FailJob`
- `Crash`
- `Restart`

### Suggested invariants

- Every job is in exactly one valid status.
- A job cannot be both queued and in-flight.
- A scheduled job cannot be in-flight before it is due.
- A completed job cannot return to a non-terminal state.
- Every receipt refers to a real job.
- A job cannot be `COMPLETED` unless all targeted tokens have either succeeded or failed.
- A job cannot target both a topic and a user at the same time.
- A job cannot target neither a topic nor a user.

### Suggested liveness properties

- If the system stays up long enough, every queued job is eventually processed.
- If a scheduled job becomes due and the system stays up, it is eventually promoted.
- If a worker starts a job and dispatch eventually finishes, the job eventually becomes terminal.

## How TLA+ works in practice

The core idea is simple:

### 1. Model state, not code

You do not model goroutines, SQL queries, or channels exactly as written. You model the meaning of the system:

- job exists
- job is queued
- worker owns job
- receipt is recorded
- system crashes

### 2. Define allowed transitions

Each action says when a transition is allowed and what it changes.

Example:

- `PromoteScheduledJob` is only allowed when a job is `SCHEDULED` and `scheduledAt <= now`
- `CompleteJob` is only allowed when the job is `IN_PROGRESS` and all tokens have been accounted for

### 3. Define properties

There are two major classes:

- safety properties
  - "something bad never happens"
  - example: a job is never both queued and completed
- liveness properties
  - "something good eventually happens"
  - example: a due scheduled job eventually gets processed

### 4. Run the model checker

TLC explores combinations of actions and interleavings that are easy to miss in tests.

If it finds a violation, it gives you a counterexample trace:

- state 1: job inserted
- state 2: queue submission skipped
- state 3: process crashes
- state 4: restart does not recover queued work
- violation: queued job is never processed

That trace is the value. It tells you where the design assumption failed.

## What the Pushboy workflow should look like

### Step 1

Write a tiny first model for just 2 or 3 jobs and a very small queue.

Do not try to model APNs or FCM network behavior beyond success or failure.

### Step 2

Model only the job lifecycle first.

Ignore rich payload fields, badge values, images, and notification content.

### Step 3

Add crash and restart.

This is where a lot of delivery systems go wrong.

### Step 4

Turn counterexamples into code and tests.

For example:

- add a recovery loop for stranded queued jobs
- unify job status names
- make terminal state updates depend on receipt durability
- add integration tests around restart recovery

## Recommended adoption path

Keep it lightweight.

### Phase 1: documentation and model sketch

- create a `specs/` folder
- add `PublishJobLifecycle.tla`
- document the real job states and allowed transitions

### Phase 2: one bounded model

- small number of jobs
- small number of workers
- tiny token sets
- crash and restart enabled

### Phase 3: use it to drive implementation

- align status vocabulary across storage, scheduler, worker, and server
- add recovery semantics for persisted-but-not-submitted jobs
- tighten invariants around receipts and terminal job states

## Why this is worth the effort

Pushboy is trying to be a push delivery system. Users do not care that the code compiles if:

- notifications disappear
- jobs stall silently
- scheduled deliveries are skipped
- counters lie
- restarts lose work

TLA+ is worth using here because it helps prove the delivery pipeline design is coherent under concurrency and failure, not just under happy-path tests.

## Practical recommendation

If you use TLA+ in Pushboy, start with exactly one question:

`Can a persisted job ever be lost or stranded before reaching a terminal state?`

If the model answers that confidently, it will already pay for itself.
