package modelinput

import (
	"encoding/json"
	"testing"
	"time"
)

func TestHRV3StrictPatchValidation(t *testing.T) {
	now := time.Now()
	from := now.Add(-time.Hour).Format(time.RFC3339)
	cases := []struct {
		raw   string
		valid bool
	}{
		{`{"maxHR":{"value":190,"basis":"user_setting","method":"user_entered","unit":"bpm"}}`, true},
		{`{"restingHR":{"mode":"automatic","manualValue":null}}`, true},
		{`{"maxHR":{"value":190,"basis":"measured","method":"user_entered","unit":"bpm"}}`, false},
		{`{"restingHR":{"mode":"manual","manualValue":null}}`, false},
		{`{"restingHR":{"mode":"automatic"}}`, false},
		{`{"maxHR":{"value":190,"basis":"user_setting","method":"user_entered","unit":"bpm","unknown":1}}`, false},
	}
	for _, tc := range cases {
		var p Payload
		if err := json.Unmarshal([]byte(tc.raw), &p); err != nil {
			t.Fatal(err)
		}
		r := Request{ID: "cb2359e5-c95f-4721-81ea-829bd8d1691a", SchemaVersion: SchemaHRReferenceV3, Kind: "hr_reference", SubjectKey: "athlete", EffectiveFrom: from, Provenance: "synthetic", Payload: p}
		c, err := Validate(r, now)
		if (err == nil) != tc.valid {
			t.Fatalf("valid=%t err=%v", tc.valid, err)
		}
		if err == nil && c.Schema != SchemaHRReferenceV3 {
			t.Fatal("schema was downgraded")
		}
	}
}
