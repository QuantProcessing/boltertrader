# Docker-Free Startup Identity Reconciliation Design

## Problem

`scripts/dev-platform` records a supervised process command after a short
stability sample and then requires an exact PID, start-time, and command match.
The canonical PostgreSQL launcher performs first-run initialization before it
`exec`s `postgres`. The PID and start time remain unchanged, but the command
legitimately changes after the initial sample, so readiness rejects the owned
process and rolls the stack back.

## Decision

Treat command identity as provisional only while a service is starting.
`start_service` continues to record the PID, immutable process start time, and
the currently observed command. While `wait_service` is polling readiness, it
may atomically refresh only the command record, and only after proving that the
recorded PID is alive and its process start time is unchanged. Once readiness
succeeds, the launcher performs one final refresh and strict ownership check;
the resulting command record is sealed because no non-startup path refreshes
it.

Normal `status`, port ownership, drain, and shutdown paths continue to use the
existing strict `is_owned` check. A PID reuse, changed start time, missing
process, unreadable command, or post-readiness command change remains
fail-closed.

## Alternatives

- Increase the initial settling delay. This is timing-dependent and still
  fails when initialization takes longer than expected.
- Introduce a separate persisted pending/ready ownership state machine. This
  makes the phase explicit but adds more state and recovery behavior than this
  narrowly scoped launcher bug requires.

## Testing

Add a lifecycle regression whose PostgreSQL command remains a wrapper long
enough for the initial identity sample, then `exec`s a final process that alone
signals readiness. Before the fix, startup must fail with an ownership error.
After the fix, startup, strict `status`, and shutdown must pass, and the sealed
command record must identify the final process. Existing mismatched-ownership
tests continue proving that unrelated and post-readiness processes are never
stopped.

## Scope And Safety

This change touches only `scripts/dev-platform` and
`scripts/dev_platform_test.go`. It does not change ports, service commands,
credentials, Test/Live policy, migration state, or venue write authority.

