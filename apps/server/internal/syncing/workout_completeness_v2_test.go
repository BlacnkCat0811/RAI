package syncing

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func readV2Fixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../../../contracts/fixtures/workout-completeness-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestWorkoutCompletenessV2SharedGolden(t *testing.T) {
	raw := readV2Fixture(t)
	m, err := DecodeWorkoutCompletenessV2(raw)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../../../contracts/fixtures/workout-completeness-v2.sha256")
	if err != nil {
		t.Fatal(err)
	}
	if BundleDigest(bytes.TrimSpace(raw)) != strings.TrimSpace(string(want)) {
		t.Fatal("manifest golden changed")
	}
	pageHash, err := os.ReadFile("../../../../contracts/fixtures/workout-completeness-page-v2.sha256")
	if err != nil {
		t.Fatal(err)
	}
	if CompletenessPageDigest(m.Streams[0].Pages[0]) != strings.TrimSpace(string(pageHash)) {
		t.Fatal("page canonical golden changed")
	}
	if m.Inventory.VisibleCount != 3 || m.Inventory.EligibleCount != 2 || m.Inventory.ExcludedCount != 1 || m.Inventory.UnclassifiedCount != 0 {
		t.Fatalf("unexpected inventory %+v", m.Inventory)
	}
}

func TestWorkoutCompletenessV2RejectsUnexplainedInventory(t *testing.T) {
	raw := readV2Fixture(t)
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	replace := func(field string, value any) []byte {
		var v any
		if err := json.Unmarshal(root[field], &v); err != nil {
			t.Fatal(err)
		}
		v = value
		root[field], _ = json.Marshal(v)
		b, _ := json.Marshal(root)
		return b
	}
	inv := func(mut func(map[string]any)) []byte {
		var v map[string]any
		if err := json.Unmarshal(root["inventory"], &v); err != nil {
			t.Fatal(err)
		}
		mut(v)
		root["inventory"], _ = json.Marshal(v)
		b, _ := json.Marshal(root)
		return b
	}
	cases := map[string][]byte{
		// An unclassified Workout must block, not be dropped from the counts.
		"unclassified blocks": inv(func(v map[string]any) { v["unclassifiedCount"] = 1 }),
		// Counts that do not add up.
		"invariant violation": inv(func(v map[string]any) { v["visibleCount"] = 4 }),
		// A count-only inventory cannot stand in for membership evidence.
		"visible digest": inv(func(v map[string]any) { v["visibleSHA256"] = strings.Repeat("a", 64) }),
		"eligible digest": inv(func(v map[string]any) {
			v["eligibleSHA256"] = strings.Repeat("b", 64)
		}),
		"excluded digest": inv(func(v map[string]any) {
			v["excludedSHA256"] = strings.Repeat("c", 64)
		}),
		// The client cannot drop the excluded Workout from the manifest.
		"exclusions dropped": replace("exclusions", []any{}),
		// Policy identity must be bound.
		"policy version":         replace("sourcePolicyVersion", "rai.workout.source_policy.apple_watch_only/v2"),
		"policy digest":          replace("sourcePolicySHA256", strings.Repeat("d", 64)),
		"classification version": replace("classificationVersion", 2),
		"contract":               replace("contract", "workout-sync-completeness/v1"),
		"protocol version":       replace("protocolVersion", 1),
	}
	for name, bad := range cases {
		if _, err := DecodeWorkoutCompletenessV2(bad); err == nil {
			t.Fatalf("%s: unsupported evidence accepted", name)
		}
	}
}

func TestWorkoutCompletenessV2RejectsUntrustedExclusion(t *testing.T) {
	raw := readV2Fixture(t)
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	var exclusions []map[string]json.RawMessage
	if err := json.Unmarshal(root["exclusions"], &exclusions); err != nil {
		t.Fatal(err)
	}
	mutate := func(mut func(map[string]json.RawMessage)) []byte {
		copied := map[string]json.RawMessage{}
		for k, v := range exclusions[0] {
			copied[k] = v
		}
		mut(copied)
		items := []map[string]json.RawMessage{copied}
		root["exclusions"], _ = json.Marshal(items)
		b, _ := json.Marshal(root)
		return b
	}
	// A client that simply claims excluded=true, or that claims exclusion for a
	// source the frozen policy does not exclude, must be rejected.
	cases := map[string][]byte{
		"classification not excluded by policy": mutate(func(v map[string]json.RawMessage) {
			v["classification"], _ = json.Marshal("apple_watch")
		}),
		"outcome mismatch": mutate(func(v map[string]json.RawMessage) {
			v["outcome"], _ = json.Marshal("eligible")
		}),
		"reason mismatch": mutate(func(v map[string]json.RawMessage) {
			v["reason"], _ = json.Marshal("unclassified_source")
		}),
		"evidence digest": mutate(func(v map[string]json.RawMessage) {
			v["evidenceSHA256"], _ = json.Marshal(strings.Repeat("e", 64))
		}),
		"evidence does not classify as claimed": mutate(func(v map[string]json.RawMessage) {
			// Apple Watch evidence claimed as third_party.
			v["sourceEvidence"], _ = json.Marshal(map[string]any{"bundleID": "com.apple.health", "name": "", "version": "", "productType": "Watch7,3", "operatingSystemVersion": "", "device": map[string]any{"manufacturer": "Apple", "model": "Watch7,3", "hardwareVersion": "", "firmwareVersion": "", "softwareVersion": ""}})
		}),
		"policy version": mutate(func(v map[string]json.RawMessage) {
			v["sourcePolicyVersion"], _ = json.Marshal("rai.workout.source_policy.apple_watch_only/v2")
		}),
	}
	for name, bad := range cases {
		if _, err := DecodeWorkoutCompletenessV2(bad); err == nil {
			t.Fatalf("%s: untrusted exclusion accepted", name)
		}
	}
}

func TestWorkoutCompletenessV2InventoryIsPermutationStable(t *testing.T) {
	raw := readV2Fixture(t)
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	first, err := DecodeWorkoutCompletenessV2(raw)
	if err != nil {
		t.Fatal(err)
	}
	var workouts, exclusions []json.RawMessage
	if err := json.Unmarshal(root["workouts"], &workouts); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(root["exclusions"], &exclusions); err != nil {
		t.Fatal(err)
	}
	workouts[0], workouts[1] = workouts[1], workouts[0]
	root["workouts"], _ = json.Marshal(workouts)
	root["exclusions"], _ = json.Marshal(exclusions)
	b, _ := json.Marshal(root)
	second, err := DecodeWorkoutCompletenessV2(b)
	if err != nil {
		t.Fatal(err)
	}
	if first.Inventory != second.Inventory {
		t.Fatal("inventory identity changed with ordering")
	}
	if !EqualStringSet(EligibleIdentitySHA256(first), EligibleIdentitySHA256(second)) {
		t.Fatal("eligible identity changed with ordering")
	}
}

func TestWorkoutCompletenessV2IdentityHashesAreDeterministic(t *testing.T) {
	const id = "20000000-0000-4000-8000-000000000001"
	a := WorkoutIdentitySHA256(id)
	b := WorkoutIdentitySHA256(strings.ToUpper(id))
	if a != b || !ValidDigest(a) {
		t.Fatal("workout identity hash must be case insensitive and hex")
	}
	if InventorySHA256([]string{"b", "a"}) != InventorySHA256([]string{"a", "b"}) {
		t.Fatal("inventory digest must ignore ordering")
	}
	if InventorySHA256([]string{"a", "a"}) == InventorySHA256([]string{"a", "b"}) {
		t.Fatal("inventory digest must not collapse duplicates")
	}
}

// syntheticV2Manifest builds a structural v2 manifest with the requested
// eligible/excluded split. It exists to exercise inventory semantics, not to
// substitute for a real capture.
func syntheticV2Manifest(t *testing.T, eligible, excluded int) WorkoutSyncCompletenessManifestV2 {
	t.Helper()
	now := int64(1700000000000000)
	m := WorkoutSyncCompletenessManifestV2{Contract: WorkoutCompletenessV2Contract, ProtocolVersion: WorkoutCompletenessV2Version, StreamSetVersion: 1,
		SourcePolicyVersion: WorkoutSourcePolicyVersion, SourcePolicySHA256: WorkoutSourcePolicySHA256, ClassificationVersion: WorkoutSourceClassificationVer,
		DatasetID: "10000000-0000-4000-8000-000000000001", DatasetEpoch: "10000000-0000-4000-8000-000000000002",
		CollectorEpoch: "10000000-0000-4000-8000-000000000003", ManifestID: "10000000-0000-4000-8000-000000000004",
		Generation: 1, WindowFromUs: now - 200*86400000000, KnownCutoffUs: now, CreatedAtUs: now + 10,
		SupportedStreams: WorkoutCompletenessTypes(), UnsupportedStreams: append([]string{}, WorkoutCompletenessUnsupported...),
		NotApplicableStreams: []string{}, Streams: []CompletenessStream{}, Workouts: []CompletenessWorkout{}, Exclusions: []CompletenessExclusion{}}
	page := func(ids []string) CompletenessPage {
		return CompletenessPage{PageID: NewID(), Sequence: 1, CandidateAnchorSHA256: strings.Repeat("a", 64), QueryStartedAtUs: now, QueryCompletedAtUs: now + 1, Additions: ids, Deletions: []string{}}
	}
	visible, eligibleIDs, excludedIDs := []string{}, []string{}, []string{}
	for i := 0; i < eligible+excluded; i++ {
		id := NewID()
		visible = append(visible, id)
		if i < eligible {
			eligibleIDs = append(eligibleIDs, id)
			continue
		}
		excludedIDs = append(excludedIDs, id)
		evidence := SourcePolicyEvidence{BundleID: "com.strava.Strava", ProductType: "iPhone14,5"}
		classification := evidence.Classification()
		m.Exclusions = append(m.Exclusions, CompletenessExclusion{WorkoutIdentitySHA256: WorkoutIdentitySHA256(id), Classification: classification,
			Outcome: SourcePolicyOutcome(classification), Reason: SourcePolicyReason(SourcePolicyOutcome(classification)),
			ClassificationVersion: WorkoutSourceClassificationVer, SourcePolicyVersion: WorkoutSourcePolicyVersion,
			EvidenceSHA256: evidence.SHA256(), SourceEvidence: evidence})
	}
	m.Streams = append(m.Streams, CompletenessStream{Key: "workouts", SampleType: WorkoutType, QueryFingerprint: CompletenessFingerprintV2(m.WindowFromUs, "workouts"), Pages: []CompletenessPage{page(visible)}})
	for _, id := range eligibleIDs {
		w := CompletenessWorkout{WorkoutID: id, BundleSHA256: strings.Repeat("b", 64), RouteState: "absent", Series: []CompletenessSeries{}}
		for _, kind := range append(append([]string{}, WorkoutCompletenessQuantities...), RouteType) {
			workout := id
			key := CompletenessKey(kind, id)
			m.Streams = append(m.Streams, CompletenessStream{Key: key, SampleType: kind, WorkoutID: &workout, QueryFingerprint: CompletenessFingerprintV2(m.WindowFromUs, key), Pages: []CompletenessPage{page([]string{})}})
			if kind != RouteType {
				w.Series = append(w.Series, CompletenessSeries{SampleType: kind, SHA256: strings.Repeat("c", 64), PointCount: 0, QueryCompletedAtUs: now + 2})
			}
		}
		m.Workouts = append(m.Workouts, w)
	}
	if len(eligibleIDs) == 0 {
		m.NotApplicableStreams = append(append([]string{}, WorkoutCompletenessQuantities...), RouteType)
	}
	hash := func(ids []string) []string {
		out := make([]string, 0, len(ids))
		for _, id := range ids {
			out = append(out, WorkoutIdentitySHA256(id))
		}
		return out
	}
	m.Inventory = CompletenessInventory{VisibleCount: len(visible), EligibleCount: len(eligibleIDs), ExcludedCount: len(excludedIDs),
		VisibleSHA256: InventorySHA256(hash(visible)), EligibleSHA256: InventorySHA256(hash(eligibleIDs)), ExcludedSHA256: InventorySHA256(hash(excludedIDs))}
	return m
}

func v2RoundTrip(t *testing.T, m WorkoutSyncCompletenessManifestV2) (*WorkoutSyncCompletenessManifestV2, error) {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeWorkoutCompletenessV2(raw)
	return &decoded, err
}

// TestWorkoutCompletenessV2RealDeviceMatrix is the real-device shape (31
// eligible Apple Watch Workouts plus 13 excluded third-party ones) and the
// other inventory corners the contract must survive.
func TestWorkoutCompletenessV2RealDeviceMatrix(t *testing.T) {
	m, err := v2RoundTrip(t, syntheticV2Manifest(t, 31, 13))
	if err != nil {
		t.Fatal(err)
	}
	if m.Inventory.VisibleCount != 44 || m.Inventory.EligibleCount != 31 || m.Inventory.ExcludedCount != 13 || len(m.Streams) != 1+31*11 {
		t.Fatalf("matrix shape %+v streams=%d", m.Inventory, len(m.Streams))
	}
	for _, split := range [][2]int{{0, 0}, {44, 0}, {0, 44}, {31, 13}, {1, 0}, {0, 1}} {
		if _, err := v2RoundTrip(t, syntheticV2Manifest(t, split[0], split[1])); err != nil {
			t.Fatalf("split %v rejected: %v", split, err)
		}
	}
	// One unexplained Workout blocks the whole manifest.
	for name, mutate := range map[string]func(*WorkoutSyncCompletenessManifestV2){
		"unclassified": func(m *WorkoutSyncCompletenessManifestV2) { m.Inventory.UnclassifiedCount = 1 },
		"dropped_exclusion": func(m *WorkoutSyncCompletenessManifestV2) {
			m.Exclusions = m.Exclusions[:len(m.Exclusions)-1]
			m.Inventory.ExcludedCount--
			m.Inventory.VisibleCount--
		},
		"visible_count":  func(m *WorkoutSyncCompletenessManifestV2) { m.Inventory.VisibleCount++ },
		"eligible_count": func(m *WorkoutSyncCompletenessManifestV2) { m.Inventory.EligibleCount++ },
	} {
		t.Run(name, func(t *testing.T) {
			broken := syntheticV2Manifest(t, 31, 13)
			mutate(&broken)
			if _, err := v2RoundTrip(t, broken); err == nil {
				t.Fatal("unexplained inventory accepted")
			}
		})
	}
}

// TestWorkoutCompletenessV2ClassificationChangeChangesIdentity proves a
// classification flip cannot reuse the previous manifest identity: the visible
// membership digest is unchanged, but the eligible/excluded digests are not.
func TestWorkoutCompletenessV2ClassificationChangeChangesIdentity(t *testing.T) {
	hash := func(ids []string) []string {
		out := make([]string, 0, len(ids))
		for _, id := range ids {
			out = append(out, WorkoutIdentitySHA256(id))
		}
		return out
	}
	visible := []string{NewID(), NewID(), NewID()}
	eligible, excluded := visible[:2], visible[2:]
	before := []string{InventorySHA256(hash(visible)), InventorySHA256(hash(eligible)), InventorySHA256(hash(excluded))}
	// The excluded Workout is reclassified as eligible.
	eligible, excluded = visible[:3], []string{}
	after := []string{InventorySHA256(hash(visible)), InventorySHA256(hash(eligible)), InventorySHA256(hash(excluded))}
	if before[0] != after[0] {
		t.Fatal("visible membership changed without a classification change")
	}
	if before[1] == after[1] || before[2] == after[2] {
		t.Fatal("classification change reused inventory identity")
	}
	// A classification change with no evidence is rejected outright.
	broken := syntheticV2Manifest(t, 2, 1)
	broken.Exclusions = []CompletenessExclusion{}
	broken.Inventory.ExcludedCount = 0
	broken.Inventory.EligibleCount = 2
	if _, err := v2RoundTrip(t, broken); err == nil {
		t.Fatal("classification change without evidence accepted")
	}
}
