package syncing

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

const (
	testWorkoutStart = int64(1793059200000000)
	testWorkoutEnd   = int64(1793059260000000)
)

func workoutActivityLine(t *testing.T, ordinal int, uuid string, start int64) string {
	t.Helper()
	body := `{"kind":"activity","ordinal":` + itoa(ordinal) + `,"activity":{"uuid":"` + uuid +
		`","startUs":` + itoa64(start) + `,"durationUs":60000000,"configuration":{"activityType":37,"locationType":1,"swimmingLocationType":0},` +
		`"statistics":[{"type":"HKQuantityTypeIdentifierHeartRate","startUs":` + itoa64(start) + `,"endUs":` + itoa64(start+1000000) + `,"average":{"value":130,"unit":"count/min"}}],` +
		`"events":[{"type":1,"startUs":` + itoa64(start) + `,"endUs":` + itoa64(start+1000000) + `}]}}`
	return body
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func TestParseWorkoutDetailChunkAndManifest(t *testing.T) {
	activityUUID := NewID()
	lines := []string{
		workoutActivityLine(t, 0, activityUUID, testWorkoutStart),
		`{"kind":"statistic","ordinal":0,"statistic":{"type":"HKQuantityTypeIdentifierHeartRate","startUs":1793059200000000,"endUs":1793059260000000,"average":{"value":131,"unit":"count/min"}}}`,
		`{"kind":"event","ordinal":0,"event":{"type":1,"startUs":1793059201000000,"endUs":1793059202000000,"metadata":{"lane":1}}}`,
	}
	wire := gzipSeriesLines(t, lines...)
	chunk, err := ParseWorkoutDetailChunk(bytes.NewReader(wire), testWorkoutStart, testWorkoutEnd)
	if err != nil {
		t.Fatal(err)
	}
	if chunk.DetailCount != 3 || chunk.TotalDetails != 5 {
		t.Fatalf("detail counts = %d/%d", chunk.DetailCount, chunk.TotalDetails)
	}
	if chunk.Entries[0].Kind != "activity" || chunk.Entries[0].Activity == nil || chunk.Entries[0].Activity.UUID != activityUUID {
		t.Fatalf("activity entry changed: %+v", chunk.Entries[0])
	}
	if chunk.Entries[1].Statistic == nil || chunk.Entries[2].Event == nil {
		t.Fatal("statistic/event entry not decoded")
	}
	canonical, digest := WorkoutDetailsCanonical(chunk.Entries)
	if chunk.DetailsSHA256 != hex.EncodeToString(digest) {
		t.Fatal("detail digest differs")
	}
	chain := AdvanceWorkoutPackageManifest(EmptyWorkoutPackageManifest(), digest)
	if len(chain) != 32 {
		t.Fatal("manifest length")
	}
	decoded, err := DecodeWorkoutDetails(canonical)
	if err != nil || len(decoded) != 3 || decoded[2].Event == nil {
		t.Fatalf("stored canonical details do not decode: %v", err)
	}
}

func TestParseWorkoutDetailChunkRejectsUnsupportedInput(t *testing.T) {
	validActivity := workoutActivityLine(t, 0, NewID(), testWorkoutStart)
	cases := map[string]string{
		"unknown field":          `{"kind":"statistic","ordinal":0,"statistic":{"type":"HKQuantityTypeIdentifierHeartRate","startUs":1793059200000000,"endUs":1793059260000000,"average":{"value":130,"unit":"count/min"}},"extra":1}`,
		"null ordinal":           `{"kind":"statistic","ordinal":null,"statistic":{"type":"HKQuantityTypeIdentifierHeartRate","startUs":1793059200000000,"endUs":1793059260000000,"average":{"value":130,"unit":"count/min"}}}`,
		"two payloads":           `{"kind":"statistic","ordinal":0,"statistic":{"type":"HKQuantityTypeIdentifierHeartRate","startUs":1793059200000000,"endUs":1793059260000000,"average":{"value":130,"unit":"count/min"}},"event":{"type":1,"startUs":1793059200000000,"endUs":1793059260000000}}`,
		"unknown kind":           `{"kind":"zone","ordinal":0,"event":{"type":1,"startUs":1793059200000000,"endUs":1793059260000000}}`,
		"wrong field kind":       `{"kind":"activity","ordinal":0,"statistic":{"type":"HKQuantityTypeIdentifierHeartRate","startUs":1793059200000000,"endUs":1793059260000000,"average":{"value":130,"unit":"count/min"}}}`,
		"outside parent":         `{"kind":"event","ordinal":0,"event":{"type":1,"startUs":1793059199000000,"endUs":1793059260000000}}`,
		"missing statistic":      `{"kind":"statistic","ordinal":0}`,
		"null activity":          `{"kind":"activity","ordinal":0,"activity":null}`,
		"null activity metadata": `{"kind":"activity","ordinal":0,"activity":{"uuid":"11111111-1111-4111-8111-111111111111","startUs":1793059200000000,"durationUs":60000000,"configuration":{"activityType":37,"locationType":1,"swimmingLocationType":0},"metadata":null}}`,
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseWorkoutDetailChunk(bytes.NewReader(gzipSeriesLines(t, line)), testWorkoutStart, testWorkoutEnd)
			requireSeriesError(t, err, 422, "unsupported_payload")
		})
	}
	t.Run("valid activity control", func(t *testing.T) {
		if _, err := ParseWorkoutDetailChunk(bytes.NewReader(gzipSeriesLines(t, validActivity)), testWorkoutStart, testWorkoutEnd); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("empty chunk", func(t *testing.T) {
		if _, err := ParseWorkoutDetailChunk(bytes.NewReader(gzipSeriesLines(t)), testWorkoutStart, testWorkoutEnd); err == nil {
			t.Fatal("empty chunk accepted")
		}
	})
}

func TestWorkoutDetailBoundsAndBudget(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
	}{
		{"activity outside parent", workoutActivityLine(t, 0, NewID(), testWorkoutStart-1)},
		{"unknown statistic type", `{"kind":"statistic","ordinal":0,"statistic":{"type":"HKQuantityTypeIdentifierNope","startUs":1793059200000000,"endUs":1793059260000000,"average":{"value":1,"unit":"count"}}}`},
		{"statistic wrong unit", `{"kind":"statistic","ordinal":0,"statistic":{"type":"HKQuantityTypeIdentifierHeartRate","startUs":1793059200000000,"endUs":1793059260000000,"average":{"value":1,"unit":"ms"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseWorkoutDetailChunk(bytes.NewReader(gzipSeriesLines(t, tc.line)), testWorkoutStart, testWorkoutEnd)
			requireSeriesError(t, err, 422, "unsupported_payload")
		})
	}
}

func TestWorkoutPackageRequestAndSealValidation(t *testing.T) {
	start, end := testWorkoutStart, testWorkoutEnd
	activityType, duration := int64(37), end-start
	valid := func() WorkoutPackageRequest {
		return WorkoutPackageRequest{DatasetID: NewID(), DatasetEpoch: NewID(), CollectorEpoch: NewID(), WorkoutID: NewID(),
			StartUs: &start, EndUs: &end, Source: &Source{BundleID: "synthetic.test"},
			Workout: &Workout{ActivityType: &activityType, DurationUs: &duration}}
	}
	if err := valid().Validate(); err != nil {
		t.Fatal("valid core rejected", err)
	}
	withDetails := valid()
	withDetails.Workout.Statistics = []WorkoutStatistic{{Type: "HKQuantityTypeIdentifierHeartRate"}}
	if err := withDetails.Validate(); err == nil {
		t.Fatal("core with statistics accepted")
	}
	missingSource := valid()
	missingSource.Source = nil
	if err := missingSource.Validate(); err == nil {
		t.Fatal("missing source accepted")
	}
	if err := (WorkoutPackageSeal{ChunkCount: 0, TotalDetails: 1, ManifestSHA256: strings.Repeat("0", 64)}).Validate(); err == nil {
		t.Fatal("zero chunk seal accepted")
	}
	if err := (WorkoutPackageSeal{ChunkCount: 1, TotalDetails: 1, ManifestSHA256: "bad"}).Validate(); err == nil {
		t.Fatal("bad manifest accepted")
	}
}
