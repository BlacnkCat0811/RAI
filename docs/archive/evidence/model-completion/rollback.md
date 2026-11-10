# Synthetic promotion / rollback evidence

Command:

```bash
bash scripts/local/test.sh integration ./integration -run 'TestSyntheticPromotionAndRollbackAudits'
```

`TestSyntheticPromotionAndRollbackAudits` creates a disposable dataset, writes explicit `active=legacy` and `candidate=synthetic-v2` assignments, validates a synthetic-only promotion gate, promotes to active with `previous=legacy`, verifies the append-only audit, rolls back to `legacy`, and checks the returned active stack.

Observed: PASS; `RAI_TEST_LEFTOVER_DATABASES=0`; cluster removed.

Not covered: real dataset gates, consumer compatibility probes, cache/in-flight request coherence, bounded old-stack catch-up, shadow aggregate suppression, rollback failure audit. Real activation is NOT AUTHORIZED / NOT RUN.

## Round 29 update

- `TestSyntheticPromotionAndRollbackAudits` and `TestManagedModelStackPublishesWorkoutDetailAndWithdrawsOnDelete` PASS in a fresh isolated PG18 (2.664 s, leftover=0, cluster removed); log `.local/logs/model-completion-fixes/integration-promotion.log`.
- Simulator App E2E exercised real HTTP → PG → Worker → promote → rollback: `rollback=true` in `ModelStackSimulatorHTTPPostgres`; log `.local/logs/model-completion-fixes/e2e-race.log`.
- Real activation, real dataset gates, cache/in-flight coherence and bounded old-stack catch-up remain NOT RUN/PARTIAL; synthetic evidence is not promoted to real activation.
