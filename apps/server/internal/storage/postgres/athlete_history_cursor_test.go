package postgres

import "testing"

func TestHistoryCursorRoundTripAndRejectsMalformed(t *testing.T) {
	in := historyCursor{DatasetID: "d", Metric: "trainingLoad", StackID: "v2", AssignmentVersion: 3, View: "revised", From: "2026-01-01", To: "2026-02-01", Date: "2026-01-20"}
	out, err := decodeHistoryCursor(encodeHistoryCursor(in))
	if err != nil || out != in {
		t.Fatalf("round trip: %#v %v", out, err)
	}
	if _, err = decodeHistoryCursor("not-base64"); err == nil {
		t.Fatal("malformed cursor accepted")
	}
}
