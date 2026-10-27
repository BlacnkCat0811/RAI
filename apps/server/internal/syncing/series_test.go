package syncing

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

const testSeriesType = "HKQuantityTypeIdentifierHeartRate"

func gzipSeriesLines(t *testing.T, lines ...string) []byte {
	t.Helper()
	var wire bytes.Buffer
	g := gzip.NewWriter(&wire)
	for _, line := range lines {
		if _, err := g.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	return wire.Bytes()
}

func TestSeriesManifestCrossLanguageFixture(t *testing.T) {
	b, err := os.ReadFile("../../../../contracts/fixtures/series-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Chunks []struct {
			ChunkIndex     int           `json:"chunkIndex"`
			Points         []SeriesPoint `json:"points"`
			Canonical      string        `json:"canonical"`
			ChunkSHA256    string        `json:"chunkSHA256"`
			ManifestSHA256 string        `json:"manifestSHA256"`
		} `json:"chunks"`
	}
	if err = json.Unmarshal(b, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Chunks) != 2 {
		t.Fatal("fixture must have two ordered chunks")
	}
	chain := EmptySeriesManifest()
	for i, chunk := range fixture.Chunks {
		if chunk.ChunkIndex != i {
			t.Fatal("fixture chunk order changed")
		}
		canonical, digest := SeriesPointsCanonical(chunk.Points)
		if string(canonical) != chunk.Canonical {
			t.Fatalf("canonical chunk %d differs from independent fixture:\n%s\n%s", i, canonical, chunk.Canonical)
		}
		if hex.EncodeToString(digest) != chunk.ChunkSHA256 {
			t.Fatalf("chunk %d hash differs from independent fixture", i)
		}
		chain = AdvanceSeriesManifest(chain, digest)
		if hex.EncodeToString(chain) != chunk.ManifestSHA256 {
			t.Fatalf("manifest %d differs from independent fixture", i)
		}
	}
}

func TestParseSeriesChunkPreservesOrderAndBounds(t *testing.T) {
	wire := gzipSeriesLines(t,
		`{"startUs":1793059202000000,"endUs":1793059203000000,"value":62.5,"unit":"count/min"}`,
		`{"startUs":1793059200000000,"endUs":1793059201000000,"value":60.5,"unit":"count/min"}`,
	)
	chunk, err := ParseSeriesChunk(bytes.NewReader(wire), testSeriesType, 1793059200000000, 1793059204000000)
	if err != nil {
		t.Fatal(err)
	}
	if chunk.PointCount != 2 || chunk.MinStartUs != 1793059200000000 || chunk.MaxEndUs != 1793059203000000 {
		t.Fatal("range metadata is not the source min/max")
	}
	if chunk.Points[0].StartUs != 1793059202000000 {
		t.Fatal("source order was sorted")
	}
	h := sha256.Sum256(wire)
	if chunk.WireSHA256 != hex.EncodeToString(h[:]) {
		t.Fatal("wire digest differs")
	}
	_, digest := SeriesPointsCanonical(chunk.Points)
	if chunk.PointsSHA256 != hex.EncodeToString(digest) {
		t.Fatal("point digest differs")
	}
}

func TestParseSeriesChunkRejectsUnsupportedInput(t *testing.T) {
	parentStart, parentEnd := int64(1793059200000000), int64(1793059204000000)
	cases := map[string]string{
		"unknown field":  `{"startUs":1793059200000000,"endUs":1793059201000000,"value":60,"unit":"count/min","metadata":{}}`,
		"null field":     `{"startUs":1793059200000000,"endUs":1793059201000000,"value":null,"unit":"count/min"}`,
		"duplicate key":  `{"startUs":1793059200000000,"startUs":1793059200000000,"endUs":1793059201000000,"value":60,"unit":"count/min"}`,
		"wrong unit":     `{"startUs":1793059200000000,"endUs":1793059201000000,"value":60,"unit":"ms"}`,
		"bad interval":   `{"startUs":1793059202000000,"endUs":1793059201000000,"value":60,"unit":"count/min"}`,
		"outside parent": `{"startUs":1793059199000000,"endUs":1793059201000000,"value":60,"unit":"count/min"}`,
		"missing unit":   `{"startUs":1793059200000000,"endUs":1793059201000000,"value":60}`,
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseSeriesChunk(bytes.NewReader(gzipSeriesLines(t, line)), testSeriesType, parentStart, parentEnd)
			requireSeriesError(t, err, 422, "unsupported_payload")
		})
	}
}

func TestParseSeriesChunkLimits(t *testing.T) {
	parentStart, parentEnd := int64(1793059200000000), int64(1793059204000000)
	if _, err := ParseSeriesChunk(bytes.NewReader(nil), testSeriesType, parentStart, parentEnd); err == nil {
		t.Fatal("empty body accepted")
	}
	lines := make([]string, MaxSeriesPointsPerChunk+1)
	for i := range lines {
		lines[i] = `{"startUs":1793059200000000,"endUs":1793059201000000,"value":60,"unit":"count/min"}`
	}
	_, err := ParseSeriesChunk(bytes.NewReader(gzipSeriesLines(t, lines...)), testSeriesType, parentStart, parentEnd)
	requireSeriesError(t, err, 413, "series_chunk_too_large")
	if _, err := ParseSeriesChunk(bytes.NewReader(gzipSeriesLines(t, strings.Repeat(" ", MaxLineBytes)+`{}`)), testSeriesType, parentStart, parentEnd); err == nil {
		t.Fatal("oversized line accepted")
	}
}

func requireSeriesError(t *testing.T, err error, status int, code string) {
	t.Helper()
	e, ok := err.(*Error)
	if !ok || e.Status != status || e.Code != code {
		t.Fatalf("expected %d/%s, got %#v", status, code, err)
	}
}

func TestSeriesRequestAndSealValidation(t *testing.T) {
	base := SeriesRequest{DatasetID: NewID(), DatasetEpoch: NewID(), CollectorEpoch: NewID(),
		SeriesID: NewID(), SampleType: testSeriesType, SampleUUID: NewID()}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*SeriesRequest){
		"category": func(r *SeriesRequest) { r.SampleType = "HKCategoryTypeIdentifierSleepAnalysis" },
		"workout":  func(r *SeriesRequest) { r.SampleType = WorkoutType },
		"bad ID":   func(r *SeriesRequest) { r.SeriesID = "not-a-uuid" },
	} {
		t.Run(name, func(t *testing.T) {
			req := base
			mutate(&req)
			if err := req.Validate(); err == nil {
				t.Fatal("invalid series request accepted")
			}
		})
	}
	if err := (SeriesSeal{ChunkCount: 0, TotalPoints: 1, ManifestSHA256: strings.Repeat("0", 64)}).Validate(); err == nil {
		t.Fatal("zero chunk seal accepted")
	}
	if err := (SeriesSeal{ChunkCount: 1, TotalPoints: MaxSeriesPoints + 1, ManifestSHA256: strings.Repeat("0", 64)}).Validate(); err == nil {
		t.Fatal("oversized point seal accepted")
	}
	if err := (SeriesSeal{ChunkCount: 1, TotalPoints: 1, ManifestSHA256: "ABC"}).Validate(); err == nil {
		t.Fatal("bad manifest accepted")
	}
}

func TestParseSeriesChunkMaximumPoints(t *testing.T) {
	base := int64(1793059200000000)
	lines := make([]string, MaxSeriesPointsPerChunk)
	for i := range lines {
		lines[i] = `{"startUs":` + fmt.Sprint(base+int64(i)*1000000) + `,"endUs":` + fmt.Sprint(base+int64(i+1)*1000000) + `,"value":60.5,"unit":"count/min"}`
	}
	chunk, err := ParseSeriesChunk(bytes.NewReader(gzipSeriesLines(t, lines...)), testSeriesType, base, base+int64(MaxSeriesPointsPerChunk)*1000000)
	if err != nil {
		t.Fatal(err)
	}
	if chunk.PointCount != MaxSeriesPointsPerChunk || chunk.MinStartUs != base || chunk.MaxEndUs != base+int64(MaxSeriesPointsPerChunk)*1000000 {
		t.Fatal("maximum chunk did not preserve all points")
	}
}
