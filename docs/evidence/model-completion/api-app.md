# Formal API and App evidence

Implemented routes:

- `GET /v2/analysis/athlete` (schema2 typed product; legacy active → 409 `not_product_activated`)
- `GET /v2/analysis/athlete/history` (`metric` ∈ six metrics, limit 1–90, `view` revised/as_known)
- `POST /v2/model-inputs/{kind}` (strict JSON, 64 KiB, canonical body, idempotent receipt; aliases hr-references/sleep-targets/...)
- `POST /v2/model-inputs/{id}/revocations` (append-only, target-schema bound, idempotent)

Reader property: `ReadAthleteProduct` selects the active assignment and one batch of publications/artifacts; it validates clean generation, SUCCEEDED run, schema/model/version and content digests; it does not call `Calculate`, read raw HealthKit payloads, or import `internal/admin`. Current snapshot path uses 2 SELECT statements in the implementation (context + artifact batch), under the 6-SELECT budget; this is a code-path count, not a measured production query count.

Executed evidence:

```bash
swift test
xcodebuild -project RAI.xcodeproj -scheme RAI -configuration Debug -sdk iphonesimulator \
  -destination 'id=D44B6925-BB19-4556-B8AE-94B758B3B532' build
```

Observed: 236 Swift tests PASS; Simulator build `** BUILD SUCCEEDED **`. The Swift schema2 test verifies RAI VO₂ 52, experimental flag, `rai_owned_submax` source and independent external VO₂. The typed-input Swift form posts HR reference/sleep target to the formal routes and renders server reasons.

Executed E2E: `bash scripts/local/test.sh e2e` PASS (105s) launched the Simulator against loopback Go HTTP and disposable PG18; `TestWorkoutCompletenessSimulatorHTTPPostgres` passed with contract=v2, two manifests, twelve streams and lost-ACK retry. This covers the completeness flow only; it does not render or assert the schema2 athlete page/history.

Not executed: App rendering of the schema2 athlete page/history against authenticated PG plus Worker publication; VoiceOver/max dynamic type/narrow screen visual matrix; candidate-free product UI assertion beyond static boundary scan. Do not treat the build or completeness E2E as a schema2 product E2E PASS.

## Round 29 update (2026-11-07)

- schema2 product E2E is now executed: `bash scripts/local/test.sh e2e` PASS (90 s), real Simulator App → loopback Go HTTP → disposable PG18 → Worker → promote/rollback; `MODEL_STACK_SIMULATOR_HTTP_PG=PASS active_home=true workout_detail=true history=true rollback=true`, with real requests to `/v2/handshake`, `/v2/workouts`, `/v2/analysis/athlete` and `/v2/analysis/athlete/history`. Log `.local/logs/model-completion-fixes/e2e-race.log`.
- `swift test` PASS: 238 tests / 34 suites, log `.local/logs/model-completion-fixes/swift-after-research.log`.
- Authorized real-device D01 PASS: Release 0.6.0(1) signed install on iPhone 12 Pro Max / iOS 27.2; read-only list observed 100 visible Workouts (51 eligible / 49 excluded / 0 unclassified), original session manifest unchanged. Evidence `.local/device-acceptance/install-result.json`, `.local/logs/model-completion-fixes/device/workout-list-report.json`.
- Real-device D02 FAIL: completeness BLOCKED after 17 Workouts because one Workout had `metadata_requires_review` on several quantity types; no receipt or committed anchors were fabricated. Evidence `.local/logs/model-completion-fixes/device/completeness-report.json`, `client-task-state.json`.
- Still not executed: VoiceOver / maximum dynamic type / narrow-screen visual matrix, full background-recovery matrix, real research outcomes and real product activation.
