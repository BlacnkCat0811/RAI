// Command rai-research runs the synthetic-only E10 prospective research
// workflow. It reads a caller-supplied JSON study file and writes aggregate
// counts and cluster summaries; it never connects to a database, reads health
// records, exports identifiers or records a real outcome.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"rai/server/internal/research"
)

type request struct {
	Snapshot         research.ProspectiveSnapshot `json:"snapshot"`
	Outcomes         []research.Outcome           `json:"outcomes"`
	TemporalCutoff   time.Time                    `json:"temporalCutoff"`
	FreezeCutoff     *time.Time                   `json:"freezeCutoff,omitempty"`
	HoldoutFraction  float64                      `json:"holdoutFraction"`
	SplitSeed        int64                        `json:"splitSeed"`
	MinCluster       int                          `json:"minCluster"`
	WithdrawAthletes []string                     `json:"withdrawAthletes,omitempty"`
	WithdrawAt       *time.Time                   `json:"withdrawAt,omitempty"`
	Addendum         research.StudyAddendum       `json:"addendum"`
}

type report struct {
	DatasetID          string                 `json:"datasetID"`
	Model              string                 `json:"model"`
	ManifestSHA        string                 `json:"manifestSHA256"`
	SnapshotAsOf       time.Time              `json:"snapshotAsOf"`
	SnapshotKnownAt    time.Time              `json:"snapshotKnownAt"`
	Observations       int                    `json:"observations"`
	FrozenObservations int                    `json:"frozenObservations"`
	TemporalTrain      int                    `json:"temporalTrain"`
	TemporalTest       int                    `json:"temporalTest"`
	AthleteTrain       int                    `json:"athleteTrain"`
	AthleteTest        int                    `json:"athleteTest"`
	Paired             int                    `json:"paired"`
	OverlapExcluded    int                    `json:"overlapExcluded"`
	UnpairedPrediction int                    `json:"unpairedPredictions"`
	UnpairedOutcome    int                    `json:"unpairedOutcomes"`
	Withdrawn          int                    `json:"withdrawn"`
	Clusters           []research.ClusterStat `json:"clusters"`
	SuppressedClusters []string               `json:"suppressedClusters"`
	AddendumValid      bool                   `json:"addendumValid"`
	SyntheticOnly      bool                   `json:"syntheticOnly"`
}

func run(in []byte) (report, error) {
	var req request
	if err := json.Unmarshal(in, &req); err != nil {
		return report{}, fmt.Errorf("study json: %w", err)
	}
	if req.MinCluster < 1 {
		req.MinCluster = 5
	}
	snapshot, err := research.CreateSnapshot(req.Snapshot)
	if err != nil {
		return report{}, fmt.Errorf("snapshot: %w", err)
	}
	if err := research.VerifyNoFutureLeak(snapshot); err != nil {
		return report{}, fmt.Errorf("future leak: %w", err)
	}
	frozen := len(snapshot.Observations)
	if req.FreezeCutoff != nil {
		snapshot = research.FreezeKnownAt(snapshot, *req.FreezeCutoff)
		frozen = len(snapshot.Observations)
	}
	temporal := research.TemporalSplit(snapshot.Observations, req.TemporalCutoff)
	keys := make([]string, 0, len(snapshot.Observations))
	for _, p := range snapshot.Observations {
		keys = append(keys, p.AthleteKey)
	}
	athletes, err := research.AthleteSplit(keys, req.HoldoutFraction, req.SplitSeed)
	if err != nil {
		return report{}, fmt.Errorf("athlete split: %w", err)
	}
	withdrawn := 0
	if len(req.WithdrawAthletes) > 0 {
		at := req.Snapshot.KnownAt
		if req.WithdrawAt != nil {
			at = *req.WithdrawAt
		}
		snapshot, withdrawn = research.Withdraw(snapshot, req.WithdrawAthletes, at)
	}
	linked := research.LinkOutcomes(snapshot.Observations, req.Outcomes)
	clusters := research.ClusterSummary(linked.Pairs, req.MinCluster)
	validAddendum, err := research.ValidateAddendum(req.Addendum)
	if err != nil {
		return report{}, fmt.Errorf("addendum: %w", err)
	}
	return report{
		DatasetID: snapshot.DatasetID, Model: snapshot.Model, ManifestSHA: snapshot.ManifestSHA,
		SnapshotAsOf: snapshot.AsOf, SnapshotKnownAt: snapshot.KnownAt, Observations: len(snapshot.Observations), FrozenObservations: frozen,
		TemporalTrain: temporal.TrainCount, TemporalTest: temporal.TestCount,
		AthleteTrain: athletes.TrainCount, AthleteTest: athletes.TestCount,
		Paired: len(linked.Pairs), OverlapExcluded: linked.OverlapExcluded, UnpairedPrediction: len(linked.UnpairedPredictions), UnpairedOutcome: len(linked.UnpairedOutcomes),
		Withdrawn: withdrawn, Clusters: clusters.Clusters, SuppressedClusters: clusters.SuppressedClusters,
		AddendumValid: validAddendum, SyntheticOnly: true,
	}, nil
}

func runWithPseudonym(in []byte, secret []byte) (report, map[string]string, error) {
	var req request
	if err := json.Unmarshal(in, &req); err != nil {
		return report{}, nil, fmt.Errorf("study json: %w", err)
	}
	snapshot, outcomes, withdraw, mapping, err := research.Pseudonymize(req.Snapshot, req.Outcomes, req.WithdrawAthletes, secret)
	if err != nil {
		return report{}, nil, err
	}
	req.Snapshot = snapshot
	req.Outcomes = outcomes
	req.WithdrawAthletes = withdraw
	b, err := json.Marshal(req)
	if err != nil {
		return report{}, nil, err
	}
	r, err := run(b)
	return r, mapping, err
}

type pseudonymDocument struct {
	Schema  string      `json:"schema"`
	Mapping [][2]string `json:"mapping"`
}

func main() {
	input := flag.String("input", "", "path to synthetic study JSON")
	output := flag.String("out", "-", "output path or - for stdout")
	secretFile := flag.String("pseudonym-secret-file", "", "file containing at least 16 bytes of synthetic study pseudonym key")
	mapFile := flag.String("pseudonym-map", "", "separate 0600 output file for the athlete pseudonym mapping")
	flag.Parse()
	if *input == "" {
		fmt.Fprintln(os.Stderr, "rai-research: -input is required")
		os.Exit(2)
	}
	b, err := os.ReadFile(*input)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rai-research:", err)
		os.Exit(1)
	}
	var r report
	var mapping map[string]string
	if *secretFile != "" {
		secret, readErr := os.ReadFile(*secretFile)
		if readErr != nil {
			fmt.Fprintln(os.Stderr, "rai-research:", readErr)
			os.Exit(1)
		}
		secret = []byte(strings.TrimSpace(string(secret)))
		r, mapping, err = runWithPseudonym(b, secret)
	} else if *mapFile != "" {
		err = errors.New("-pseudonym-map requires -pseudonym-secret-file")
	} else {
		r, err = run(b)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "rai-research:", err)
		os.Exit(1)
	}
	if *mapFile != "" {
		doc, _ := json.MarshalIndent(pseudonymDocument{Schema: "rai.research.pseudonym_map/1", Mapping: research.SortedMapping(mapping)}, "", "  ")
		doc = append(doc, '\n')
		if err := os.WriteFile(*mapFile, doc, 0o600); err != nil {
			fmt.Fprintln(os.Stderr, "rai-research:", err)
			os.Exit(1)
		}
	}
	out, _ := json.MarshalIndent(r, "", "  ")
	out = append(out, '\n')
	if *output == "-" {
		_, err = os.Stdout.Write(out)
	} else {
		err = os.WriteFile(*output, out, 0o600)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "rai-research:", err)
		os.Exit(1)
	}
	_ = errors.New // keep errors imported for future typed diagnostics
}
