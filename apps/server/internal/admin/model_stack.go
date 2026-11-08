package admin

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"time"

	am "rai/server/internal/athletemodel"
	"rai/server/internal/modelmanifest"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
)

// ModelStackCommand is CLI-only. The default inspection omits all metric
// values, confidence numbers, identifiers and internal health/model parameters.
func ModelStackCommand(ctx context.Context, url string, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("model-stack inspect --dataset --evaluation-at | promotion-plan")
	}
	if args[0] == "promotion-plan" {
		if len(args) != 1 {
			return errors.New("promotion-plan takes no flags")
		}
		return json.NewEncoder(out).Encode(am.PromotionPlan())
	}
	if args[0] == "prepare-synthetic" {
		fs := flag.NewFlagSet("prepare-synthetic", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		dataset := fs.String("dataset", "", "")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || !syncing.ValidID(*dataset) {
			return errors.New("prepare-synthetic requires an explicit test dataset")
		}
		s, err := postgres.Open(ctx, url, 2)
		if err != nil {
			return errors.New("model-stack database configuration failed")
		}
		defer s.Pool.Close()
		id, err := s.PrepareModelStack(ctx, *dataset)
		if err != nil {
			return errors.New("model-stack prepare refused")
		}
		hash, _ := modelmanifest.CurrentHash()
		return json.NewEncoder(out).Encode(map[string]string{"stack": modelmanifest.StackID(), "manifestSHA256": hash, "jobID": id, "status": "pending"})
	}
	if args[0] == "promote" || args[0] == "rollback" {
		fs := flag.NewFlagSet("model-stack", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		dataset := fs.String("dataset", "", "")
		actor := fs.String("actor", "", "")
		stack := fs.String("stack", "", "")
		manifest := fs.String("manifest", "", "")
		gateFile := fs.String("gate-file", "", "")
		synthetic := fs.Bool("synthetic", false, "")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || !syncing.ValidID(*dataset) || *actor == "" || len(*manifest) != 64 {
			return errors.New("explicit dataset, actor, manifest and stack/gate required")
		}
		s, e := postgres.Open(ctx, url, 2)
		if e != nil {
			return errors.New("model-stack database configuration failed")
		}
		defer s.Pool.Close()
		if args[0] == "rollback" {
			r, e := s.RollbackModelStack(ctx, *dataset, *actor, *manifest)
			if e != nil {
				return errors.New("model-stack rollback refused")
			}
			return json.NewEncoder(out).Encode(r)
		}
		if *stack == "" || *gateFile == "" || !*synthetic {
			return errors.New("synthetic promote requires stack, gate-file and --synthetic")
		}
		b, e := os.ReadFile(*gateFile)
		if e != nil {
			return errors.New("gate file unreadable")
		}
		var gate am.PromotionGate
		if json.Unmarshal(b, &gate) != nil {
			return errors.New("gate file invalid")
		}
		r, e := s.PromoteModelStack(ctx, *dataset, *actor, *stack, gate, *manifest)
		if e != nil {
			return errors.New("model-stack promote refused")
		}
		return json.NewEncoder(out).Encode(r)
	}
	if args[0] != "inspect" {
		return errors.New("unsupported model-stack action")
	}
	fs := flag.NewFlagSet("model-stack", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dataset := fs.String("dataset", "", "")
	evaluation := fs.String("evaluation-at", "", "")
	synthetic := fs.Bool("synthetic-test-values", false, "explicit synthetic fixture values only")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || !syncing.ValidID(*dataset) {
		return errors.New("explicit dataset and evaluation-at required")
	}
	at, e := time.Parse(time.RFC3339Nano, *evaluation)
	if e != nil {
		return errors.New("explicit RFC3339 evaluation-at required")
	}
	s, e := postgres.Open(ctx, url, 2)
	if e != nil {
		return errors.New("model-stack database configuration failed")
	}
	defer s.Pool.Close()
	r, stats, e := s.ReadAthleteModelCandidateSnapshot(ctx, *dataset, at)
	if e != nil {
		return errors.New("model-stack technical read failed")
	}
	if *synthetic {
		return json.NewEncoder(out).Encode(r)
	}
	type summary struct {
		Metric         string                `json:"metric"`
		ModelVersion   string                `json:"modelVersion"`
		State          am.ProductMetricState `json:"state"`
		ModelStatus    string                `json:"modelStatus"`
		Quality        string                `json:"quality"`
		Freshness      string                `json:"freshness"`
		Reasons        []string              `json:"reasonCodes"`
		TechnicalError *string               `json:"technicalError"`
	}
	envelopes := []am.Envelope{r.Snapshot.TrainingReadiness.Envelope, r.Snapshot.PhysiologicalRecovery.Envelope, r.Snapshot.TrainingLoad.Envelope, r.Snapshot.RunningAbility.Envelope}
	if r.Snapshot.RAIVO2Max != nil {
		envelopes = append(envelopes, r.Snapshot.RAIVO2Max.Envelope)
	}
	envelopes = append(envelopes, r.Snapshot.ExternalVO2Max.Envelope)
	metrics := []summary{}
	for _, v := range envelopes {
		metrics = append(metrics, summary{v.Metric, v.ModelVersion, v.State, v.ModelStatus, v.Quality, v.Freshness, v.ReasonCodes, v.TechnicalError})
	}
	return json.NewEncoder(out).Encode(struct {
		CandidateOnly bool      `json:"candidateOnly"`
		ResponseState string    `json:"responseState"`
		Metrics       []summary `json:"metrics"`
		DBReads       int       `json:"dbReads"`
	}{true, r.Snapshot.ResponseState, metrics, stats.DBReads})
}
