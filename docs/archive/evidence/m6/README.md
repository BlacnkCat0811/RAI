# M6 synthetic engineering evidence — 2026-11-04

Candidate-only; no production, deployment or device acceptance. Final decision is in [M6 Result](../../M6_RESULT.md). The original model paper and prior phase evidence remain unchanged.

- [baseline.json](baseline.json): original status/HEAD/branch/stat, DEVELOPER_DIR and 40 frozen file hashes.
- [frozen-and-sqlc.json](frozen-and-sqlc.json): old source/migration/checksum preservation, sqlc regeneration and toolchain versions.
- [go-checks.txt](go-checks.txt): Go normal/race/vet/integration vet, dependency boundary, domain benchmark.
- [final-boundary-go.txt](final-boundary-go.txt): final future-baseline boundary and affected-domain race verification.
- [full-postgres.txt](full-postgres.txt): full isolated PostgreSQL18/race suite; terminal status and cluster removal.
- [focused-postgres.txt](focused-postgres.txt): final M6/migration tests, faults, three backend deaths, fences, roles/raw rollback, rebuild and M2 unit guard.
- [revision-and-rebuild.txt](revision-and-rebuild.txt): real M2 parameter revision, independent M5B identity and 18-step incremental/rebuild.
- [performance.txt](performance.txt), [performance.json](performance.json): final 25-case matrix plus persisted-dependency queue regression. 14d correctly collecting; old history is sparse. Timings include race detector and local concurrent regression; no production SLO.
- [query-plan.txt](query-plan.txt): explicit forced-index capability EXPLAIN/ANALYZE, with dataset identity removed. No claim that this is the natural production planner.
- [swift-package.txt](swift-package.txt): actual full package attempt fails because installed CommandLineTools lacks Testing. No Swift/UI changes or Simulator UI acceptance.
- [validation.json](validation.json): final command outcomes, limitations, counts and source hashes.

Numerical evidence is synthetic. Ordinary CLI/Worker output is redacted. A UUID/privacy scan is recorded in validation.json. No artifacts imply physiological validity, production completeness or real training safety.
