# UA1-R R2 — native metadata compatibility

This fixes the registry v1 native parser. `vo2max-facts/v1`, wire version 1,
metadataRegistryVersion 1, typed metadata, query fingerprint and semantic
domains remain unchanged. Local `nativeMetadataParserRevision=2` only prevents
an old blocked archive from being retried endlessly after a binary upgrade.

Before changing the parser, the original iPhone's retained archive was opened
with secure HealthKit decoding on the device. Its hash matched the UA1 archive.
There were 24 samples, no deletions, and no unknown metadata keys. SyncVersion
and VO2MaxTestType each had 24 NSNumber values; AlgorithmVersion had 13. All 61
were `objCType=d`, CFNumberType 6, finite, mathematically integral and inside
the safe binary64 integer magnitude. SyncIdentifier was string in 24 cases.
Only field names, carrier categories and aggregate check counts were exported.
No VO2 quantity or identifier content was inspected in public output.

The current iPhoneOS SDK HKMetadata.h declares SyncVersion as NSNumber,
VO2MaxTestType as NSNumber containing the enum, AlgorithmVersion as NSNumber
containing NSInteger, and SyncIdentifier as NSString. Numeric archive carriers
therefore do not justify treating these integer semantics as floating values.

| Key | Semantic domain (unchanged v1) | Native carriers | Wire |
| --- | --- | --- | --- |
| HKVO2MaxTestType | 1, 2, 3, 4 only | Exact integer NSNumber; finite integral safe floating NSNumber | int64 |
| HKMetadataKeySyncVersion | Full signed Int64; paired with SyncIdentifier | Same; integer carriers preserve the full domain | int64 |
| HKAlgorithmVersion | Full signed Int64 source algorithm context | Same; current SDK specifies NSInteger | int64 |
| HKMetadataKeySyncIdentifier | Nonempty string, at most 255 UTF-8 bytes | String only | string |
| Boolean registry keys | True / false | CFBoolean only | boolean |

For an integer carrier, parse `stringValue` directly to Int64. Never convert it
to Double first. For `d` carriers require finite, integral, absolute value
strictly below 2^53, and `Int64(exactly:)`. For `f` carriers the corresponding
strict boundary is 2^24. The boundary itself is rejected conservatively, since
floating 2^53+1 has already become 2^53. Exact integer carriers at and above
2^53 remain supported within Int64, as before. NSDecimalNumber, fractional,
non-finite, boolean, overflow and unknown carrier representations fail closed.
No truncating Double-to-Int conversion is used. Native numbers never coerce
SyncIdentifier, numeric strings never coerce integer fields, and enum 0/5+
remain unsupported. Signed version values retain the existing frozen v1
domain; this fix does not impose a guessed positive-only SDK rule.

Archive recovery verifies the retained bytes against the old hash and decodes
every object. It normalizes every original sample and deletion, then atomically
stages a new immutable pending request bound to `sourceBlockedHash` and the old
candidate anchor. The original archive stays referenced until durable ACK.
Lost ACK, offline and process death preserve the original pending identity.
An ACK atomically commits the anchor, clears the pending/blocked roots and
authorizes GC. Failed state replacement cannot advance an anchor or discard
the archive. Unknown metadata stays blocked; no field or object is filtered
out to make normalization succeed.

Old archives did not record query-fullness. They conservatively stage with
hasMore=true, then continue from the ACKed candidate anchor and require a fresh
query to confirm exhaustion. New archives retain the actual fullness flag.
No state deletion, anchor reset, synthetic page or source-classification guess
is part of this recovery.

`contracts/fixtures/vo2max-native-metadata-v1.json` contains six synthetic native
carrier / canonical typed metadata vectors shared by Swift and Go, including
the observed shape, all four test types, signed values, exact Int64 endpoints,
safe floating boundaries and boolean/string identity. Separate regression
matrices reject unsafe/ambiguous floating values and unsupported future shapes.
