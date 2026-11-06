# Strict JSON decode — what was optimised and what was proven to stay strict

Files: `apps/server/internal/syncing/bundle_decode.go` (decoder), `apps/server/integration/workout_strict_json_test.go`
(rejection matrix).

## 1. How the wire decoder works

`DecodeWorkoutBundleJSON(raw, *WorkoutBundle)` is deliberately two passes over the same bytes:

1. **Schema walk** (`bundleScanner.walk`) with `json.Decoder` + `UseNumber()`. It consumes the
   document token by token, rejects `null` for non-pointer targets, duplicate keys, unknown keys,
   wrong-case keys, wrong scalar kinds, integer overflow, non-finite numbers, documents deeper
   than 64 levels, objects with more than 256 fields, and every per-array count and point limit
   (`samples` ≤ 50,000, `series` ≤ 50,000, `routes` ≤ 16, route points ≤ 1,000,000, series points
   ≤ 1,000,000, `completeSampleTypes` ≤ len(Units), …). It then requires `io.EOF`, so trailing
   data is rejected.
2. **Binding pass**: a second `json.Decoder` with `DisallowUnknownFields()` + `UseNumber()`
   decodes into the typed struct.

Both passes are needed because pass 2 alone cannot distinguish a duplicate key from a repeated
value, cannot reject `null` inside `map[string]any`, and cannot enforce the count limits before
allocating. Semantic rules (unit membership, time ranges, id validity, latitude/longitude, NaN)
are **not** here: they run exactly once, in `Store.ImportWorkoutBundle` via
`syncing.ValidateWorkoutBundle`. The HTTP handler only calls `DecodeWorkoutBundleJSON`, so the
semantic pass has a single owner and runs a single time per import.

## 2. What was changed

The cost of the schema walk was dominated by reflection work per key, not by tokenisation:

| before | after |
|---|---|
| every object allocated a fresh `map[string]bool` for duplicate detection | one map per nesting **depth**, cleared and reused (`bundleScanner.keySet`) |
| every key did a linear scan over `t.NumField()` running `strings.Split(field.Tag.Get("json"), ",")[0]` — one allocation and one tag parse per key, per object | one memoised `map[string]reflect.Type` per struct type (`bundleShapeOf`, `sync.Map` with `LoadOrStore`), built once per process |
| `bundleRequired(t)` rebuilt the required-key slice for every object instance | the required list is computed once per struct type and stored in the same `bundleShape` |
| array element paths built with `fmt.Sprintf("%s[%d]", path, i)` | `path + "[" + strconv.Itoa(i) + "]"` |

For a 50,000-sample bundle the old code allocated 50,000 duplicate-detection maps and did
~11 field lookups × 50,000 objects of tag splitting; now it allocates one map per depth (≤ 8) and
does a single map lookup per key.

The binding pass, `DisallowUnknownFields`, `UseNumber`, the depth limit, the object-field limit,
the array limits and the trailing-data check are untouched — the strictness contract is exactly
the one the previous code enforced.

## 3. Measured decode cost (normal build, real HTTP, isolated PG18)

From the handler's own `workout_bundle_transport` line, `json_decode_ms`, exclusive window:

| profile | JSON bytes | decode P50 | decode P95 | throughput |
|---|---|---|---|---|
| S60 | 4,134,363 | 103.80 | 105.66 | ≈ 40 MB/s |
| S60H | 10,118,893 | 263.10 | 266.34 | ≈ 38 MB/s |
| S240 | 16,537,010 | 409.03 | 413.32 | ≈ 40 MB/s |

Decode is now 3.7 % of the S60 acknowledgement (103.8 ms of 2,796.7 ms) and 3.4 % of S240
(409.0 of 12,035.0 ms). It is no longer a meaningful share of the budget.

**Honest note on before/after.** A direct before/after of *this* optimisation on the normal build
was **not captured**: the first R0 baseline binary predates both the transport section of the
report and the optimisation, so its log contains no `json_decode_ms` at all, and the round-1
number (P50 1,649.6 ms) was measured on the `-race` build where the same decode costs 103.8 ms —
the race build inflates it ~14×, so that figure is not a valid comparison. What *is* established
is the current absolute cost (§3) and that the strictness contract is intact (§4). A clean
before/after would require keeping the old decoder behind a build tag, which was not done.

## 4. Strictness proven preserved (R2-F27)

`TestStrictWorkoutBundleJSONRejectionMatrix` drives the real handler and asserts status 422, code
`unsupported_payload`, and the exact `field` + `reason` for each rule:

| rule | case | expected field / reason |
|---|---|---|
| missing required | no `protocolVersion` | `protocolVersion` / `required` |
| missing required (nested) | sample without `source` | `samples[0].source` / `required` |
| null not allowed | `"datasetID": null` | `datasetID` / `null_not_allowed` |
| duplicate field | `datasetID` twice | `datasetID` / `duplicate_field` |
| unknown field | `"bogus": 1` | `bogus` / `unknown_field` |
| wrong case | `"ProtocolVersion"` | `ProtocolVersion` / `unknown_field` |
| wrong scalar type | `"protocolVersion": "3"` | `protocolVersion` / `invalid_type` |
| integer overflow | `startUs: 9223372036854775808` | `samples[0].startUs` / `invalid_type` |
| object where scalar expected | `"workoutID": {}` | `workoutID` / `invalid_type` |
| depth limit | 70-deep metadata | `*` / `nesting_limit` |
| object field limit | 300-key metadata | `*` / `object_field_limit` |
| array count limit | 50,001 samples | `samples` / `array_count_limit` |
| enum count limit | `completeSampleTypes` = len(Units)+1 | `completeSampleTypes` / `array_count_limit` |
| route deletion limit | `routeDeletions` = MaxBundleRoutes+1 | `routeDeletions` / `array_count_limit` |
| truncated document | half a JSON object | `*` / `invalid_json_or_schema` |
| trailing document | `{} {}` | `$` / `invalid_json_or_schema` |
| empty document | `""` | `$` / `invalid_json_or_schema` |
| non-finite number | `1e400` | `*` / `invalid_json_or_schema` |

`TestStrictWorkoutBundleSemanticRules` then proves the semantic pass still rejects, once the wire
layer accepts: `unsupported_unit` (wrong unit for the sample type), `unsupported_protocol`
(protocolVersion 2), `invalid_uuid` (sample uuid and dataset id), `invalid_time_range`
(end before start). Unit and source rules therefore still live in the Store and are still
enforced exactly once.

Result (real isolated PostgreSQL, `scripts/test-postgres.sh go test -tags=integration -run '^TestStrict' ./integration`):

```
--- PASS: TestStrictWorkoutBundleJSONRejectionMatrix (0.55s)
--- PASS: TestStrictWorkoutBundleSemanticRules (0.00s)
ok  rai/server/integration  0.359s
```

Also in `r5-gates.log`. The decoder was **not** replaced by `json.Unmarshal`: there is no lenient
path, and `DisallowUnknownFields` + the explicit schema walk remain the only way in.
