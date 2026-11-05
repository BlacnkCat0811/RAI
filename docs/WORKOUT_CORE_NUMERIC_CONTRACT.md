# Workout core numeric comparison — UA1-R / R1

This is a comparison-path bugfix for Workout Protocol 3. The wire schema,
canonical JSON codec, raw-fact hashes, bundle identity and server decoder do
not change. Global Swift `JSON.Equatable` remains representation-sensitive.

| Semantic category | Current fields | Identity rule |
| --- | --- | --- |
| Exact integer | activityType, event type, locationType, swimmingLocationType | Canonical integer token; no conversion of Int64 to Double |
| Timestamp / duration | startUs, endUs, durationUs (including nested events, statistics and activities) | Exact microsecond integer; a one-unit difference is a change |
| Quantity | totalDistance, totalEnergyBurned, statistics sum/average/minimum/maximum, configuration lapLength | Canonical unit plus finite canonical numeric value; `42` and `42.0` compare equal |
| Floating scalar | No standalone floating scalar in the current Workout core | Future fields remain compared in full; no guessed conversion or tolerance |
| Identifier / version | UUID/type/source strings; version/config identifiers | Strings remain strings; numeric strings, booleans and numbers are distinct |
| Metadata | Existing metadata at event/activity/event-detail levels | Existing core guard excludes metadata; the original complete event is retained unchanged, including exact Int64 metadata. Metadata enrichment is a separate existing contract |

HealthKit quantities are converted by `Normalizer.quantity` to the existing
canonical unit before comparison. This comparator performs no unit conversion,
epsilon rounding, time rounding, array sorting or field dropping beyond the
pre-existing metadata exclusion. Different canonical units remain different.
Non-finite numbers fail in the existing canonical codec.

`WorkoutCoreIdentity.canonical` serializes the core using the **existing**
canonical codec instead of comparing Swift enum cases. It never converts all
numbers to Double. The only additional comparison normalization is signed
quantity zero (`-0` / `0`), which represents the same physical value. This is
restricted to `{unit,value}` quantities; it never changes stored bytes.

Semantic equality means equal comparison-identity bytes and therefore equal
comparison-identity digests. A comparison identity is **not** a replacement raw
fact hash. `originalWorkout` returns the saved original event on equality, so
the original raw bytes/hash, metadata and upload identity survive a relaunch.
For normal finite nonnegative quantities, including `42` / `42.0`, the frozen
canonical bytes are already identical. Actual core changes still fail closed
with `workout_core_change_requires_review`.

Old state is decoded using the unchanged decoder (Bool → Int64 → Double).
No app-state deletion, anchor reset, bootstrap or schema migration is needed.
Int64.max metadata and quantities near the binary64 precision boundary have
explicit regression cases; distinct large integers must not become equal due
to floating conversion. Synthetic roundtrip properties cover 3,075 nested
Workout shapes. Native unit conversion and non-finite rejection also run in
the iOS Simulator XCTest suite.

An explicit rescan previously discarded the local receipt, then generated a
new observation time even for unchanged HealthKit content. Existing v3 servers
intentionally retain each different request as historical receipt evidence.
For a repeat, the client now reads the existing latest receipt and tries its
`sourceObservation.observedAtUs` when rebuilding the **entire** request. Only
an exact match to that receipt's full bundle SHA-256 permits reuse of the old
request. The client stages those exact bytes and performs the ordinary POST,
which rechecks deletion barriers and returns the original durable receipt.
The GET alone never commits an anchor or creates a local ACK. Any content,
coverage, ordering, metadata or session difference prevents exact reuse. A
failed lookup still permits normal immutable offline staging; cancellation
propagates. This is not a semantic-hash change or a new server deduplication
rule. It also works when an older forced rescan cleared the local receipt,
because the original server receipt remains authoritative.
