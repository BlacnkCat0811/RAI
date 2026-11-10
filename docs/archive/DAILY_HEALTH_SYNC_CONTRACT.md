# Daily Facts Sync v1 — M5A

Status: implemented; synthetic/isolated database evidence in [M5A_RESULT.md](M5A_RESULT.md). Real iPhone/HealthKit acceptance is **NOT RUN**. This contract does not change [Workout Protocol 3](../contracts/sync-v3.md).

## Scope and facts

| HealthKit identifier | Payload | Meaning |
| --- | --- | --- |
| `HKQuantityTypeIdentifierHeartRateVariabilitySDNN` | quantity, `ms` | Source SDNN; never RMSSD, raw RR or PPG |
| `HKQuantityTypeIdentifierRestingHeartRate` | quantity, `count/min` | Source RHR estimate; revisions use the actual delete/new UUID lifecycle |
| `HKCategoryTypeIdentifierSleepAnalysis` | category integer, start/end interval | Raw intervals; overlap and multiple sources remain separate |

SDK checked: installed Xcode iPhoneOS 26.5 HealthKit headers (`HKCategoryValues.h`, `HKTypeIdentifiers.h`, `HKMetadata.h`). Sleep values: 0=inBed, 1=asleepUnspecified (deprecated asleep alias), 2=awake, 3=asleepCore, 4=asleepDeep, 5=asleepREM. Unknown future values fail with `invalid_category`; the page and its old committed anchor remain blocked for a supported client/contract upgrade. They are never mapped to a known stage. Multi-point quantity samples likewise require a future version, rather than flattening a series.

Apple VO₂max and HeartRateRecoveryOneMinute are **deferred to M5A.1**. SDK identifies HR Recovery as count/min discrete arithmetic, but HKQuantity compatibility/device semantics have not been accepted. No VO₂max transport, physiological aggregation, scores, load or model handlers are enabled by M5A.

Identity is `(datasetID, sampleType, HealthKit UUID)`. Same UUID/same canonical content is unchanged; a different UUID with the same time/value is a distinct fact. Same UUID/different value, metadata or provenance returns `sample_identity_conflict`. Never collapse by local date, minimum heart rate, average or source preference.

## Source and metadata

Each fact carries the existing `HealthKitSource`: required bundleID, optional name, source revision version, productType, OS version and device manufacturer/model/hardware/firmware/software descriptors. Absent SDK context stays absent. Collector/ingest device is separately bound by existing database triggers. No serial, UDI or local device identifier is collected.

Raw ingestion accepts Apple, third-party, manual and unknown origins. `HKWasUserEntered` is factual context; a type name or bundle prefix alone does not prove Apple Watch generation or model eligibility. M5B owns any eligibility classification. Source is immutable and retained in `health_samples.metadata["RAI:dailySource"]`, its content hash and sample change event. This server-reserved field cannot be supplied through wire metadata.

Allowed metadata, with exact SDK key strings:

- `HKTimeZone`: nonempty IANA-compatible timezone name, at most 128 UTF-8 bytes; server validates its timezone database.
- `HKWasUserEntered`: boolean.
- `HKAlgorithmVersion`: integer (SDK declares NSInteger), including HRV algorithm context.
- `HKMetadataKeySyncIdentifier`: nonempty string, at most 255 bytes; paired with integer `HKMetadataKeySyncVersion`.
- `HKMetadataKeyHeartRateMotionContext`, `HKMetadataKeyHeartRateSensorLocation`, `HKMetadataKeyUserMotionContext`, `HKMetadataKeyDevicePlacementSide`: raw integer context.

Unknown metadata or incompatible value types stop that type's page without advancing its anchor. No unrecognized key is silently stripped. Unsupported SDK samples remain in HealthKit, and the local error records the need for an upgrade; they are not exported into logs or an uncontrolled debug dump. This conservative boundary may block a real source with additional metadata until reviewed.

## Discovery, bootstrap and anchors

Three independent flows live in the existing file-backed `OutboxStore`; no second local database. Each stores stream ID, sequence chain, immutable bootstrap lower bound, committed secure HKQueryAnchor archive, pending body identity/hash/file and candidate anchor, last discovery/upload state, error and last receipt. New optional LocalState fields preserve v1 state decoding.

Initial range: **90 days** before flow creation. The model design needs the previous 28 days for baseline; 90 provides multiple baseline windows and correction context. The transport supports at most **180 days at first discovery**; no all-history initial scan. The predicate includes intervals overlapping the lower bound and has no moving upper/lower day bucket. That same fixed predicate and fingerprint are used for every incremental query. A completed page with `hasMore=false` durably marks bootstrap complete. A full page conservatively implies more work and may be followed by an empty page.

The fingerprint is `sha256:` plus SHA-256 of UTF-8 `daily-v1|<sampleType>|<bootstrapStartUs>`. A HealthKit type never shares another type's anchor. Corrupt anchor decoding fails closed; there is no automatic nil-anchor fallback, stream rotation or silent rediscovery. Lost local state requires explicit session/identity recovery, outside the ordinary hot path.

Commit order:

1. Query from the **committed** anchor, normalize the complete bounded page.
2. Atomically write an immutable body file, then publish its pending reference/candidate anchor in the existing atomic state file. The committed anchor stays unchanged.
3. POST the same bytes on every attempt.
4. Server commits raw facts, barriers, logs, dirty intervals, coverage and immutable receipt in one PostgreSQL transaction with synchronous commit.
5. Validate ACK version, dataset/epoch/collector, stream, sequence, batch, exact body SHA-256, committed status and positive commit revision.
6. Atomically persist ACK, candidate→committed anchor, sequence/previous-batch chain and pending removal. Only then may its body be reclaimed and its cached transfer result discarded.

A kill before upload leaves pending work. A kill in flight or after server commit replays the identical batch and gets its original receipt. Failure during atomic anchor save leaves the old recoverable state, or the complete new state. These are process-restart guarantees; physical power-loss/filesystem durability is not proven by Foundation `.atomic` alone.

## Scheduling and offline behavior

Read-only authorization requests include the three types separately from WorkoutKit scheduling authorization. Neither query results nor `authorizationStatus(for:)` prove read authorization. An empty result is only an empty visible observation.

App launch, foreground entry, the native “同步日常健康” action, observer wake hints, and a cancellable **foreground-only** 15-minute opportunity run daily work independently of Workout selection. Foreground pages are capped at four per type per opportunity; pending/more work schedules another bounded opportunity. One failed type does not block the others. No fixed background cadence is promised. Caught-up types reuse their discovery observation for up to 15 minutes within the same wake generation, so a network retry does not create fresh empty HRV/RHR batches ahead of Sleep. Pending work and the oldest acknowledged type receive shared upload capacity first. Launch/foreground/manual/observer triggers advance a durable wake generation; each query stages the generation captured before its read, and an older ACK cannot consume a newer wake hint. These rules are tested against actual shared HTTP 429 responses (all three types complete with exactly three facts/receipts).

Observers contain no sample facts. The callback first attempts to persist `dailyWakePending`, schedules local bounded discovery, and returns completion without waiting for query/network delivery. Suspension after the hint preserves the work marker; foreground rechecks all three anchored streams. One pending page per type prevents unlimited offline queue growth. HealthKit remains the source for changes after the committed anchor. Network failure keeps pending bytes and the old anchor; bounded backoff/Retry-After governs replay, including failure at the handshake before an upload starts. Existing recovery persists daily backoff alongside Workout recovery without changing either channel’s wire semantics. Background delivery scheduling, deletion-retention limits, permission changes and extended offline periods still require real-device validation; query emptiness never authorizes a server-side delete.

## Wire and bounds

`POST /v2/sync/daily-facts`, Content-Type `application/json`, uncompressed, authenticated using the existing dataset/device bearer. Handshake adds `dailyFactsVersion=1`, `dailyFactTypes`, `dailyMaxChanges=128`, `dailyMaxBytes=524288`. Workout protocolVersion remains 3. See [OpenAPI](../contracts/openapi-v2.yaml).

Required batch fields:

```text
version=1
 datasetID / datasetEpoch / collectorEpoch
 streamID / batchID / clientSeq / previousBatchID (explicit null for seq=1)
 sampleType / queryFingerprint / bootstrapStartUs
 observedAtUs / hasMore
 facts[] / deletedUUIDs[] (explicit arrays, including empty)
```

Each fact requires UUID, startUs/endUs, full source and exactly one of quantity or category; metadata is optional. The batch's sampleType determines each fact's type. Absolute timestamps are signed UTC epoch microseconds in the supported year-1..9999 range. Raw start/end and trustworthy sample timezone context survive travel, DST and cross-midnight intervals. No current device timezone is invented for old samples. Future aggregation must use the dataset timezone policy (handshake `timezone`), not implicit UTC midnight.

Bounds: additions+deletions **≤128 combined**, each JSON body **≤512 KiB**, first lookback ≤180 days, server fact transaction context ≤5 seconds (existing database statement/lock timeouts also apply). Limits were checked with synthetic 32/64/128 batch comparisons; 128 reduced requests and bootstrap time while keeping short bounded transactions. If a HealthKit page exceeds the byte budget, query again from the same committed anchor at half the page limit until it fits; no truncation. An individual unsupported/oversized fact remains blocked, never silently omitted.

## Server transaction, deletes and invalidation

Uses existing `sync_streams`, `sync_batches`, `health_samples`, `entity_tombstones`, `entity_changes`, `change_events` and `dirty_scopes`. Migration 20 adds only `daily_health_coverage`. It is expand-only; migration 1–19 files are unchanged. Runtime role grants/readiness and generated sqlc models include the new table.

Lock order is dataset → stream → existing operational upload slot. Dataset epoch and current collector lease are checked inside the transaction. The dataset row, not a sequence, produces commit revision order. Stream creation and initial coverage/receipt commit atomically. Exact old retries are read-only receipt retrieval after current fencing checks; they never replay fact writes. Sequence gaps, chain mismatches or unrecognized old batches fail closed.

Deletes win even within the same batch. Unknown deletes create barriers. Old exact batch retry still returns its old immutable ACK, without resurrecting data. A later newly identified batch containing a tombstoned UUID commits a `suppressed` count and `deleteStatus=deleted_precedence`, allowing the client to acknowledge discovery without revival. RHR delete+new is atomic; overlapping/multiple-source sleep remains distinct.

Each changed, time-known sample gets `daily:<type>:<uuid>` in existing dirty_scopes, carrying absolute affected start/end and an incremented generation. Deletion invalidates the previous interval. Unknown deletes retain their barrier/log without inventing time ranges. M5B can expand these bounded intervals using explicit model dependencies and dataset timezone. No nonexistent handler/job is registered: ACK says `analysisStatus=awaiting_model_implementation`; clean generation stays behind dirty generation. This is a dependency hook, not completed analysis.

## ACK and coverage

Receipt includes version, batch/dataset/epoch/collector/stream/sequence identities, SHA-256 of **exact request bytes**, positive commitRevision, counts (inserted/unchanged/deleted/tombstoned/suppressed), deleteStatus and analysisStatus. Reusing batch identity with different bytes returns conflict. ACK means durable facts/coverage commit, without waiting for future analysis.

`GET /v2/sync/daily-facts/coverage` reads all three types in one repeatable-read database snapshot. Fields:

| Field | Exact meaning |
| --- | --- |
| state=`sync_not_completed` | No acknowledged daily discovery for current collector, or latest acknowledged page had more work |
| state=`no_sample_observed` | Caught-up acknowledged query, no live raw fact/barrier observed by server |
| state=`sample_deleted` | No live facts and at least one received deletion barrier (does not prove the server saw the original sample) |
| state=`sample_exists` | Server has at least one live raw fact |
| queryCompleted | Server received an ACK-eligible query page; not permission proof or all-history completeness |
| anchorPresent | That committed observation has a client candidate anchor; server does not store/decode Apple's opaque anchor |
| bootstrapComplete | At least one caught-up page has committed for this stream |
| hasMore | Client's conservative bounded-page continuation observation |
| sampleCount/deletedCount | Latest acknowledged page's observed counts, before retry/delete suppression; never expected physiological counts |
| lastDiscoveryAtUs | Client observation timestamp of last acknowledged query page |
| lastSyncAt | Server commit observation time |
| lastFactAtUs | Maximum source end timestamp ever observed in this stream, including a later deleted/suppressed sample; not latest live eligibility |
| sourceContext | Distinct bundle identifiers in the most recent nonempty additions page; full revision/device remains per fact, not an exhaustive source inventory |
| hasLiveFacts/hasDeletedFacts | Indexed existence checks of current raw rows / received barriers |

The server cannot see an unuploaded pending page or infer why HealthKit produced nothing. It reports received evidence only, never `permission_granted`, `permission_denied`, coverage percentage or physiological `fresh/stale`. A completed historical discovery is not a promise of current synchronization after an offline period.

## Errors and privacy

| Status | Code / handling |
| --- | --- |
| 422 | invalid_payload, unsupported_version, unsupported_type, unsupported_unit, invalid_category, source_invalid, unsupported_metadata; retain page/anchor for review or upgrade |
| 413 | batch_too_large; smaller anchored query, no truncation |
| 409 | generation_conflict, collector_fenced, dataset_retired, stream_identity_conflict, stale_batch, batch_identity_conflict, sample_identity_conflict, upload_lease_lost; preserve exact pending bytes, never mutate identities automatically |
| 429 | existing request/upload admission codes plus Retry-After |
| 500 | internal_error; no database diagnostic/health payload returned; retry original pending body |

`deleted_precedence` is a successful suppressed-fact outcome, not a failed transaction. Device/dataset auth failures retain existing 401/403 semantics. Ordinary logs contain operation templates, status/error class, duration and request correlation only. No health values, full sleep intervals, sample UUID inventories or credentials are logged. Synthetic fixtures are the only automated data; no production migration, deployment or personal HealthKit export is part of M5A.

Apple references: [anchored query](https://developer.apple.com/documentation/healthkit/hkanchoredobjectquery), [HealthKit authorization](https://developer.apple.com/documentation/healthkit/authorizing-access-to-health-data), [observer query](https://developer.apple.com/documentation/healthkit/hkobserverquery). API documentation plus installed SDK evidence govern the wire; an empty visible query does not establish read permission.
