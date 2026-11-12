package postgres

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"rai/server/internal/modelrelease"
	rd "rai/server/internal/readiness"
)

// These synthetic payloads and hashes were emitted by the actual caca7f4 and
// 975b912 Go types, before JSONB reordered their fields. They catch accidental
// changes in serialization as well as current-config validation regressions.
func TestRetainedReleasePayloadsSurviveJSONBAndRejectIdentityCorruption(t *testing.T) {
	files, err := filepath.Glob("testdata/model-releases/*.json")
	if err != nil || len(files) != 2 {
		t.Fatal("retained fixtures missing", err)
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			b, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var fixture struct {
				Manifest  string                                                               `json:"manifest"`
				Artifacts []struct{ Metric, Schema, Algorithm, Version, Hash, Payload string } `json:"artifacts"`
			}
			if err = json.Unmarshal(b, &fixture); err != nil {
				t.Fatal(err)
			}
			stack := "rai-v2-" + fixture.Manifest[:16]
			release, ok := modelrelease.Lookup(stack, fixture.Manifest, "UTC")
			if !ok {
				t.Fatal("release missing")
			}
			for _, value := range fixture.Artifacts {
				a := publishedModelArtifact{Payload: jsonbRoundTrip(t, []byte(value.Payload)), Schema: value.Schema,
					Algorithm: value.Algorithm, Version: value.Version, ContentHash: value.Hash, Generation: 1, ManifestSHA: fixture.Manifest}
				const dataset = "00000000-0000-4000-8000-000000000001"
				if value.Metric == "runningAbility" {
					// The retired v60 ability is retained as an audit row only.
					if validHistoryArtifactForRelease(value.Metric, dataset, "UTC", a, release) {
						t.Fatal("retired running ability is still served")
					}
					continue
				}
				if !validHistoryArtifactForRelease(value.Metric, dataset, "UTC", a, release) {
					t.Fatalf("retained %s rejected", value.Metric)
				}
				bad := a
				bad.ContentHash = "corrupt"
				if validHistoryArtifactForRelease(value.Metric, dataset, "UTC", bad, release) {
					t.Fatal("bad content accepted")
				}
				bad = a
				bad.Generation++
				if validHistoryArtifactForRelease(value.Metric, dataset, "UTC", bad, release) {
					t.Fatal("bad generation accepted")
				}
				bad = a
				bad.ManifestSHA = "corrupt"
				if validHistoryArtifactForRelease(value.Metric, dataset, "UTC", bad, release) {
					t.Fatal("bad manifest accepted")
				}
				if validHistoryArtifactForRelease(value.Metric, "different-dataset", "UTC", a, release) {
					t.Fatal("other dataset accepted")
				}
				if value.Metric == "trainingReadiness" {
					var anchor rd.Result
					if err := json.Unmarshal(a.Payload, &anchor); err != nil {
						t.Fatal(err)
					}
					fresh, err := rd.FreshenV3ForConfig(anchor, anchor.AsOf+1, release.ReadinessConfig)
					if err != nil || fresh.Score == nil || *fresh.Score != 80 {
						t.Fatal("old readiness changed", err)
					}
					expired, err := rd.FreshenV3ForConfig(anchor, anchor.ValidBefore, release.ReadinessConfig)
					if err != nil || expired.Score != nil {
						t.Fatal("old readiness survived validity boundary", err)
					}
				}
			}
		})
	}
}
