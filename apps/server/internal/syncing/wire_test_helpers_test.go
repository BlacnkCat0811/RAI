package syncing

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"testing"
)

// requireSeriesError asserts that err is a protocol *Error with the given
// status and code. It replaces the helper that lived in the deleted series
// tests.
func requireSeriesError(t *testing.T, err error, status int, code string) {
	t.Helper()
	var failure *Error
	if !errors.As(err, &failure) {
		t.Fatalf("expected *Error, got %v", err)
	}
	if failure.Status != status || failure.Code != code {
		t.Fatalf("expected %d %s, got %d %s", status, code, failure.Status, failure.Code)
	}
}

// parseWire decodes a gzip-compressed NDJSON batch with the retained strict
// decoder and validates every event, mirroring the production v6 ingest
// decode path. The legacy v2 wire budget (compressed size, decoded size,
// per-line length, event count) is gone from production; tests that need the
// detail budget call ValidateDetailBudget explicitly.
func parseWire(t *testing.T, wire []byte, sampleType string) ([]Event, error) {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(wire))
	if err != nil {
		return nil, Fail(400, "body_read_failed")
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil, Fail(400, "body_read_failed")
	}
	var events []Event
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var event Event
		if err := DecodeStrictJSON(line, &event); err != nil {
			return nil, err
		}
		if err := event.Validate(sampleType); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}
