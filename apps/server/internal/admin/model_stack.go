package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

func readOwnerAuthorization(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, errors.New("authorization file must be regular and mode 0600")
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 || len(b) > 64*1024 {
		return nil, errors.New("authorization file unreadable")
	}
	return b, nil
}

// ModelStackCommand is CLI-only. The default inspection omits all metric
// values, confidence numbers, identifiers and internal health/model parameters.
func ModelStackCommand(ctx context.Context, url string, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("model-stack inspect --dataset --evaluation-at | identity | promotion-plan | prepare-synthetic | prepare-real | activate-real | rollback-real")
	}
	if args[0] == "identity" {
		if len(args) != 1 {
			return errors.New("identity takes no flags")
		}
		hash, err := modelmanifest.CurrentHash()
		if err != nil {
			return errors.New("model-stack identity unavailable")
		}
		return json.NewEncoder(out).Encode(map[string]string{"stack": modelmanifest.StackID(), "manifestSHA256": hash})
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
	if args[0] == "prepare-real" {
		fs := flag.NewFlagSet("prepare-real", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		dataset := fs.String("dataset", "", "")
		authorizationFile := fs.String("authorization-file", "", "")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || !syncing.ValidID(*dataset) || *authorizationFile == "" {
			return errors.New("prepare-real requires an explicit dataset and authorization file")
		}
		authorization, err := readOwnerAuthorization(*authorizationFile)
		if err != nil {
			return err
		}
		s, err := postgres.Open(ctx, url, 2)
		if err != nil {
			return errors.New("model-stack database configuration failed")
		}
		defer s.Pool.Close()
		id, err := s.PrepareModelStackAuthorized(ctx, *dataset, authorization)
		if err != nil {
			if errors.Is(err, am.ErrOwnerAuthorizationInvalid) {
				return errors.New("model-stack prepare refused: owner_authorization_invalid")
			}
			return errors.New("model-stack prepare refused")
		}
		hash, _ := modelmanifest.CurrentHash()
		ah := sha256.Sum256(authorization)
		return json.NewEncoder(out).Encode(map[string]string{"stack": modelmanifest.StackID(), "manifestSHA256": hash, "jobID": id, "status": "pending", "ownerAuthorized": "true", "authorizationSHA256": hex.EncodeToString(ah[:])})
	}
	if args[0] == "activate-real" || args[0] == "rollback-real" {
		fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		dataset := fs.String("dataset", "", "")
		actor := fs.String("actor", "", "")
		stack := fs.String("stack", "", "")
		manifest := fs.String("manifest", "", "")
		authorizationFile := fs.String("authorization-file", "", "")
		phrase := fs.String("confirm-phrase", "", "")
		wantPhrase := "ACTIVATE-REAL-DATASET"
		if args[0] == "rollback-real" {
			wantPhrase = "ROLLBACK-REAL-DATASET"
		}
		if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || !syncing.ValidID(*dataset) || *actor == "" || len(*manifest) != 64 || *authorizationFile == "" || *phrase != wantPhrase {
			return errors.New("explicit dataset, actor, manifest, authorization file and confirm phrase required")
		}
		authorization, err := readOwnerAuthorization(*authorizationFile)
		if err != nil {
			return err
		}
		s, err := postgres.Open(ctx, url, 2)
		if err != nil {
			return errors.New("model-stack database configuration failed")
		}
		defer s.Pool.Close()
		var result postgres.AssignmentAudit
		if args[0] == "activate-real" {
			if *stack == "" {
				return errors.New("activate-real requires an explicit stack")
			}
			result, err = s.PromoteModelStackAuthorized(ctx, *dataset, *actor, *stack, *manifest, authorization)
		} else {
			result, err = s.RollbackModelStackAuthorized(ctx, *dataset, *actor, *manifest, authorization)
		}
		if err != nil {
			if errors.Is(err, am.ErrOwnerAuthorizationInvalid) {
				return errors.New("model-stack operation refused: owner_authorization_invalid")
			}
			return errors.New("model-stack operation refused")
		}
		return json.NewEncoder(out).Encode(result)
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
