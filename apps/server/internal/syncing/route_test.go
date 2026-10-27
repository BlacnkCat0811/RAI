package syncing

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func routeWire(t *testing.T, lines ...string) []byte {
	t.Helper()
	var decoded bytes.Buffer
	for _, line := range lines {
		if !strings.HasSuffix(line, "\n") {
			line += "\n"
		}
		decoded.WriteString(line)
	}
	var wire bytes.Buffer
	g := gzip.NewWriter(&wire)
	if _, err := g.Write(decoded.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	return wire.Bytes()
}

func TestParseRouteChunkStrictAndCanonical(t *testing.T) {
	line := `{"sequence":0,"recordedAtUs":1793059200000000,"latitude":31.2,"longitude":121.4,"altitude":4.5,"course":91.2}`
	chunk, err := ParseRouteChunk(bytes.NewReader(routeWire(t, line)))
	if err != nil || len(chunk.Points) != 1 || chunk.WireSHA256 == "" || chunk.PointsSHA256 == "" {
		t.Fatal("valid route chunk rejected", err)
	}
	for name, bad := range map[string]string{
		"unknown field": `{"sequence":0,"recordedAtUs":1793059200000000,"latitude":31.2,"longitude":121.4,"extra":1}`,
		"null optional": `{"sequence":0,"recordedAtUs":1793059200000000,"latitude":31.2,"longitude":121.4,"altitude":null}`,
		"missing lat":   `{"sequence":0,"recordedAtUs":1793059200000000,"longitude":121.4}`,
		"bad latitude":  `{"sequence":0,"recordedAtUs":1793059200000000,"latitude":91,"longitude":121.4}`,
	} {
		if _, err := ParseRouteChunk(bytes.NewReader(routeWire(t, bad))); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	canonical, digest := RoutePointsCanonical(chunk.Points)
	if len(canonical) == 0 || len(digest) != 32 {
		t.Fatal("canonical route digest missing")
	}
}

func TestRouteManifestAndSealValidation(t *testing.T) {
	good := RouteManifest{ProtocolVersion: ProtocolVersion, DatasetID: NewID(), DatasetEpoch: NewID(),
		CollectorEpoch: NewID(), RouteID: NewID(), WorkoutID: NewID(), Source: &Source{BundleID: "synthetic"},
		ExpectedChunkCount: 1, ExpectedPointCount: 1, FirstRecordedAtUs: 1, LastRecordedAtUs: 1}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := good
	bad.ProtocolVersion = 99
	requireSyncError(t, bad.Validate(), "unsupported_protocol")
	bad = good
	bad.ExpectedPointCount = 0
	requireSyncError(t, bad.Validate(), "unsupported_payload")
	seal := RouteSeal{ProtocolVersion: ProtocolVersion, ChunkCount: 1, TotalPoints: 1,
		ManifestSHA256: strings.Repeat("a", 64), PayloadManifestSHA256: strings.Repeat("b", 64)}
	if err := seal.Validate(); err != nil {
		t.Fatal(err)
	}
	seal.ProtocolVersion = 1
	requireSyncError(t, seal.Validate(), "unsupported_protocol")
}

func TestAssociationAndEnrichmentValidation(t *testing.T) {
	association := AssociationRequest{ProtocolVersion: ProtocolVersion, DatasetID: NewID(), DatasetEpoch: NewID(),
		CollectorEpoch: NewID(), AssociationID: NewID(), SampleType: sampleType, SampleUUID: NewID(),
		WorkoutUUID: NewID(), Source: &Source{BundleID: "synthetic"}}
	if err := association.Validate(); err != nil {
		t.Fatal(err)
	}
	association.SampleType = "HKQuantityTypeIdentifierUnknown"
	if err := association.Validate(); err == nil {
		t.Fatal("unknown association sample type accepted")
	}
	value := 10.0
	enrichment := WorkoutEnrichmentRequest{ProtocolVersion: ProtocolVersion, DatasetID: NewID(), DatasetEpoch: NewID(),
		CollectorEpoch: NewID(), EnrichmentID: NewID(), WorkoutID: NewID(), Source: &Source{BundleID: "synthetic"},
		SourceVersion: 1, ObservedAtUs: 1, TotalDistanceM: &value}
	if err := enrichment.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(enrichment); err != nil {
		t.Fatal(err)
	}
	enrichment.TotalDistanceM = nil
	if err := enrichment.Validate(); err == nil {
		t.Fatal("empty enrichment accepted")
	}
}

func requireSyncError(t *testing.T, err error, code string) {
	t.Helper()
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}
