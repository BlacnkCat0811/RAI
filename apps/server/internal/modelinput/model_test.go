package modelinput

import (
	"encoding/json"
	"testing"
	"time"
)

func base(now time.Time) Request {
	return Request{ID: "6f9619ff-8b86-4d01-b42d-00cf4fc964ff", Kind: "sleep_target", SubjectKey: "athlete", EffectiveFrom: now.Add(-time.Hour).Format(time.RFC3339), Provenance: "user_form", Payload: Payload{"hours": 8.0}}
}
func TestSleepTargetCanonicalStable(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	r := base(now)
	a, err := Validate(r, now)
	if err != nil {
		t.Fatal(err)
	}
	r.Payload = Payload{"hours": 7.0}
	b, err := Validate(r, now)
	if err != nil {
		t.Fatal(err)
	}
	if a.ContentHash == b.ContentHash {
		t.Fatal("payload change must change hash")
	}
	if a.Schema != SchemaSleepTarget {
		t.Fatal(a.Schema)
	}
}
func TestRejectsUnknownKindAndFutureMeasurement(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	r := base(now)
	r.Kind = "generic_json_pocket"
	if _, err := Validate(r, now); err == nil {
		t.Fatal("unknown kind accepted")
	}
	r = base(now)
	r.Kind = "hr_reference"
	r.Payload = Payload{"maxHR": 190.0, "restingHR": 50.0, "measuredAt": now.Add(time.Hour).Format(time.RFC3339)}
	if _, err := Validate(r, now); err == nil {
		t.Fatal("future measurement accepted")
	}
	r.Payload = Payload{"maxHR": 50.0, "restingHR": 60.0}
	if _, err := Validate(r, now); err == nil {
		t.Fatal("max<=rest accepted")
	}
}
func TestEffortRequiresExplicitConfirmation(t *testing.T) {
	now := time.Now().UTC()
	r := base(now)
	r.Kind = "workout_effort"
	r.Payload = Payload{"workoutID": "6f9619ff-8b86-4d01-b42d-00cf4fc964ff", "mode": "maximal_supported"}
	if _, err := Validate(r, now); err == nil {
		t.Fatal("unconfirmed effort accepted")
	}
	r.Payload["userConfirmed"] = true
	c, err := Validate(r, now)
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	_ = json.Unmarshal(c.Payload, &p)
	if p["mode"] != "maximal_supported" {
		t.Fatal(p)
	}
}
func TestTRIMPParametersRequireExplicitBoundedValues(t *testing.T) {
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	r := base(now)
	r.Kind = "trimp_parameters"
	r.Payload = Payload{"restingHR": 52.0, "maxHR": 188.0, "coefficientA": 0.64, "coefficientB": 1.92}
	c, err := Validate(r, now)
	if err != nil || c.Schema != SchemaTRIMPParameters {
		t.Fatalf("valid parameters rejected: %#v %v", c, err)
	}
	var stored map[string]float64
	if err = json.Unmarshal(c.Payload, &stored); err != nil || stored["coefficientA"] != 0.64 || stored["restingHR"] != 52 {
		t.Fatalf("canonical payload=%s error=%v", c.Payload, err)
	}
	for _, mutate := range []func(Payload){
		func(p Payload) { p["maxHR"] = 52.0 },
		func(p Payload) { p["maxHR"] = 220.0 },
		func(p Payload) { p["restingHR"] = 29.0 },
		func(p Payload) { p["coefficientA"] = 0.0 },
		func(p Payload) { p["coefficientB"] = 10.1 },
		func(p Payload) { delete(p, "restingHR") },
		func(p Payload) { p["method"] = "user_entered" },
	} {
		bad := base(now)
		bad.Kind = "trimp_parameters"
		bad.Payload = Payload{"restingHR": 52.0, "maxHR": 188.0, "coefficientA": 0.64, "coefficientB": 1.92}
		mutate(bad.Payload)
		if _, err = Validate(bad, now); err == nil {
			t.Fatalf("invalid parameters accepted: %#v", bad.Payload)
		}
	}
}

func TestTRIMPParametersCannotBeRevoked(t *testing.T) {
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	id := "123e4567-e89b-12d3-a456-426614174000"
	target := "123e4567-e89b-12d3-a456-426614174001"
	r := Request{ID: id, Kind: "revocation", SubjectKey: "athlete", EffectiveFrom: now.Format(time.RFC3339), RevokesID: &target, Provenance: "api", Payload: Payload{"targetSchema": SchemaTRIMPParameters}}
	if _, err := Validate(r, now); err == nil {
		t.Fatal("revocation of an append-only parameter set accepted")
	}
}

func TestReceiptEmptyJobsIsArray(t *testing.T) {
	r := ReceiptFor(Canonical{ID: "x", Schema: "s", ContentHash: "h"}, time.Unix(1, 0), nil)
	b, _ := json.Marshal(r)
	if string(b) == "" || r.JobIDs == nil {
		t.Fatal("jobs must be empty array")
	}
}

func TestRevocationRequiresTargetSchemaAndRevokesID(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	id := "123e4567-e89b-12d3-a456-426614174000"
	target := "123e4567-e89b-12d3-a456-426614174001"
	base := Request{ID: id, Kind: "revocation", SubjectKey: "athlete", EffectiveFrom: now.Format(time.RFC3339), RevokesID: &target, Provenance: "api", Payload: Payload{"targetSchema": SchemaSleepTarget}}
	c, err := Validate(base, now)
	if err != nil || c.Schema != SchemaSleepTarget {
		t.Fatalf("revocation validate: %#v %v", c, err)
	}
	base.Payload = Payload{"targetSchema": "rai.unknown/1"}
	if _, err = Validate(base, now); err == nil {
		t.Fatal("unknown target schema accepted")
	}
	base.Payload = Payload{"targetSchema": SchemaSleepTarget}
	base.RevokesID = nil
	if _, err = Validate(base, now); err == nil {
		t.Fatal("revocation without target accepted")
	}
}

func TestEffortKindsAndAgeEstimatedColdStart(t *testing.T) {
	now := time.Now().UTC()
	for _, mode := range []string{"race", "threshold_test", "speed_test", "sprint_test"} {
		r := base(now)
		r.Kind = "workout_effort"
		r.Payload = Payload{"workoutID": "6f9619ff-8b86-4d01-b42d-00cf4fc964ff", "mode": mode, "userConfirmed": true}
		if _, err := Validate(r, now); err != nil {
			t.Fatalf("%s rejected: %v", mode, err)
		}
	}
	r := base(now)
	r.Kind = "workout_effort"
	r.Payload = Payload{"workoutID": "6f9619ff-8b86-4d01-b42d-00cf4fc964ff", "mode": "easy_run", "userConfirmed": true}
	if _, err := Validate(r, now); err == nil {
		t.Fatal("unknown effort kind accepted")
	}
	r = base(now)
	r.Kind = "hr_reference"
	r.Payload = Payload{"maxHR": 186.0, "measuredAt": now.Add(-time.Hour).Format(time.RFC3339), "method": "age_estimated", "unit": "bpm"}
	if _, err := Validate(r, now); err != nil {
		t.Fatalf("explicit age-estimated cold start rejected: %v", err)
	}
}
