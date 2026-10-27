# RAI V2 P3.5 iPhone sync client

Native SwiftUI iPhone target `RAI`, bundle `com.rai.v2.ios`, iOS 18+, Swift 6 with complete concurrency checks. Only the sync debug screen is implemented. No analysis or training features. The frozen layout is `ios/RAI`, as specified in `docs/V2_PHASE0_BASELINE.md`.

## Build and tests

Open `RAI.xcodeproj` in Xcode. The checked-in project is sufficient; after changing `project.yml`, regenerate from this directory using `xcodegen generate`. XcodeGen 2.46 has a [nested capability serialization bug](https://github.com/yonaskolb/XcodeGen/issues/1637); the narrow post-generation hook fixes and validates the HealthKit dictionary without upgrading dependencies.

```bash
swift test
xcodebuild -project RAI.xcodeproj -scheme RAI -destination 'generic/platform=iOS' CODE_SIGNING_ALLOWED=NO build
# Select an available simulator UUID with xcrun simctl list devices.
xcodebuild -project RAI.xcodeproj -scheme RAI -destination 'platform=iOS Simulator,id=<SIMULATOR_UUID>' test
```

`swift test` runs the Swift Testing suites, including 108 Go-generated route vectors. Its initial XCTest discovery may print zero; the final Swift Testing test count is the relevant result. The Xcode tests exercise synthetic HealthKit quantity conversions on a simulator. An unsigned iPhone build does not prove signing, installation or real-device operation.

For signing, copy `Config/Developer.xcconfig.example` to the ignored `Config/Developer.xcconfig`, supply an Apple Development Team, then build for a connected, unlocked, trusted iPhone. The HealthKit entitlement is read only; there is no HealthKit save/delete call or write authorization request.

## Isolated development configuration

Use the debug screen to save the API origin, collector epoch and device bearer token to Keychain (`AfterFirstUnlockThisDeviceOnly`). The handshake binds the service/dataset/epoch/collector identity to local state; changing it is rejected. Do not use the existing fnOS `rai` database for acceptance. Credentials are never logged or committed.

HTTPS is required in Release. Debug permits HTTP only for the exact host configured by `RAI_DEV_HTTP_HOST` (default `rai-dev.local`) and its scoped ATS exception. No global arbitrary-load exception is used. Provide a resolvable development hostname or TLS endpoint before a real-device run; personal IPs are not in source.

## Durable state and boundaries

- `HealthKitClient` implements a small mockable `HealthKitReading` boundary, using Apple's anchored-query async descriptor. Global Workout/sample streams are unfiltered and retain their delivered anchor across launches. Query limits are 25 Workouts or 250 quantity changes; each foreground pass is bounded. Cadence is not fabricated: this SDK has no direct running-cadence quantity type. Running speed/power are read where available.
- Read authorization is requested only for Workout, heart rate, walking/running distance, active energy, running speed, running power, step count and Workout routes. The UI reports that access was requested, because [HealthKit does not reveal read authorization status](https://developer.apple.com/documentation/healthkit/authorizing-access-to-health-data).
- HealthKit anchors stay in protected, backup-excluded Application Support files. Immutable gzip payloads precede the atomic `state.json` replacement. A pending page owns its candidate anchor. Each validated batch ACK advances its sequence; the validated sealed page receipt and manifest are required before the delivered anchor advances.
- Workout/sample deletion is a normal immutable stream event. Local tombstones prevent cached replay. Source-deleted parents cancel obsolete local relationship work without synthesizing server receipts or advancing the relationship anchor.
- Workout body is immutable. Allowed nil-to-value total/allowlisted metadata additions use versioned enrichment. Unsupported statistics or incompatible source changes stop the affected stream with a retained anchor and an inspectable error; other fact streams continue. No source data is silently converted to a normal value.
- Multi-point quantity samples use the frozen series protocol. Oversized Workouts use the existing detail-package protocol; unsupported single-detail bounds stop safely. Workout/sample facts are prioritized ahead of dependent routes and associations.
- Associations use HealthKit's actual Workout relationship predicate and per-Workout/type local anchors. Missing parents retain their original request; a new durable fact generation wakes a waiting association. Conflicting bindings remain blocked for inspection.
- Routes use a per-Workout local anchored query, a bounded 1,000-point spool, immutable gzip files, exact Go canonical floating-point representation, both SHA-256 chains, manifest registration, chunk ACK validation and seal. The 5,000-point limit is a protocol limit. Source order and missing measurements are preserved.
- The stable background URLSession uploads files, reconnects tasks by page/cursor and persists responses before processing them. Cancelling a foreground wait leaves the system upload alive. Background completion drains saved work without reading HealthKit. A user force-quit requires reopening the App; [iOS does not automatically relaunch force-quit Apps for background transfers](https://developer.apple.com/documentation/foundation/urlsessionconfiguration/background(withidentifier:)). Correctness does not depend on BGProcessingTask, which is not added in this phase.
- Retryable transport/408/429/5xx errors keep the same identities and files, with exponential backoff capped at 60 seconds and Retry-After respected. Protocol, identity, authorization and receipt conflicts stop automatic retry. `Retry Saved Work` explicitly retries retained work after inspection.
- Statistics logs contain only counts, byte sizes, retries and timings: HealthKit read, normalization, encode, gzip, network, batch ACK, route read/chunk/seal. `route_upload_ms` is total wall time from durable staging through seal, including queue wait, suspension and retries; per-chunk network time is separate. DB commit timing is not inferred from network time. No payload, GPS, heart-rate series or credentials are logged.

Delivered payload/receipt files are currently retained in the app sandbox; storage growth and real-device memory/latency need measurement before accepting long-term operation.

## Synthetic fnOS checks

From the repository root:

```bash
RAI_TEST_GOPROXY=https://goproxy.cn,direct bash scripts/test-remote.sh bash ../../scripts/check-p35-server.sh
RAI_TEST_GOPROXY=https://goproxy.cn,direct bash scripts/test-remote.sh bash ../../scripts/test-p3-e2e.sh
RAI_TEST_GOPROXY=https://goproxy.cn,direct bash scripts/test-remote.sh bash ../../scripts/p35-route-vectors.sh
```

The Swift probe uses the same SyncCore but is a macOS command-line **test harness**, not a desktop client. Start its disposable fnOS API through `test-remote.sh bash ../../scripts/p35-client-host.sh`, redirecting output to a private log. Wait for `P35_CLIENT_READY`, then build `swift build --product SyncProbe` here and run `python3 ../../scripts/test-p35-client.py <host-log>`. The driver obtains a temporary device token privately, opens a loopback SSH tunnel, kills two synthetic macOS processes with SIGKILL and resumes the saved outbox. Host-side SQL checks exact fact/chunk/page counts and durable jobs, then the API and temporary PostgreSQL container are removed. No Worker is started. This is not iPhone E2E.

## Remaining physical-device gate

The user confirmed no physical iPhone is available for this run. Signing/install, actual read authorization, real Workout/sample/route reads, repeat incremental sync, foreground/background lifecycle, iPhone kill/network loss, server-commit response loss and API/PG restart recovery with that phone remain **NOT VERIFIED**. Real 20/45-minute records, GPS seal, latency and peak memory remain unmeasured. Delete tests must use disposable or mocked facts, never delete private HealthKit records for acceptance.

Use `docs/evidence/p35-ios-2026-10-27.json` and the Client Closure section of `docs/V2_PHASE3_RESULT.md` as the actual result. P3.5 is BLOCKED at the physical-device gate; `P4 READY: NO`.
