package syncing

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

const WorkoutCompletenessContract = "workout-sync-completeness/v1"
const WorkoutCompletenessVersion = 1
const MaxCompletenessBytes = 16 << 20
const MaxCompletenessWorkouts = 256
const MaxCompletenessPages = 8192
const MaxCompletenessEvents = 250000

var WorkoutCompletenessQuantities = []string{
	"HKQuantityTypeIdentifierHeartRate", "HKQuantityTypeIdentifierDistanceWalkingRunning",
	"HKQuantityTypeIdentifierActiveEnergyBurned", "HKQuantityTypeIdentifierRunningSpeed",
	"HKQuantityTypeIdentifierRunningPower", "HKQuantityTypeIdentifierStepCount",
	"HKQuantityTypeIdentifierBasalEnergyBurned", "HKQuantityTypeIdentifierRunningGroundContactTime",
	"HKQuantityTypeIdentifierRunningStrideLength", "HKQuantityTypeIdentifierRunningVerticalOscillation",
}
var WorkoutCompletenessUnsupported = []string{"native_running_cadence", "elevation_sample"}

func WorkoutCompletenessTypes() []string {
	x := append([]string{WorkoutType, RouteType}, WorkoutCompletenessQuantities...)
	sort.Strings(x)
	return x
}
func CompletenessKey(kind, workout string) string {
	if kind == WorkoutType {
		return "workouts"
	}
	return "workout:" + workout + ":" + kind
}
func CompletenessFingerprint(from int64, key string) string {
	h := sha256.Sum256([]byte(WorkoutCompletenessContract + "|stream-set:1|running-overlap-v1|" + strconv.FormatInt(from, 10) + "|" + key))
	return "sha256:" + hex.EncodeToString(h[:])
}

type CompletenessPage struct {
	PageID                string   `json:"pageID"`
	Sequence              int64    `json:"sequence"`
	PreviousPageSHA256    *string  `json:"previousPageSHA256"`
	PreviousAnchorSHA256  *string  `json:"previousAnchorSHA256"`
	CandidateAnchorSHA256 string   `json:"candidateAnchorSHA256"`
	QueryStartedAtUs      int64    `json:"queryStartedAtUs"`
	QueryCompletedAtUs    int64    `json:"queryCompletedAtUs"`
	Additions             []string `json:"additions"`
	Deletions             []string `json:"deletions"`
	HasMore               bool     `json:"hasMore"`
}
type CompletenessHead struct {
	Sequence         int64  `json:"sequence"`
	PageSHA256       string `json:"pageSHA256"`
	AnchorSHA256     string `json:"anchorSHA256"`
	QueryFingerprint string `json:"queryFingerprint"`
}
type CompletenessStream struct {
	Key              string             `json:"key"`
	SampleType       string             `json:"sampleType"`
	WorkoutID        *string            `json:"workoutID"`
	QueryFingerprint string             `json:"queryFingerprint"`
	Pages            []CompletenessPage `json:"pages"`
}
type CompletenessSeries struct {
	SampleType         string `json:"sampleType"`
	SHA256             string `json:"sha256"`
	PointCount         int    `json:"pointCount"`
	QueryCompletedAtUs int64  `json:"queryCompletedAtUs"`
}
type CompletenessWorkout struct {
	WorkoutID    string               `json:"workoutID"`
	BundleSHA256 string               `json:"bundleSHA256"`
	RouteState   string               `json:"routeState"`
	Series       []CompletenessSeries `json:"series"`
}
type WorkoutSyncCompletenessManifest struct {
	Contract               string                `json:"contract"`
	ProtocolVersion        int                   `json:"protocolVersion"`
	DatasetID              string                `json:"datasetID"`
	DatasetEpoch           string                `json:"datasetEpoch"`
	CollectorEpoch         string                `json:"collectorEpoch"`
	ManifestID             string                `json:"manifestID"`
	Generation             int64                 `json:"generation"`
	PreviousManifestSHA256 *string               `json:"previousManifestSHA256"`
	StreamSetVersion       int                   `json:"streamSetVersion"`
	WindowFromUs           int64                 `json:"windowFromUs"`
	KnownCutoffUs          int64                 `json:"knownCutoffUs"`
	CreatedAtUs            int64                 `json:"createdAtUs"`
	SupportedStreams       []string              `json:"supportedStreams"`
	UnsupportedStreams     []string              `json:"unsupportedStreams"`
	NotApplicableStreams   []string              `json:"notApplicableStreams"`
	Streams                []CompletenessStream  `json:"streams"`
	Workouts               []CompletenessWorkout `json:"workouts"`
}
type WorkoutCompletenessReceipt struct {
	Contract        string `json:"contract"`
	ProtocolVersion int    `json:"protocolVersion"`
	DatasetID       string `json:"datasetID"`
	DatasetEpoch    string `json:"datasetEpoch"`
	CollectorEpoch  string `json:"collectorEpoch"`
	ManifestID      string `json:"manifestID"`
	Generation      int64  `json:"generation"`
	ManifestSHA256  string `json:"manifestSHA256"`
	IngestStatus    string `json:"ingestStatus"`
	CommitRevision  int64  `json:"commitRevision"`
	StreamCount     int    `json:"streamCount"`
	WorkoutCount    int    `json:"workoutCount"`
}

func completenessCanonical(v any) []byte {
	raw, _ := json.Marshal(v)
	var tree any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	_ = d.Decode(&tree)
	raw, _ = json.Marshal(tree)
	return raw
}
func CompletenessPageDigest(p CompletenessPage) string { return BundleDigest(completenessCanonical(p)) }
func CompletenessHeadDigest(heads map[string]CompletenessHead) string {
	return BundleDigest(completenessCanonical(heads))
}
func EqualStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]string{}, a...), append([]string{}, b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] || i > 0 && x[i] == x[i-1] {
			return false
		}
	}
	return true
}
func DecodeWorkoutCompleteness(raw []byte) (WorkoutSyncCompletenessManifest, error) {
	var m WorkoutSyncCompletenessManifest
	if len(raw) > MaxCompletenessBytes {
		return m, Fail(413, "batch_too_large")
	}
	if err := DecodeStrictJSON(raw, &m); err != nil {
		return m, Fail(422, "invalid_payload")
	}
	if err := vo2Required(raw, strings.Fields("contract protocolVersion datasetID datasetEpoch collectorEpoch manifestID generation previousManifestSHA256 streamSetVersion windowFromUs knownCutoffUs createdAtUs supportedStreams unsupportedStreams notApplicableStreams streams workouts"), "previousManifestSHA256"); err != nil {
		return m, err
	}
	var root map[string]json.RawMessage
	_ = json.Unmarshal(raw, &root)
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
	if err := m.Validate(); err != nil {
		return m, err
	}
	return m, nil
}
func (m WorkoutSyncCompletenessManifest) Validate() error {
	bad := func() error { return Fail(422, "invalid_completeness") }
	if m.Contract != WorkoutCompletenessContract || m.ProtocolVersion != 1 || m.StreamSetVersion != 1 {
		return Fail(422, "unsupported_version")
	}
	for _, id := range []string{m.DatasetID, m.DatasetEpoch, m.CollectorEpoch, m.ManifestID} {
		if !ValidID(id) {
			return bad()
		}
	}
	if m.Generation < 1 || m.Generation > 1 && m.PreviousManifestSHA256 == nil || m.PreviousManifestSHA256 != nil && !ValidDigest(*m.PreviousManifestSHA256) ||
		m.WindowFromUs < -62135596800000000 || m.KnownCutoffUs <= m.WindowFromUs || m.CreatedAtUs < m.KnownCutoffUs || m.CreatedAtUs > 253402300799999999 ||
		len(m.Workouts) > MaxCompletenessWorkouts || m.Workouts == nil || m.Streams == nil || !EqualStringSet(m.SupportedStreams, WorkoutCompletenessTypes()) || !EqualStringSet(m.UnsupportedStreams, WorkoutCompletenessUnsupported) {
		return bad()
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
	for _, w := range m.Workouts {
		if !ValidID(w.WorkoutID) || seenWorkouts[w.WorkoutID] || !ValidDigest(w.BundleSHA256) || len(w.Series) != len(WorkoutCompletenessQuantities) || (w.RouteState != "present" && w.RouteState != "absent") {
			return bad()
		}
		seenWorkouts[w.WorkoutID] = true
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
		if s.Key != CompletenessKey(s.SampleType, parent) || !expected[s.Key] || s.SampleType == WorkoutType && s.WorkoutID != nil || s.SampleType != WorkoutType && s.WorkoutID == nil || s.QueryFingerprint != CompletenessFingerprint(m.WindowFromUs, s.Key) || len(s.Pages) == 0 {
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

// Points retain source order. Parent groups are sorted for query order neutrality.
func CompletenessSeriesDigest(series []WorkoutBundleSeries, kind string) (string, int) {
	items := []map[string]any{}
	count := 0
	for _, s := range series {
		if s.SampleType != kind {
			continue
		}
		points := []map[string]any{}
		for _, p := range s.Points {
			points = append(points, map[string]any{"startUs": p.StartUs, "endUs": p.EndUs, "value": p.Value, "unit": p.Unit})
			count++
		}
		items = append(items, map[string]any{"sampleUUID": s.SampleUUID, "points": points})
	}
	sort.Slice(items, func(i, j int) bool { return items[i]["sampleUUID"].(string) < items[j]["sampleUUID"].(string) })
	return BundleDigest(completenessCanonical(items)), count
}
