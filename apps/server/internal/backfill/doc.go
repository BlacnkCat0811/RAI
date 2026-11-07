// Package backfill defines the pure planning domain for the E6 unified
// bounded-backfill workflow: plan, execute, resume, status, diff and cancel.
//
// The package performs no I/O. It does not read PostgreSQL, files, environment
// variables or the wall clock, it never formats a scalar health value, and it
// never publishes anything. Storage, jobs and the admin CLI own all of that and
// pass an explicit Request plus facts they already know.
//
// The commands map onto the pure layer as follows:
//
//   - plan: Plan builds deterministic output Blocks (at most MaxBlockDays per
//     block) and the DAG warmup windows needed before the first output day.
//     Planning is read-only and returns no health values.
//   - execute: consumes PlanResult.Blocks in order. Intermediate blocks are staged;
//     only the storage/job layer may mark them complete and publish a final
//     candidate. A block is identified by InputHash, TargetAsOf and its date
//     span, so a retry can never publish the same block twice.
//   - resume: Resume keeps the identity of unfinished blocks and rejects a
//     completed set that was produced by different request parameters.
//   - status: Summarize derives pending/running/complete/failed/cancelled
//     counts from blocks; Complete is useful only when every block is complete.
//   - diff: Diff compares two snapshots. It is redacted by default: state,
//     reason, null transitions, version/mode/unit boundaries and value buckets
//     are reported, never scalar values. DiffWithOptions exposes values only
//     for synthetic or explicitly approved research output.
//   - cancel: Cancel marks unfinished blocks cancelled. It removes no raw fact,
//     acknowledged receipt or historical artifact; that remains the storage
//     layer's contract.
//
// All dates are civil calendar days in the form YYYY-MM-DD and all date math
// uses time.Parse/time.AddDate in UTC. No local timezone or ambient clock is
// consulted.
package backfill
