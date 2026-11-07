package research

import (
	"strings"
	"testing"
	"time"
)

func TestPseudonymizeSeparatesMappingAndIsStable(t *testing.T) {
	secret := []byte("synthetic-test-secret-0123456789")
	snap := ProspectiveSnapshot{DatasetID: "d", Model: "rai.vo2max.submax", ManifestSHA: "m", AsOf: time.Unix(100, 0).UTC(), KnownAt: time.Unix(90, 0).UTC(), Observations: []Prediction{{AthleteKey: "a", ClusterKey: "c", PredictedAt: time.Unix(91, 0).UTC(), HorizonDays: 7}, {AthleteKey: "b", ClusterKey: "c", PredictedAt: time.Unix(91, 0).UTC(), HorizonDays: 7}}}
	outcomes := []Outcome{{AthleteKey: "a", ClusterKey: "c", ObservedAt: time.Unix(95, 0).UTC(), Value: ptr(1), Independent: true}}
	out, outs, _, mapping, err := Pseudonymize(snap, outcomes, []string{"b"}, secret)
	if err != nil {
		t.Fatal(err)
	}
	if out.Observations[0].AthleteKey == "a" || outs[0].AthleteKey == "a" {
		t.Fatal("athlete key leaked")
	}
	if !strings.HasPrefix(out.Observations[0].AthleteKey, "ath_") {
		t.Fatalf("unexpected pseudonym %q", out.Observations[0].AthleteKey)
	}
	if len(mapping) != 2 || mapping["a"] == "a" {
		t.Fatalf("mapping not separated: %#v", mapping)
	}
	again, _, _, mapping2, err := Pseudonymize(snap, outcomes, []string{"b"}, secret)
	if err != nil || again.Observations[0].AthleteKey != out.Observations[0].AthleteKey || mapping2["a"] != mapping["a"] {
		t.Fatal("pseudonym is not deterministic")
	}
	if _, _, _, _, err := Pseudonymize(snap, outcomes, nil, []byte("short")); err == nil {
		t.Fatal("short secret accepted")
	}
}

func ptr(v float64) *float64 { return &v }
