# WorkoutSyncCompletenessManifest / 1 — UA1-R R3

This control plane leaves Workout Protocol 3 facts, identity hashes, deletion
barriers and receipt formats unchanged. It certifies discovery of **visible
running Workouts and all supported associated streams** for one dataset epoch,
collector epoch, fixed lower-bound predicate and capture cutoff. It does not
certify read permission, future HealthKit arrivals, non-running/whole-body load,
or physiological calculability. The client uses a fixed 200-day initial lower
bound; M6 still independently requires its frozen 180-day tail and freshness.

Stream set 1 supports Workout discovery, the ten existing quantity types,
Workout Route and quantity-series capture linked to each anchored quantity
parent. Native running cadence and elevation samples are explicitly unsupported.
An empty supported query is complete-empty. Child streams are not applicable
only when the running-Workout inventory is empty. Route absent/present/pending
and completed-empty quantity series are explicit evidence, not inferred from
nonempty data. Temporarily unavailable/pending required streams cannot finalize.

The fixed query predicate is running Workouts overlapping the initial lower
bound, without a moving upper predicate. Child anchored queries use exact
Workout membership. The fingerprint includes contract, stream-set version,
predicate semantics, fixed lower bound and exact stream key. Opaque HealthKit
anchors never leave the phone; their SHA-256 identities bind the page chain.

`knownCutoffUs` is the capture-cycle start, not the latest health event time.
Every query starts at/after it and exhausts its current anchored results. The
claim is visibility observed through that capture cutoff; a later HealthKit
arrival (including a backdated fact) belongs to a later incremental generation.
Quantity-series queries have no independent native anchor: their fresh capture
digest/count is bound to the corresponding anchored quantity stream and exact
durable parent bundle. Native anchors are never invented for this API.

Each immutable manifest carries dataset/collector/session identity, generation,
previous manifest SHA, streamSetVersion, supported/unsupported/not-applicable
streams, fixed from/cutoff/created times, the running Workout inventory, durable
bundle receipt references, per-stream fingerprints, and all staged pages. A
page carries a stable identity, continuous sequence, previous page hash,
committed/candidate anchor hashes, query times, additions/deletions and hasMore.
The last page for every required stream must be exhausted. Final request SHA
is the manifest hash, returned in the immutable receipt; it is not a circular
self-hash field inside the request.

Capture pages and uncommitted candidate cursors are durable locally before
continuing a query. These cursors may drain a staged query; they never replace
committed anchors. Facts/deletions are delivered through the existing v3 path.
After all fact ACKs and query/series completion, exact manifest bytes are staged.
Only its validated durable server receipt atomically commits the complete set
of anchors, heads, inventory and next generation. Lost ACK/offline/app kill
retries the same manifest identity. A new collector starts its own chain; old
session evidence is never inherited. A changed stream set requires a new
reviewed contract; v1 rejects unknown versions.

The server locks the dataset, fences epoch/collector, validates strict versioned
shape, expected stream set, predicate fingerprints, page/anchor/sequence chains,
timestamps, inventory additions/deletions, receipt membership and durable facts.
Every visible inventory Workout must have a current-collector immutable v3
receipt, completed supported coverage and verified series capture digests.
Deletes dominate old upserts; absence never deletes raw facts. Existing stored
running Workouts in the bounded interval cannot silently disappear from the
inventory. A newer relevant fact revision invalidates prior completeness until
a new verified manifest represents it. A client `complete=true` flag is neither
part of the contract nor an accepted substitute for this lineage.

One additive immutable receipt table stores the request, derived stream heads,
inventory, fact revision, cutoff and final receipt in the same dataset-ordered
transaction as its commit log and Readiness invalidation/job. There is no bare
mutable dataset completeness boolean. Worker reads only verified current-
collector evidence; M6 formulas, load knownness and legacy product consumers do
not change. With all declared streams complete and no known running Workouts,
recorded running load may be zero under this bounded visibility semantics.

Server verification establishes transport consistency for the trusted collector;
HealthKit does not provide remote cryptographic attestation of query execution.
Real-device execution and receipt inspection remain separate required evidence.
No syntheticCoverage, fixture override, manual DB flag or invented HR reference
may stand in for that acceptance.

The v1 request budget is 16 MiB, 256 inventory Workouts, 8,192 captured pages and
250,000 addition/deletion references. Exceeding a budget retains the staged
cycle and committed anchors and blocks finalization. It does not shorten the
predicate, drop fields or claim partial discovery as complete.
