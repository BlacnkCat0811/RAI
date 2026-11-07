// Package diagnostics implements pure, deterministic, research-only running
// diagnostics for four independently versioned models:
//
//   - rai.workout_durability/v1
//   - rai.aerobic_efficiency/v1
//   - rai.critical_speed/v1
//   - rai.load_settling/v1
//
// Every model carries an independent identity and is explicitly marked
// Experimental. The package has no I/O, no clock, no global mutable policy and
// no randomness. It imports only the Go standard library and deliberately does
// not depend on any other internal business package or on presentation code.
//
// All timestamps are Unix microseconds. Results are candidate research
// observations with explicit status, reason codes, support and evidence
// references. They are not medical diagnoses, not predictions of recovery or
// sleep, not VO2max or running economy, not lactate threshold, and not a claim
// about maximal endurance.
package diagnostics
