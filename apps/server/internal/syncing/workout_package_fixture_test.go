package syncing

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func TestWorkoutPackageManifestCrossLanguageFixture(t *testing.T) {
	b, err := os.ReadFile("../../../../contracts/fixtures/workout-package-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Chunks []struct {
			ChunkIndex     int             `json:"chunkIndex"`
			Details        []WorkoutDetail `json:"details"`
			Canonical      string          `json:"canonical"`
			DetailsSHA256  string          `json:"detailsSHA256"`
			ManifestSHA256 string          `json:"manifestSHA256"`
		} `json:"chunks"`
	}
	if err = json.Unmarshal(b, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Chunks) != 2 {
		t.Fatal("fixture must have two ordered chunks")
	}
	chain := EmptyWorkoutPackageManifest()
	for i, chunk := range fixture.Chunks {
		if chunk.ChunkIndex != i {
			t.Fatal("fixture chunk order changed")
		}
		canonical, digest := WorkoutDetailsCanonical(chunk.Details)
		if string(canonical) != chunk.Canonical {
			t.Fatalf("canonical chunk %d differs from independent fixture:\n%s\n%s", i, canonical, chunk.Canonical)
		}
		if hex.EncodeToString(digest) != chunk.DetailsSHA256 {
			t.Fatalf("chunk %d hash differs from independent fixture", i)
		}
		chain = AdvanceWorkoutPackageManifest(chain, digest)
		if hex.EncodeToString(chain) != chunk.ManifestSHA256 {
			t.Fatalf("manifest %d differs from independent fixture", i)
		}
	}
}
