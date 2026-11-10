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
	w := gzip.NewWriter(&b)
	if _, err := w.Write(raw); err != nil {
		t.Fatalf("gzip: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	h := sha256.Sum256(b.Bytes())
	return Envelope{
		ProtocolVersion:  ProtocolVersion,
		DatasetID:        NewID(),
		DatasetEpoch:     NewID(),
		CollectorEpoch:   NewID(),
		StreamID:         NewID(),
		BatchID:          NewID(),
		ClientSeq:        1,
		PageID:           NewID(),
		FragmentCount:    1,
		QueryFingerprint: "sha256:" + strings.Repeat("a", 64),
		ContentEncoding:  "gzip",
		WireBytes:        b.Len(),
		DecodedBytes:     len(raw),
		EventCount:       count,
		PayloadSHA256:    hex.EncodeToString(h[:]),
	}, b.Bytes()
}

func assertCode(t *testing.T, err error, want string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("want error code %q, got %v", want, err)
	}
	if e.Code != want {
		t.Fatalf("want error code %q, got %q", want, e.Code)
	}
}

func line(id string) string {
	return `{"op":"upsert","kind":"sample","type":"` + sampleType + `","uuid":"` + id + `","startUs":1793059200000001,"endUs":1793059200000001,"quantity":{"value":132,"unit":"count/min"},"source":{"bundleID":"synthetic"},"metadata":{"b":2,"a":1}}`
}

func TestProtocolRoundTrip(t *testing.T) {
	e, _ := payload(t, []byte(line(NewID())+"\n"), 1)
	if err := e.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	canonical, hash := e.Canonical()
	var decoded Envelope
	if err := DecodeStrictJSON(canonical, &decoded); err != nil {
		t.Fatalf("decode canonical: %v", err)
	}
	canonical2, hash2 := decoded.Canonical()
	if !bytes.Equal(canonical, canonical2) {
		t.Fatalf("canonical changed across decode:\n%s\n%s", canonical, canonical2)
	}
	if !bytes.Equal(hash, hash2) {
		t.Fatal("envelope hash changed across decode")
	}
}

func TestStrictJSON(t *testing.T) {
	base := line(NewID())
	cases := map[string]string{
		"unknown":    strings.Replace(base, `"metadata"`, `"unknown":1,"metadata"`, 1),
		"duplicate":  strings.Replace(base, `"op":"upsert"`, `"op":"upsert","op":"upsert"`, 1),
		"case_alias": strings.Replace(base, `"op"`, `"Op"`, 1),
		"uuid_alias": strings.Replace(base, `"uuid"`, `"UUID"`, 1),
		"trailing":   base + `{}`,
		"array":      `[` + base + `]`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			var ev Event
			assertCode(t, DecodeStrictJSON([]byte(raw), &ev), "unsupported_payload")
		})
	}
}

func TestDeleteWinsAndConflicts(t *testing.T) {
	id := NewID()
	var a, b Event
	if err := DecodeStrictJSON([]byte(line(id)), &a); err != nil {
		t.Fatalf("decode a: %v", err)
	}
	if err := DecodeStrictJSON([]byte(line(id)), &b); err != nil {
		t.Fatalf("decode b: %v", err)
	}
	b.Quantity.Value = 140
	del := Event{Op: "delete", Kind: "sample", Type: sampleType, UUID: id}
	for _, order := range [][]Event{{a, b, del}, {del, a, b}, {b, del, a}} {
		got, err := Collapse(order)
		if err != nil {
			t.Fatalf("collapse: %v", err)
		}
		if len(got) != 1 || got[0].Op != "delete" || got[0].UUID != id {
			t.Fatalf("delete must win, got %#v", got)
		}
	}
	_, err := Collapse([]Event{a, b})
	assertCode(t, err, "sample_identity_conflict")
}

func TestImmutableEnvelope(t *testing.T) {
	e, _ := payload(t, []byte(line(NewID())+"\n"), 1)
	canonical, hash := e.Canonical()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(canonical, &fields); err != nil {
		t.Fatalf("unmarshal canonical: %v", err)
	}
	fields["ProtocolVersion"] = fields["protocolVersion"]
	delete(fields, "protocolVersion")
	mutated, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal mutated: %v", err)
	}
	var out Envelope
	assertCode(t, DecodeStrictJSON(mutated, &out), "unsupported_payload")

	e.FragmentCount = 2
	_, changed := e.Canonical()
	if bytes.Equal(hash, changed) {
		t.Fatal("fragment count must change envelope identity")
	}
}

func TestUnsupportedCapabilities(t *testing.T) {
	start := int64(1793059200000001)
	ev := Event{
		Op: "upsert", Kind: "sample", Type: sampleType, UUID: NewID(),
		StartUs: &start, EndUs: &start,
		Quantity: &Quantity{Value: 132, Unit: "Hz"},
		Source:   &Source{BundleID: "synthetic"},
	}
	assertCode(t, ev.Validate(sampleType), "unsupported_unit")

	ev.Quantity.Unit = Units[sampleType]
	ev.Type = "HKQuantityTypeIdentifierNotSupported"
	if err := ev.Validate(sampleType); err == nil {
		t.Fatal("unsupported sample type must be rejected")
	}
}

func TestCrossLanguageFixture(t *testing.T) {
	root := "../../../../contracts/fixtures/"
	raw, err := os.ReadFile(root + "envelope.json")
	if err != nil {
		t.Fatalf("read envelope fixture: %v", err)
	}
	var e Envelope
	if err := DecodeStrictJSON(raw, &e); err != nil {
		t.Fatalf("decode envelope fixture: %v", err)
	}
	canonical, hash := e.Canonical()
	want, err := os.ReadFile(root + "envelope.canonical.json")
	if err != nil {
		t.Fatalf("read canonical fixture: %v", err)
	}
	if !bytes.Equal(canonical, want) {
		t.Fatalf("canonical mismatch:\n got %s\nwant %s", canonical, want)
	}
	sum, err := os.ReadFile(root + "envelope.sha256")
	if err != nil {
		t.Fatalf("read sha fixture: %v", err)
	}
	if got, want := hex.EncodeToString(hash), strings.TrimSpace(string(sum)); got != want {
		t.Fatalf("sha mismatch: got %s want %s", got, want)
	}
}
