package syncing

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

const sampleType = "HKQuantityTypeIdentifierHeartRate"

func payload(t *testing.T, raw []byte, count int) (Envelope, []byte) {
	t.Helper()
	var b bytes.Buffer
	g := gzip.NewWriter(&b)
	if _, err := g.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b.Bytes())
	return Envelope{ProtocolVersion: 2, DatasetID: NewID(), DatasetEpoch: NewID(), CollectorEpoch: NewID(), StreamID: NewID(), BatchID: NewID(), ClientSeq: 1, PageID: NewID(), FragmentCount: 1, QueryFingerprint: "sha256:" + strings.Repeat("a", 64), ContentEncoding: "gzip", WireBytes: b.Len(), DecodedBytes: len(raw), EventCount: count, PayloadSHA256: hex.EncodeToString(h[:])}, b.Bytes()
}
func assertCode(t *testing.T, err error, want string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != want {
		t.Fatalf("wanted %s got %v", want, err)
	}
}
func line(id string) string {
	return `{"op":"upsert","kind":"sample","type":"` + sampleType + `","uuid":"` + id + `","startUs":1793059200000001,"endUs":1793059200000001,"quantity":{"value":132,"unit":"count/min"},"source":{"bundleID":"synthetic"},"metadata":{"b":2,"a":1}}`
}
func TestProtocolRoundTrip(t *testing.T) {
	e, b := payload(t, []byte(line(NewID())+"\n"), 1)
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	events, err := ParseBody(bytes.NewReader(b), e, sampleType)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || *events[0].StartUs != 1793059200000001 {
		t.Fatal("microseconds lost")
	}
	canonical, hash := e.Canonical()
	var decoded Envelope
	if err := DecodeJSON(canonical, &decoded); err != nil {
		t.Fatal(err)
	}
	again, h := decoded.Canonical()
	if !bytes.Equal(canonical, again) || !bytes.Equal(hash, h) {
		t.Fatal("unstable identity")
	}
}
func TestStrictJSON(t *testing.T) {
	base := line(NewID())
	for name, bad := range map[string]string{
		"unknown":        strings.Replace(base, `"op":`, `"surprise":true,"op":`, 1),
		"duplicate":      strings.Replace(base, `"value":132`, `"value":131,"value":132`, 1),
		"case_alias":     strings.Replace(base, `"value":132`, `"value":132,"Value":131`, 1),
		"top_case_alias": strings.Replace(base, `"op":`, `"Kind":"workout","op":`, 1),
		"missing_value":  strings.Replace(base, `"value":132,`, ``, 1),
		"null_value":     strings.Replace(base, `"value":132`, `"value":null`, 1),
		"trailing":       base + `{}`,
		"array":          `[]`,
	} {
		t.Run(name, func(t *testing.T) {
			e, b := payload(t, []byte(bad), 1)
			_, err := ParseBody(bytes.NewReader(b), e, sampleType)
			assertCode(t, err, "unsupported_payload")
		})
	}
}
func TestBodyBudgets(t *testing.T) {
	t.Run("wire", func(t *testing.T) {
		e, _ := payload(t, nil, 0)
		_, err := ParseBody(strings.NewReader(strings.Repeat("x", MaxWireBytes+1)), e, sampleType)
		assertCode(t, err, "batch_too_large")
	})
	t.Run("decoded", func(t *testing.T) {
		e, b := payload(t, []byte(strings.Repeat("x", MaxDecodedBytes+1)), 1)
		_, err := ParseBody(bytes.NewReader(b), e, sampleType)
		assertCode(t, err, "batch_too_large")
	})
	t.Run("hash", func(t *testing.T) {
		e, b := payload(t, []byte(line(NewID())), 1)
		b[0] ^= 1
		_, err := ParseBody(bytes.NewReader(b), e, sampleType)
		assertCode(t, err, "payload_hash_mismatch")
	})
	t.Run("line", func(t *testing.T) {
		e, b := payload(t, []byte(strings.Repeat("x", MaxLineBytes+1)), 1)
		_, err := ParseBody(bytes.NewReader(b), e, sampleType)
		assertCode(t, err, "event_too_large")
	})
	t.Run("count", func(t *testing.T) {
		e, b := payload(t, []byte(line(NewID())), 2)
		_, err := ParseBody(bytes.NewReader(b), e, sampleType)
		assertCode(t, err, "event_count_mismatch")
	})
	t.Run("empty", func(t *testing.T) {
		e, b := payload(t, nil, 0)
		events, err := ParseBody(bytes.NewReader(b), e, sampleType)
		if err != nil || len(events) != 0 {
			t.Fatal(err)
		}
	})
	t.Run("gzip_crc", func(t *testing.T) {
		e, b := payload(t, []byte(line(NewID())), 1)
		b[len(b)-1] ^= 1
		h := sha256.Sum256(b)
		e.PayloadSHA256 = hex.EncodeToString(h[:])
		_, err := ParseBody(bytes.NewReader(b), e, sampleType)
		assertCode(t, err, "unsupported_payload")
	})
}
func TestDeleteWinsAndConflicts(t *testing.T) {
	id := NewID()
	var a Event
	if err := DecodeJSON([]byte(line(id)), &a); err != nil {
		t.Fatal(err)
	}
	b := a
	b.Quantity = &Quantity{Value: 140, Unit: "count/min"}
	del := Event{Op: "delete", Kind: "sample", Type: sampleType, UUID: id}
	for _, events := range [][]Event{{a, b, del}, {del, a, b}, {b, del, a}} {
		out, err := Collapse(events)
		if err != nil || len(out) != 1 || out[0].Op != "delete" {
			t.Fatalf("delete did not win: %v", err)
		}
	}
	_, err := Collapse([]Event{a, b})
	assertCode(t, err, "sample_identity_conflict")
}
func TestImmutableEnvelope(t *testing.T) {
	e, _ := payload(t, nil, 0)
	_, original := e.Canonical()
	var fields map[string]json.RawMessage
	b, _ := json.Marshal(e)
	_ = json.Unmarshal(b, &fields)
	delete(fields, "fragmentIndex")
	b, _ = json.Marshal(fields)
	var missing Envelope
	assertCode(t, DecodeJSON(b, &missing), "unsupported_payload")
	e.FragmentCount = 2
	_, changed := e.Canonical()
	if bytes.Equal(original, changed) {
		t.Fatal("fragment identity not bound")
	}
}
func TestUnsupportedCapabilities(t *testing.T) {
	for name, test := range map[string]struct{ line, code string }{
		"kind":   {strings.Replace(line(NewID()), `"kind":"sample"`, `"kind":"workout"`, 1), "unsupported_payload"},
		"unit":   {strings.Replace(line(NewID()), `"unit":"count/min"`, `"unit":"Hz"`, 1), "unsupported_unit"},
		"series": {strings.Replace(line(NewID()), sampleType, "HKDataTypeIdentifierHeartbeatSeries", 1), "unsupported_payload"},
	} {
		t.Run(name, func(t *testing.T) {
			e, b := payload(t, []byte(test.line), 1)
			_, err := ParseBody(bytes.NewReader(b), e, sampleType)
			assertCode(t, err, test.code)
		})
	}
}

func TestCrossLanguageFixture(t *testing.T) {
	root := "../../../../contracts/fixtures/"
	read := func(name string) []byte {
		t.Helper()
		b, err := os.ReadFile(root + name)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	var e Envelope
	if err := DecodeJSON(read("envelope.json"), &e); err != nil {
		t.Fatal(err)
	}
	canonical, hash := e.Canonical()
	if !bytes.Equal(canonical, read("envelope.canonical.json")) || hex.EncodeToString(hash) != strings.TrimSpace(string(read("envelope.sha256"))) {
		t.Fatal("canonical fixture drift")
	}
	events, err := ParseBody(bytes.NewReader(read("sample.ndjson.gz")), e, sampleType)
	if err != nil || len(events) != 1 {
		t.Fatal(err)
	}
}
