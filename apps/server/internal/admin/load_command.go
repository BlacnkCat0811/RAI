package admin

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"rai/server/internal/storage/postgres"
	tl "rai/server/internal/trainingload"
)

func LoadCommand(ctx context.Context, url string, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: rai analysis load compute|inspect|state|recompute [explicit flags]")
	}
	fs := flag.NewFlagSet("load", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dataset := fs.String("dataset", "", "dataset")
	workout := fs.String("workout", "", "workout")
	operation := fs.String("operation", "", "idempotent operation UUID")
	actor := fs.String("actor", "", "audit actor")
	date := fs.String("day", "", "local day")
	from := fs.String("from", "", "local day")
	through := fs.String("through", "", "local day")
	model := fs.String("model-version", "", "model/version")
	parameter := fs.String("parameter-version", "", "HR parameter schema version")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		return errors.New("invalid load flags")
	}
	s, err := postgres.Open(ctx, url, 4)
	if err != nil {
		return errors.New("load database configuration failed")
	}
	defer s.Pool.Close()
	if err = s.Ready(ctx); err != nil {
		return errors.New("load schema not ready")
	}
	switch args[0] {
	case "compute":
		if err = QueueTrainingLoadCandidate(ctx, s, *dataset, *workout, *operation, *actor); err != nil {
			return errors.New("candidate load request failed")
		}
		return json.NewEncoder(out).Encode(map[string]string{"status": "queued", "channel": "candidate", "model": tl.Algorithm + "/" + tl.Version})
	case "inspect":
		r, id, e := s.CandidateLoad(ctx, *dataset, *workout, *date)
		if e != nil {
			return errors.New("candidate load unavailable")
		}
		return json.NewEncoder(out).Encode(map[string]any{"runID": id, "result": r})
	case "state":
		r, id, e := s.CandidateLoadState(ctx, *dataset, *date)
		if e != nil {
			return errors.New("candidate state unavailable")
		}
		return json.NewEncoder(out).Encode(map[string]any{"runID": id, "state": r})
	case "recompute":
		n, e := RecomputeTrainingLoad(ctx, s, *dataset, *from, *through, *model, *parameter)
		if e != nil {
			return errors.New("bounded load recompute request failed")
		}
		return json.NewEncoder(out).Encode(map[string]any{"status": "queued", "workouts": n, "channel": "candidate"})
	}
	return errors.New("unsupported load action")
}
