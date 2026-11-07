package admin

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"time"

	"rai/server/internal/backfill"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
)

type backfillFlags struct {
	dataset, actor, manifest, stack, from, through, asOf, view, operation, file, id, before, after string
	blockDays                                                                                      int
	today                                                                                          string
}

func parseBackfillFlags(name string, args []string) (backfillFlags, error) {
	var f backfillFlags
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&f.dataset, "dataset", "", "")
	fs.StringVar(&f.actor, "actor", "", "")
	fs.StringVar(&f.manifest, "manifest", "", "")
	fs.StringVar(&f.stack, "stack", "", "")
	fs.StringVar(&f.from, "from", "", "")
	fs.StringVar(&f.through, "through", "", "")
	fs.StringVar(&f.asOf, "as-of", "", "")
	fs.StringVar(&f.view, "view", "revised", "")
	fs.StringVar(&f.operation, "operation", "dry_run", "")
	fs.IntVar(&f.blockDays, "block-days", 31, "")
	fs.StringVar(&f.today, "today", "", "")
	fs.StringVar(&f.file, "file", "", "")
	fs.StringVar(&f.id, "id", "", "")
	fs.StringVar(&f.before, "before", "", "")
	fs.StringVar(&f.after, "after", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return f, errors.New("invalid backfill flags")
	}
	return f, nil
}

func (f backfillFlags) request() backfill.Request {
	return backfill.Request{DatasetID: f.dataset, ManifestSHA: f.manifest, StackID: f.stack, From: f.from, Through: f.through, AsOf: f.asOf, View: backfill.View(f.view), Operation: backfill.Operation(f.operation), Actor: f.actor, BlockDays: f.blockDays}
}

func (f backfillFlags) plan() (backfill.Request, backfill.PlanResult, error) {
	req := f.request()
	today := f.today
	if today == "" {
		today = time.Now().In(time.Local).Format(backfill.DayLayout)
	}
	p, err := backfill.Plan(req, backfill.Facts{Today: today})
	return req, p, err
}

// BackfillCommand is CLI-only. plan is a pure read; execute records only a
// plan and schedules the first bounded block. Workers own publication.
func BackfillCommand(ctx context.Context, url string, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("backfill plan|execute|resume|status|cancel|diff")
	}
	action := args[0]
	f, err := parseBackfillFlags(action, args[1:])
	if err != nil {
		return err
	}
	if action == "diff" && f.file != "" {
		b, e := os.ReadFile(f.file)
		if e != nil {
			return errors.New("backfill diff file unreadable")
		}
		var pair struct {
			Before backfill.Snapshot `json:"before"`
			After  backfill.Snapshot `json:"after"`
		}
		if json.Unmarshal(b, &pair) != nil {
			return errors.New("backfill diff file invalid")
		}
		return json.NewEncoder(out).Encode(backfill.Diff(pair.Before, pair.After))
	}
	if !syncing.ValidID(f.dataset) || f.actor == "" {
		return errors.New("explicit dataset and actor required")
	}
	if action == "plan" {
		req, p, e := f.plan()
		if e != nil {
			return errors.New("backfill plan refused")
		}
		hashes := make([]string, len(p.Blocks))
		for i, b := range p.Blocks {
			hashes[i] = b.InputHash
		}
		return json.NewEncoder(out).Encode(map[string]any{"outputDays": p.OutputDays, "blocks": len(p.Blocks), "blockDays": req.BlockDays, "inputHashes": hashes, "warmup": p.Warmup, "wrote": false})
	}
	if action == "execute" {
		if f.operation == "dry_run" {
			f.operation = "backfill"
		}
		req, p, e := f.plan()
		if e != nil {
			return errors.New("backfill execute refused")
		}
		s, e := postgres.Open(ctx, url, 2)
		if e != nil {
			return errors.New("backfill database configuration failed")
		}
		defer s.Pool.Close()
		op, e := s.CreateBackfillOperation(ctx, f.dataset, f.actor, req, p)
		if e != nil {
			return errors.New("backfill execute refused")
		}
		if _, e = s.ResumeBackfillOperation(ctx, f.dataset, op.ID); e != nil {
			return errors.New("backfill schedule refused")
		}
		op, e = s.ReadBackfillOperation(ctx, f.dataset, op.ID)
		if e != nil {
			return errors.New("backfill status unavailable")
		}
		return json.NewEncoder(out).Encode(op)
	}
	s, e := postgres.Open(ctx, url, 2)
	if e != nil {
		return errors.New("backfill database configuration failed")
	}
	defer s.Pool.Close()
	switch action {
	case "diff":
		changes, e := s.DiffBackfillOperations(ctx, f.dataset, f.before, f.after)
		if e != nil {
			return errors.New("backfill diff requires two completed operations for this synthetic dataset")
		}
		return json.NewEncoder(out).Encode(changes)
	case "status":
		op, e := s.ReadBackfillOperation(ctx, f.dataset, f.id)
		if e != nil {
			return errors.New("backfill status not found")
		}
		return json.NewEncoder(out).Encode(op)
	case "resume":
		remaining, e := s.ResumeBackfillOperation(ctx, f.dataset, f.id)
		if e != nil {
			return errors.New("backfill resume refused")
		}
		status := "running"
		if len(remaining) == 0 {
			status = "candidate_ready"
		}
		return json.NewEncoder(out).Encode(map[string]any{"remainingBlocks": len(remaining), "status": status})
	case "cancel":
		op, e := s.CancelBackfillOperation(ctx, f.dataset, f.id)
		if e != nil {
			return errors.New("backfill cancel refused")
		}
		return json.NewEncoder(out).Encode(op)
	default:
		return errors.New("unsupported backfill action")
	}
}
