package syncing

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestWorkoutCompletenessSharedGoldenAndStrictShape(t *testing.T) {
	raw, err := os.ReadFile("../../../../contracts/fixtures/workout-completeness-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	m, err := DecodeWorkoutCompleteness(raw)
	if err != nil {
		t.Fatal(err)
	}
	pageHash, _ := os.ReadFile("../../../../contracts/fixtures/workout-completeness-page-v1.sha256")
	if CompletenessPageDigest(m.Streams[0].Pages[0]) != strings.TrimSpace(string(pageHash)) {
		t.Fatal("page canonical golden changed")
	}
	manifestHash, _ := os.ReadFile("../../../../contracts/fixtures/workout-completeness-v1.sha256")
	if BundleDigest(bytes.TrimSpace(raw)) != strings.TrimSpace(string(manifestHash)) {
		t.Fatal("manifest golden changed")
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"hasMore":false,`, "", 1),
		strings.Replace(string(raw), `"hasMore":false`, `"hasMore":null`, 1),
		strings.Replace(string(raw), `"hasMore":false`, `"hasMore":false,"hasMore":false`, 1),
		strings.Replace(string(raw), `"hasMore":false`, `"HasMore":false`, 1),
		strings.Replace(string(raw), `"hasMore":false`, `"hasMore":false,"complete":true`, 1),
		strings.Replace(string(raw), `"streamSetVersion":1`, `"streamSetVersion":2`, 1),
		strings.Replace(string(raw), `"hasMore":false`, `"hasMore":true`, 1),
	} {
		if _, err := DecodeWorkoutCompleteness([]byte(bad)); err == nil {
			t.Fatal("unsupported evidence shape accepted")
		}
	}
}
func TestWorkoutCompletenessCanonicalPagePersistenceProperty(t *testing.T) {
	for i := int64(1); i <= 1024; i++ {
		page := CompletenessPage{PageID: "10000000-0000-4000-8000-000000000005", Sequence: i, CandidateAnchorSHA256: strings.Repeat("a", 64), QueryStartedAtUs: 1700000000000000 + i, QueryCompletedAtUs: 1700000000000001 + i, Additions: []string{}, Deletions: []string{}, HasMore: i%2 == 0}
		raw, _ := json.Marshal(page)
		var decoded CompletenessPage
		if err := DecodeStrictJSON(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		if CompletenessPageDigest(page) != CompletenessPageDigest(decoded) {
			t.Fatal("page identity changed across persistence")
		}
	}
}

func TestWorkoutCompletenessSeriesSharedGolden(t *testing.T) {
	raw, err := os.ReadFile("../../../../contracts/fixtures/workout-completeness-series-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture []struct {
		SampleUUID string        `json:"sampleUUID"`
		Points     []SeriesPoint `json:"points"`
	}
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	series := []WorkoutBundleSeries{}
	for _, item := range capture {
		series = append(series, WorkoutBundleSeries{SampleType: WorkoutCompletenessQuantities[0], SampleUUID: item.SampleUUID, Points: item.Points})
	}
	want, err := os.ReadFile("../../../../contracts/fixtures/workout-completeness-series-v1.sha256")
	if err != nil {
		t.Fatal(err)
	}
	got, count := CompletenessSeriesDigest(series, WorkoutCompletenessQuantities[0])
	if got != strings.TrimSpace(string(want)) || count != 3 {
		t.Fatal("cross-language series identity changed")
	}
	series[0], series[1] = series[1], series[0]
	reordered, _ := CompletenessSeriesDigest(series, WorkoutCompletenessQuantities[0])
	if got != reordered {
		t.Fatal("parent query order changed capture identity")
	}
}
