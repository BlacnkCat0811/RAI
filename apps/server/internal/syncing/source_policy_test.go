package syncing

import (
	"os"
	"strings"
	"testing"
)

// TestWorkoutSourcePolicyGoldenVectors is the shared Swift/Go policy oracle.
// Both languages must produce exactly these classifications for exactly this
// fixture, and the fixture digest is the policy identity bound into manifests.
func TestWorkoutSourcePolicyGoldenVectors(t *testing.T) {
	raw, err := os.ReadFile("../../../../contracts/fixtures/workout-source-policy-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../../../contracts/fixtures/workout-source-policy-v1.sha256")
	if err != nil {
		t.Fatal(err)
	}
	if BundleDigest(raw) != strings.TrimSpace(string(want)) {
		t.Fatal("policy document changed; policy identity must be revised, not edited")
	}
	if WorkoutSourcePolicySHA256 != strings.TrimSpace(string(want)) {
		t.Fatal("bound policy digest differs from the frozen fixture")
	}
	var doc struct {
		Contract                string            `json:"contract"`
		PolicyVersion           string            `json:"policyVersion"`
		ClassificationVersion   int               `json:"classificationVersion"`
		Outcomes                map[string]string `json:"outcomes"`
		Reasons                 map[string]string `json:"reasons"`
		EligibleClassifications []string          `json:"eligibleClassifications"`
		ExcludedClassifications []string          `json:"excludedClassifications"`
		Vectors                 []struct {
			Name           string               `json:"name"`
			Classification string               `json:"classification"`
			Outcome        string               `json:"outcome"`
			Evidence       SourcePolicyEvidence `json:"evidence"`
		} `json:"vectors"`
	}
	if err = DecodeStrictJSON(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.PolicyVersion != WorkoutSourcePolicyVersion || doc.ClassificationVersion != WorkoutSourceClassificationVer {
		t.Fatal("policy identity mismatch")
	}
	for _, v := range doc.Vectors {
		if got := v.Evidence.Classification(); got != v.Classification {
			t.Fatalf("%s: classified %q, want %q", v.Name, got, v.Classification)
		}
		if got := SourcePolicyOutcome(v.Classification); got != v.Outcome {
			t.Fatalf("%s: outcome %q, want %q", v.Name, got, v.Outcome)
		}
		if doc.Outcomes[v.Classification] != v.Outcome {
			t.Fatalf("%s: outcome table drifted", v.Name)
		}
		if err := v.Evidence.Validate(); err != nil {
			t.Fatalf("%s: %v", v.Name, err)
		}
		if !ValidDigest(v.Evidence.SHA256()) {
			t.Fatalf("%s: evidence digest invalid", v.Name)
		}
	}
	// The policy is frozen: only Apple Watch native running Workouts are
	// eligible, and the exclusions are exactly third-party and user-entered.
	if !EqualStringSet(doc.EligibleClassifications, []string{"apple_watch"}) {
		t.Fatal("eligible set changed")
	}
	if !EqualStringSet(doc.ExcludedClassifications, []string{"third_party", "user_entered"}) {
		t.Fatal("excluded set changed")
	}
	for _, name := range []string{"unclassified_missing_bundle", "unclassified_missing_product_type", "unclassified_ambiguous_model", "unclassified_scoped_without_device", "unclassified_other_apple_source", "unclassified_non_apple_manufacturer"} {
		found := false
		for _, v := range doc.Vectors {
			if v.Name == name {
				found = true
				if SourcePolicyOutcome(v.Classification) != SourceOutcomeUnclassified {
					t.Fatalf("%s must stay unclassified", name)
				}
			}
		}
		if !found {
			t.Fatalf("missing golden vector %s", name)
		}
	}
	// Nothing may map to unclassified by omission except source_unknown and
	// associated_apple_watch; a silent fallback would let a new class through.
	for class, outcome := range sourcePolicyOutcomes {
		if outcome == SourceOutcomeEligible && class != "apple_watch" {
			t.Fatalf("%s cannot be eligible", class)
		}
	}
	if SourcePolicyOutcome("brand_new_class") != SourceOutcomeUnclassified {
		t.Fatal("unknown classification must be unclassified")
	}
}

func TestDeviceScopedAppleHealthSourceRequiresRealWatchEvidence(t *testing.T) {
	source := HealthKitSource{BundleID: "com.apple.health." + NewID(), ProductType: "Watch8,3", Version: "1", Device: &HealthKitDevice{Manufacturer: "Apple Inc.", Model: "Watch"}}
	if source.Classification(nil) != "apple_watch" {
		t.Fatal("device scoped Watch evidence not recognized")
	}
	child := source
	child.Device = nil
	if child.Classification(nil) != "source_unknown" || child.Classification(&source) != "associated_apple_watch" {
		t.Fatal("missing child device fabricated or association lost")
	}
	child = source
	child.ProductType = "iPhone13,4"
	if child.Classification(&source) != "associated_apple_watch" {
		t.Fatal("paired iPhone child not associated with Watch parent")
	}
	child = source
	child.ProductType = "iPhone13,4"
	child.Version = "2"
	if child.Classification(&source) != "source_unknown" {
		t.Fatal("version mismatch hidden by parent")
	}
	child = source
	child.BundleID = "com.apple.health.not-a-device-uuid"
	if child.Classification(nil) != "source_unknown" {
		t.Fatal("arbitrary prefix accepted")
	}
}

func TestWorkoutCoreEqualityIgnoresMetadataButBindsMicroseconds(t *testing.T) {
	b := validBundle()
	start, end, duration := int64(253402300799999899), int64(253402300799999999), int64(0)
	b.Workout.StartUs = &start
	b.Workout.EndUs = &end
	b.Workout.Workout.DurationUs = &duration
	same := b.Workout
	same.Metadata = map[string]any{"HKIndoorWorkout": false}
	if !WorkoutCoreEqual(b.Workout, same) {
		t.Fatal("metadata-only change rejected")
	}
	changed := same
	changedStart := start + 1
	changed.StartUs = &changedStart
	if WorkoutCoreEqual(b.Workout, changed) {
		t.Fatal("metadata comparison rounded away an immutable microsecond change")
	}
}

func TestWorkoutSourcePolicyEvidenceIsBoundedAndCanonical(t *testing.T) {
	e := SourcePolicyEvidence{BundleID: "com.strava.Strava", ProductType: "iPhone14,5"}
	if e.Classification() != "third_party" {
		t.Fatal("third-party evidence must classify as third_party")
	}
	if e.SHA256() != (SourcePolicyEvidence{BundleID: "com.strava.Strava", ProductType: "iPhone14,5"}).SHA256() {
		t.Fatal("evidence digest must be deterministic")
	}
	if e.SHA256() == (SourcePolicyEvidence{BundleID: "com.strava.Strava", ProductType: "iPhone14,6"}).SHA256() {
		t.Fatal("evidence digest must bind the descriptor")
	}
	big := SourcePolicyEvidence{BundleID: strings.Repeat("x", 4096)}
	if big.Validate() == nil {
		t.Fatal("oversized evidence must be rejected")
	}
}
