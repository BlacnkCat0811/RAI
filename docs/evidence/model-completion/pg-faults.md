# PostgreSQL migration, role and fault evidence

Environment: fresh disposable PostgreSQL 18.6 cluster from `scripts/test-postgres.sh`; fsync, full_page_writes, synchronous_commit and data checksums enabled. No external/production DSN.

Executed:

```bash
bash scripts/local/test.sh integration ./internal/admin -run 'Test.*Migration|TestMigrations'
bash scripts/local/test.sh integration ./integration -run 'TestModelInputIdempotencyAndConflict|TestProductReaderRejectsLegacyActive|TestSyntheticPromotionAndRollbackAudits'
```

Observed: PASS, `RAI_TEST_LEFTOVER_DATABASES=0`, `RAI_TEST_CLUSTER_REMOVED=1` for both. Migration 00030–00035 apply on fresh and incremental schemas; checksums for 00001–00029 are unchanged; old `model_input_kind_schema` is replaced additively in 00032.

Covered here: input/receipt transaction and idempotent retry, conflicting canonical body rejection, immutable single row, legacy-active schema2 refusal, synthetic active→previous assignment audit, rollback to legacy.

Not executed and therefore not claimed: backend termination at each publication boundary, two-worker lease/fencing race, N+1 generation race, two delete lock orders, real role matrix for API/Worker/owner/admin, `EXPLAIN ANALYZE` plans, full 180-day/180-workout rebackfill. These require a dedicated fault-injection harness and are marked PARTIAL/NOT RUN in acceptance.json.
