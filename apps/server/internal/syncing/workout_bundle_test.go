package syncing

import (
	"encoding/json"
	"testing"
)

func validBundle() WorkoutBundle {
	start, end := int64(1_790_000_000_000_000), int64(1_790_000_060_000_000)
	duration, activity := int64(60_000_000), int64(37)
	workout := Event{Op: "upsert", Kind: "workout", Type: WorkoutType, UUID: NewID(), StartUs: &start, EndUs: &end,
		Source: &Source{BundleID: "synthetic.test"}, Workout: &Workout{ActivityType: &activity, DurationUs: &duration}}
	sample := Event{Op: "upsert", Kind: "sample", Type: "HKQuantityTypeIdentifierHeartRate", UUID: NewID(),
		StartUs: &start, EndUs: &end, Source: &Source{BundleID: "synthetic.test"},
		Quantity: &Quantity{Value: 120, Unit: "count/min"}}
	return WorkoutBundle{ProtocolVersion: 6, DatasetID: NewID(), DatasetEpoch: NewID(), CollectorEpoch: NewID(),
		WorkoutID: workout.UUID, Workout: workout, Samples: []Event{sample},
		Series: []WorkoutBundleSeries{{SampleType: sample.Type, SampleUUID: sample.UUID,
			Points: []SeriesPoint{{StartUs: start, EndUs: start, Value: 1, Unit: "count/min"}}}},
		Routes: []WorkoutBundleRoute{{RouteID: NewID(), Source: Source{BundleID: "synthetic.test"},
			Points: []WorkoutBundlePoint{{RecordedAtUs: start, Latitude: 31.2, Longitude: 121.4}}}}}
}

func TestWorkoutBundleValidation(t *testing.T) {
	bundle := validBundle()
	if err := bundle.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(bundle); err != nil {
		t.Fatal(err)
	}
}

func TestWorkoutBundleRejectsBrokenMembership(t *testing.T) {
	cases := map[string]func(*WorkoutBundle){
		"unknown protocol":    func(b *WorkoutBundle) { b.ProtocolVersion = 2 },
		"workout id mismatch": func(b *WorkoutBundle) { b.WorkoutID = NewID() },
		"duplicate sample":    func(b *WorkoutBundle) { b.Samples = append(b.Samples, b.Samples[0]) },
		"series without parent": func(b *WorkoutBundle) {
			b.Series[0].SampleUUID = NewID()
		},
		"series unit mismatch": func(b *WorkoutBundle) { b.Series[0].Points[0].Unit = "m" },
		"series outside parent": func(b *WorkoutBundle) {
			b.Series[0].Points[0].StartUs = *b.Samples[0].StartUs - 1
		},
		"duplicate route": func(b *WorkoutBundle) { b.Routes = append(b.Routes, b.Routes[0]) },
		"route illegal timestamp": func(b *WorkoutBundle) {
			b.Routes[0].Points = append(b.Routes[0].Points, WorkoutBundlePoint{RecordedAtUs: -62135596800000001, Latitude: 31, Longitude: 121})
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			bundle := validBundle()
			mutate(&bundle)
			if err := bundle.Validate(); err == nil {
				t.Fatal("invalid bundle accepted")
			}
		})
	}
}

func TestBundleValidateProtocol6Only(t *testing.T) {
	for _, version := range []int{2, 3, 4, 5} {
		bundle := validBundle()
		bundle.ProtocolVersion = version
		if err := bundle.Validate(); err == nil {
			t.Fatalf("protocol %d: expected rejection, got nil", version)
		} else if e, ok := err.(*Error); !ok || e.Status != 422 || e.Code != "unsupported_payload" {
			t.Fatalf("protocol %d: unexpected error %#v", version, err)
		}
	}
	bundle := validBundle()
	if err := bundle.Validate(); err != nil {
		t.Fatalf("protocol 6: %v", err)
	}
}
