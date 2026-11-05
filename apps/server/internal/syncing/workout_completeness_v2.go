package syncing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
)

// Workout Sync Completeness v2 (UA1-R3B).
//
// v1 is frozen: it requires a durable receipt for every visible Workout, which
// can never be satisfied while the source policy excludes third-party Workouts.
// v2 keeps the same wire facts and stream set, and changes exactly one thing:
// completeness now proves that every visible running Workout is *explained* as
// either eligible (durable receipt) or excluded by the frozen source policy
// (deterministic evidence). An unresolved Workout still blocks.
const (
	WorkoutCompletenessV2Contract = "workout-sync-completeness/v2"
	WorkoutCompletenessV2Version  = 2
	MaxCompletenessExclusions     = 256
)

// CompletenessInventory is the bounded membership summary. Counts alone are
// never enough: each set is committed by a hash over hashed Workout identities.
type CompletenessInventory struct {
	VisibleCount               int    `json:"visibleCount"`
	EligibleCount              int    `json:"eligibleCount"`
	ExcludedCount              int    `json:"excludedCount"`
	UnclassifiedCount          int    `json:"unclassifiedCount"`
	PendingClassificationCount int    `json:"pendingClassificationCount"`
	VisibleSHA256              string `json:"visibleSHA256"`
	EligibleSHA256             string `json:"eligibleSHA256"`
	ExcludedSHA256             string `json:"excludedSHA256"`
}

// CompletenessExclusion is the durable evidence for one excluded Workout. It
// carries a hashed identity, the frozen policy identity and the minimal source
// descriptor the classifier reads, so the server can re-run the policy itself.
type CompletenessExclusion struct {
	WorkoutIdentitySHA256 string               `json:"workoutIdentitySHA256"`
	Classification        string               `json:"classification"`
	Outcome               string               `json:"outcome"`
	Reason                string               `json:"reason"`
	ClassificationVersion int                  `json:"classificationVersion"`
	SourcePolicyVersion   string               `json:"sourcePolicyVersion"`
	EvidenceSHA256        string               `json:"evidenceSHA256"`
	SourceEvidence        SourcePolicyEvidence `json:"sourceEvidence"`
}

type WorkoutSyncCompletenessManifestV2 struct {
	Contract               string                  `json:"contract"`
	ProtocolVersion        int                     `json:"protocolVersion"`
	StreamSetVersion       int                     `json:"streamSetVersion"`
	SourcePolicyVersion    string                  `json:"sourcePolicyVersion"`
	SourcePolicySHA256     string                  `json:"sourcePolicySHA256"`
	ClassificationVersion  int                     `json:"classificationVersion"`
	DatasetID              string                  `json:"datasetID"`
	DatasetEpoch           string                  `json:"datasetEpoch"`
	CollectorEpoch         string                  `json:"collectorEpoch"`
	ManifestID             string                  `json:"manifestID"`
	Generation             int64                   `json:"generation"`
	PreviousManifestSHA256 *string                 `json:"previousManifestSHA256"`
	WindowFromUs           int64                   `json:"windowFromUs"`
	KnownCutoffUs          int64                   `json:"knownCutoffUs"`
	CreatedAtUs            int64                   `json:"createdAtUs"`
	SupportedStreams       []string                `json:"supportedStreams"`
	UnsupportedStreams     []string                `json:"unsupportedStreams"`
	NotApplicableStreams   []string                `json:"notApplicableStreams"`
	Inventory              CompletenessInventory   `json:"inventory"`
	Streams                []CompletenessStream    `json:"streams"`
	Workouts               []CompletenessWorkout   `json:"workouts"`
	Exclusions             []CompletenessExclusion `json:"exclusions"`
}

type WorkoutCompletenessReceiptV2 struct {
	Contract            string `json:"contract"`
	ProtocolVersion     int    `json:"protocolVersion"`
	SourcePolicyVersion string `json:"sourcePolicyVersion"`
	DatasetID           string `json:"datasetID"`
	DatasetEpoch        string `json:"datasetEpoch"`
	CollectorEpoch      string `json:"collectorEpoch"`
	ManifestID          string `json:"manifestID"`
	Generation          int64  `json:"generation"`
	ManifestSHA256      string `json:"manifestSHA256"`
	IngestStatus        string `json:"ingestStatus"`
	CommitRevision      int64  `json:"commitRevision"`
	StreamCount         int    `json:"streamCount"`
	WorkoutCount        int    `json:"workoutCount"`
	VisibleCount        int    `json:"visibleCount"`
	EligibleCount       int    `json:"eligibleCount"`
	ExcludedCount       int    `json:"excludedCount"`
	UnclassifiedCount   int    `json:"unclassifiedCount"`
	VisibleSHA256       string `json:"visibleSHA256"`
	EligibleSHA256      string `json:"eligibleSHA256"`
	ExcludedSHA256      string `json:"excludedSHA256"`
}

func DecodeWorkoutCompletenessV2(raw []byte) (WorkoutSyncCompletenessManifestV2, error) {
	var m WorkoutSyncCompletenessManifestV2
	if len(raw) > MaxCompletenessBytes {
		return m, Fail(413, "batch_too_large")
	}
	if err := DecodeStrictJSON(raw, &m); err != nil {
		return m, Fail(422, "invalid_payload")
	}
	if err := vo2Required(raw, strings.Fields("contract protocolVersion datasetID datasetEpoch collectorEpoch manifestID generation previousManifestSHA256 streamSetVersion sourcePolicyVersion sourcePolicySHA256 classificationVersion windowFromUs knownCutoffUs createdAtUs supportedStreams unsupportedStreams notApplicableStreams inventory streams workouts exclusions"), "previousManifestSHA256"); err != nil {
		return m, err
	}
	var root map[string]json.RawMessage
	_ = json.Unmarshal(raw, &root)
	if err := vo2Required(root["inventory"], strings.Fields("visibleCount eligibleCount excludedCount unclassifiedCount pendingClassificationCount visibleSHA256 eligibleSHA256 excludedSHA256")); err != nil {
		return m, err
	}
	var streams []map[string]json.RawMessage
	_ = json.Unmarshal(root["streams"], &streams)
	for _, s := range streams {
		b, _ := json.Marshal(s)
		if err := vo2Required(b, strings.Fields("key sampleType workoutID queryFingerprint pages"), "workoutID"); err != nil {
			return m, err
		}
		var pages []json.RawMessage
		_ = json.Unmarshal(s["pages"], &pages)
		for _, p := range pages {
			if err := vo2Required(p, strings.Fields("pageID sequence previousPageSHA256 previousAnchorSHA256 candidateAnchorSHA256 queryStartedAtUs queryCompletedAtUs additions deletions hasMore"), "previousPageSHA256", "previousAnchorSHA256"); err != nil {
				return m, err
			}
		}
	}
	var workouts []map[string]json.RawMessage
	_ = json.Unmarshal(root["workouts"], &workouts)
	for _, w := range workouts {
		b, _ := json.Marshal(w)
		if err := vo2Required(b, strings.Fields("workoutID bundleSHA256 routeState series")); err != nil {
			return m, err
		}
		var series []json.RawMessage
		_ = json.Unmarshal(w["series"], &series)
		for _, s := range series {
			if err := vo2Required(s, strings.Fields("sampleType sha256 pointCount queryCompletedAtUs")); err != nil {
				return m, err
			}
		}
	}
	var exclusions []map[string]json.RawMessage
	_ = json.Unmarshal(root["exclusions"], &exclusions)
	for _, x := range exclusions {
		b, _ := json.Marshal(x)
		if err := vo2Required(b, strings.Fields("workoutIdentitySHA256 classification outcome reason classificationVersion sourcePolicyVersion evidenceSHA256 sourceEvidence")); err != nil {
			return m, err
		}
		if err := vo2Required(x["sourceEvidence"], strings.Fields("bundleID name version productType operatingSystemVersion device")); err != nil {
			return m, err
		}
		var evidence map[string]json.RawMessage
		_ = json.Unmarshal(x["sourceEvidence"], &evidence)
		if err := vo2Required(evidence["device"], strings.Fields("manufacturer model hardwareVersion firmwareVersion softwareVersion")); err != nil {
			return m, err
		}
	}
	if err := m.Validate(); err != nil {
		return m, err
	}
	return m, nil
}

// CompletenessFingerprintV2 binds a stream query to the v2 contract identity.
// v1 and v2 fingerprints can never collide, so a v1 anchor can never be reused
// under the changed inventory semantics.
func CompletenessFingerprintV2(from int64, key string) string {
	h := sha256.Sum256([]byte(WorkoutCompletenessV2Contract + "|stream-set:1|running-overlap-v1|" + strconv.FormatInt(from, 10) + "|" + key))
	return "sha256:" + hex.EncodeToString(h[:])
}

// EligibleIdentitySHA256 is the hashed identity of an eligible Workout as the
// manifest commits it.
func EligibleIdentitySHA256(m WorkoutSyncCompletenessManifestV2) []string {
	out := make([]string, 0, len(m.Workouts))
	for _, w := range m.Workouts {
		out = append(out, WorkoutIdentitySHA256(w.WorkoutID))
	}
	return out
}

func (m WorkoutSyncCompletenessManifestV2) ExcludedIdentitySHA256() []string {
	out := make([]string, 0, len(m.Exclusions))
	for _, x := range m.Exclusions {
		out = append(out, x.WorkoutIdentitySHA256)
	}
	return out
}

// VisibleIdentitySHA256 is the union of the two explained sets. The inventory
// invariant is checked against it, so a Workout cannot be dropped from both.
func (m WorkoutSyncCompletenessManifestV2) VisibleIdentitySHA256() []string {
	return append(EligibleIdentitySHA256(m), m.ExcludedIdentitySHA256()...)
}

func (m WorkoutSyncCompletenessManifestV2) Validate() error {
	bad := func() error { return Fail(422, "invalid_completeness") }
	if m.Contract != WorkoutCompletenessV2Contract || m.ProtocolVersion != WorkoutCompletenessV2Version || m.StreamSetVersion != 1 {
		return Fail(422, "unsupported_version")
	}
	if m.SourcePolicyVersion != WorkoutSourcePolicyVersion || m.SourcePolicySHA256 != WorkoutSourcePolicySHA256 || m.ClassificationVersion != WorkoutSourceClassificationVer {
		return Fail(422, "source_policy_mismatch")
	}
	for _, id := range []string{m.DatasetID, m.DatasetEpoch, m.CollectorEpoch, m.ManifestID} {
		if !ValidID(id) {
			return bad()
		}
	}
	if m.Generation < 1 || m.Generation > 1 && m.PreviousManifestSHA256 == nil || m.PreviousManifestSHA256 != nil && !ValidDigest(*m.PreviousManifestSHA256) ||
		m.WindowFromUs < -62135596800000000 || m.KnownCutoffUs <= m.WindowFromUs || m.CreatedAtUs < m.KnownCutoffUs || m.CreatedAtUs > 253402300799999999 ||
		len(m.Workouts) > MaxCompletenessWorkouts || m.Workouts == nil || m.Streams == nil || m.Exclusions == nil ||
		len(m.Exclusions) > MaxCompletenessExclusions || !EqualStringSet(m.SupportedStreams, WorkoutCompletenessTypes()) || !EqualStringSet(m.UnsupportedStreams, WorkoutCompletenessUnsupported) {
		return bad()
	}
	// The inventory invariant. Nothing unresolved may be committed.
	inv := m.Inventory
	if inv.VisibleCount != inv.EligibleCount+inv.ExcludedCount+inv.UnclassifiedCount || inv.UnclassifiedCount != 0 || inv.PendingClassificationCount != 0 ||
		inv.VisibleCount < 0 || inv.VisibleCount > MaxCompletenessWorkouts || inv.EligibleCount != len(m.Workouts) || inv.ExcludedCount != len(m.Exclusions) {
		return Fail(422, "inventory_invariant_violation")
	}
	na := []string{}
	if len(m.Workouts) == 0 {
		na = append(append([]string{}, WorkoutCompletenessQuantities...), RouteType)
	}
	if !EqualStringSet(m.NotApplicableStreams, na) {
		return bad()
	}
	expected := map[string]bool{"workouts": true}
	seenWorkouts := map[string]bool{}
	eligibleHashes := map[string]bool{}
	for _, w := range m.Workouts {
		if !ValidID(w.WorkoutID) || seenWorkouts[w.WorkoutID] || !ValidDigest(w.BundleSHA256) || len(w.Series) != len(WorkoutCompletenessQuantities) || (w.RouteState != "present" && w.RouteState != "absent") {
			return bad()
		}
		seenWorkouts[w.WorkoutID] = true
		eligibleHashes[WorkoutIdentitySHA256(w.WorkoutID)] = true
		series := []string{}
		for _, s := range w.Series {
			if !ValidDigest(s.SHA256) || s.PointCount < 0 || s.PointCount > MaxBundleSeriesPoints || s.QueryCompletedAtUs < m.KnownCutoffUs || s.QueryCompletedAtUs > m.CreatedAtUs {
				return bad()
			}
			series = append(series, s.SampleType)
		}
		if !EqualStringSet(series, WorkoutCompletenessQuantities) {
			return bad()
		}
		for _, t := range append(append([]string{}, WorkoutCompletenessQuantities...), RouteType) {
			expected[CompletenessKey(t, w.WorkoutID)] = true
		}
	}
	seenExcluded := map[string]bool{}
	for _, x := range m.Exclusions {
		if !ValidDigest(x.WorkoutIdentitySHA256) || seenExcluded[x.WorkoutIdentitySHA256] || eligibleHashes[x.WorkoutIdentitySHA256] {
			return Fail(422, "inventory_identity_conflict")
		}
		seenExcluded[x.WorkoutIdentitySHA256] = true
		// The server re-runs the frozen policy; a client-declared exclusion is
		// only a claim until this check passes.
		if SourcePolicyOutcome(x.Classification) != SourceOutcomeExcluded || x.Outcome != SourceOutcomeExcluded || x.Reason != SourceReasonExcluded ||
			x.ClassificationVersion != WorkoutSourceClassificationVer || x.SourcePolicyVersion != WorkoutSourcePolicyVersion {
			return Fail(422, "exclusion_policy_mismatch")
		}
		if err := x.SourceEvidence.Validate(); err != nil {
			return err
		}
		if x.SourceEvidence.Classification() != x.Classification || x.EvidenceSHA256 != x.SourceEvidence.SHA256() {
			return Fail(422, "exclusion_evidence_invalid")
		}
	}
	if inv.EligibleSHA256 != InventorySHA256(EligibleIdentitySHA256(m)) || inv.ExcludedSHA256 != InventorySHA256(m.ExcludedIdentitySHA256()) ||
		inv.VisibleSHA256 != InventorySHA256(m.VisibleIdentitySHA256()) {
		return Fail(422, "inventory_digest_mismatch")
	}
	if len(m.Streams) != len(expected) {
		return bad()
	}
	total, events := 0, 0
	for _, s := range m.Streams {
		parent := ""
		if s.WorkoutID != nil {
			parent = *s.WorkoutID
			if !seenWorkouts[parent] {
				return bad()
			}
		}
		if s.Key != CompletenessKey(s.SampleType, parent) || !expected[s.Key] || s.SampleType == WorkoutType && s.WorkoutID != nil || s.SampleType != WorkoutType && s.WorkoutID == nil || s.QueryFingerprint != CompletenessFingerprintV2(m.WindowFromUs, s.Key) || len(s.Pages) == 0 {
			return bad()
		}
		delete(expected, s.Key)
		total += len(s.Pages)
		for i, p := range s.Pages {
			if !ValidID(p.PageID) || p.Sequence < 1 || !ValidDigest(p.CandidateAnchorSHA256) || p.PreviousAnchorSHA256 != nil && !ValidDigest(*p.PreviousAnchorSHA256) || p.PreviousPageSHA256 != nil && !ValidDigest(*p.PreviousPageSHA256) || p.QueryStartedAtUs < m.KnownCutoffUs || p.QueryCompletedAtUs < p.QueryStartedAtUs || p.QueryCompletedAtUs > m.CreatedAtUs || p.Additions == nil || p.Deletions == nil || len(p.Additions)+len(p.Deletions) > 1000 || i < len(s.Pages)-1 && !p.HasMore {
				return bad()
			}
			for _, id := range append(append([]string{}, p.Additions...), p.Deletions...) {
				if !ValidID(id) {
					return bad()
				}
			}
			events += len(p.Additions) + len(p.Deletions)
		}
		if s.Pages[len(s.Pages)-1].HasMore {
			return Fail(409, "completeness_stream_pending")
		}
	}
	if total > MaxCompletenessPages || events > MaxCompletenessEvents {
		return Fail(413, "batch_too_large")
	}
	return nil
}
