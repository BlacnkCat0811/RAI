package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"rai/server/internal/analysis"
	rd "rai/server/internal/readiness"
	rc "rai/server/internal/recovery"
	"rai/server/internal/storage/postgres/dbgen"
	"rai/server/internal/syncing"
	tl "rai/server/internal/trainingload"
	wf "rai/server/internal/workoutfeatures"
)

type readinessAssignment struct {
	Hash     string      `json:"configHash"`
	ID       string      `json:"assignmentID"`
	Timezone string      `json:"timezone"`
	Scope    string      `json:"scope"`
	Coverage rd.Coverage `json:"syntheticCoverage"`
}

func (s *Store) loadReadinessInput(ctx context.Context, tx pgx.Tx, l Lease, in *analysis.Snapshot, asOf time.Time) error {
	var raw []byte
	var tz, epoch, collector, stack, stackConfig string
	var stackVersion int64
	var capture time.Time
	var valid bool
	e := tx.QueryRow(ctx, `SELECT d.timezone,d.epoch::text,COALESCE(c.collector_epoch::text,''),clock_timestamp(),m.extension_configs->$2,m.stack_id,encode(m.config_sha256,'hex'),m.assignment_version,
 NOT EXISTS(SELECT 1 FROM collector_leases c LEFT JOIN devices v ON v.dataset_id=c.dataset_id AND v.id=c.device_id WHERE c.dataset_id=d.id AND (v.id IS NULL OR NOT v.can_collect OR v.revoked_at IS NOT NULL))
 FROM datasets d JOIN analysis_model_channels m ON m.dataset_id=d.id AND m.channel='candidate' LEFT JOIN collector_leases c ON c.dataset_id=d.id WHERE d.id=$1 AND d.retired_at IS NULL`, l.DatasetID, rd.Model).Scan(&tz, &epoch, &collector, &capture, &raw, &stack, &stackConfig, &stackVersion, &valid)
	if e != nil {
		return e
	}
	var a readinessAssignment
	if !valid {
		return &analysis.Failure{Code: "analysis_dependency_pending", Retryable: true}
	}
	if json.Unmarshal(raw, &a) != nil || a.Timezone != tz || a.Hash != rd.DefaultConfig(tz).Hash() || !syncing.ValidID(a.ID) || a.Scope != rd.Scope(tz) || l.ScopeKey != a.Scope {
		return analysis.Invalid()
	}
	loc, e := time.LoadLocation(tz)
	if e != nil {
		return analysis.Invalid()
	}
	at := asOf.In(loc)
	today := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, loc)
	x := &rd.ReadinessSnapshot{LoadAssignment: rd.Hash([]any{stack, stackConfig, stackVersion}), Dataset: l.DatasetID, Epoch: epoch, Collector: collector, Assignment: a.ID, Generation: in.Generation, AsOf: asOf.UnixMicro(), CapturedAt: capture.UnixMicro(), Config: rd.DefaultConfig(tz), Loads: []rd.WorkoutLoad{}, Days: []rd.Day{}}
	for d := today.AddDate(0, 0, -x.Config.BaselineDays); !d.After(today); d = d.AddDate(0, 0, 1) {
		x.Days = append(x.Days, rd.Day{Date: d.Format("2006-01-02"), Start: d.UnixMicro(), End: d.AddDate(0, 0, 1).UnixMicro()})
	}
	x.Coverage = a.Coverage
	var manifest []byte
	if e = tx.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_array(id,query_fingerprint,last_client_seq,last_batch_id) ORDER BY id),'[]'::jsonb) FROM (SELECT id,query_fingerprint,last_client_seq,last_batch_id FROM sync_streams WHERE dataset_id=$1 AND collector_epoch::text=$2 AND sample_type='HKWorkoutTypeIdentifier' ORDER BY id LIMIT 129) bounded_streams`, l.DatasetID, collector).Scan(&manifest); e != nil {
		return e
	}
	var streams any
	if json.Unmarshal(manifest, &streams) != nil {
		return analysis.Invalid()
	}
	// Preserve visibly synthetic isolated model fixtures; real transport evidence
	// is accepted only from the immutable, server-validated control plane below.
	if a, ok := streams.([]any); ok && len(a) > 128 {
		return &analysis.Failure{Code: "analysis_input_limit"}
	}
	x.Coverage.CaughtUp = x.Coverage.CaughtUp && x.Coverage.Synthetic && x.Coverage.StreamHash == rd.Hash(streams)
	if coverage, found, err := verifiedWorkoutCompleteness(ctx, tx, l.DatasetID, epoch, collector); err != nil {
		return err
	} else if found {
		x.Coverage = coverage
	}
	if x.Coverage.Through > x.AsOf {
		x.Coverage.CaughtUp = false
		x.Coverage.Through = x.AsOf
	}
	var rb []byte
	var rh, rid string
	var rg int64
	e = tx.QueryRow(ctx, `SELECT a.payload,encode(a.content_sha256,'hex'),p.published_run_id::text,p.generation FROM analysis_publications p JOIN dirty_scopes d ON d.dataset_id=p.dataset_id AND d.scope_key=p.scope_key AND d.generation=p.generation AND d.clean_generation=d.generation JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id AND r.status='SUCCEEDED' JOIN analysis_artifacts a ON a.dataset_id=r.dataset_id AND a.run_id=r.id AND a.artifact_key='physiological_recovery' WHERE p.dataset_id=$1 AND p.scope_key=$2 AND p.metric_family=$3 AND p.day=$4::date`, l.DatasetID, rc.Scope(tz, today.Format("2006-01-02")), rc.Family, today.Format("2006-01-02")).Scan(&rb, &rh, &rid, &rg)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	if e == nil {
		var r rc.Result
		if json.Unmarshal(rb, &r) != nil || rc.Hash(r) != rh || r.ModelVersion != rc.Model || r.Dataset != l.DatasetID || r.DatasetEpoch != epoch || r.Collector != collector || r.Timezone != tz || r.Generation != rg {
			return analysis.Invalid()
		}
		if r.AsOf <= x.AsOf {
			fresh := int64(253402300799999999)
			for _, f := range []rc.Freshness{r.SDNNFreshness, r.RHRFreshness, r.SleepFreshness} {
				if f.LastEvent == nil {
					fresh = 0
				} else {
					fresh = min(fresh, *f.LastEvent+f.LimitUs)
				}
			}
			for _, c := range r.Coverage {
				if !c.Complete {
					fresh = 0
				}
				fresh = min(fresh, c.Through+rc.DefaultConfig(tz).CoverageFreshUs)
			}
			aged, err := rc.Freshen(r, x.AsOf)
			if err != nil {
				return err
			}
			x.Recovery = &rd.RecoveryEvidence{Model: rc.Model, RunID: rid, Hash: rh, Generation: rg, AsOf: r.AsOf, FreshUntil: fresh, Status: aged.Status, Quality: aged.Quality, Score: aged.Score, Confidence: aged.Confidence, Coverage: aged.ComponentCoverage}
		}
	}
	in.From = time.UnixMicro(x.AsOf - x.Config.LookbackUs).UTC()
	in.Through = today.AddDate(0, 0, 1).UTC()
	rows, e := dbgen.New(tx).ReadinessWorkouts(ctx, dbgen.ReadinessWorkoutsParams{DatasetID: l.DatasetID, WorkoutUuid: tl.Scope(""), EventEnd: pgtype.Timestamptz{Time: in.From, Valid: true}, EventEnd_2: pgtype.Timestamptz{Time: in.Through, Valid: true}})
	if e != nil {
		return e
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if seen[row.WWorkoutUuid] {
			continue
		}
		seen[row.WWorkoutUuid] = true
		if len(x.Loads) >= rd.MaxLoads {
			return &analysis.Failure{Code: "analysis_input_limit"}
		}
		w := rd.WorkoutLoad{ID: row.WWorkoutUuid, End: row.EventEnd.Time.UnixMicro(), FactHash: row.FactHash, RunID: row.LoadRun, Hash: row.LoadHash, Quality: "insufficient", Days: []rd.DailyLoad{}}
		if len(row.Payload) > 0 {
			var load tl.Result
			if json.Unmarshal(row.Payload, &load) != nil || tl.Hash(load) != row.LoadHash || load.DatasetID != l.DatasetID || load.WorkoutID != w.ID {
				return analysis.Invalid()
			}
			w.Model = load.Model + "/" + load.ModelVersion
			w.ConfigHash = load.ConfigHash
			w.AU = load.Load
			w.Quality = load.Quality
			w.Compatible = stack == wf.StackID() && stackConfig == wf.DefaultPolicy().Hash() && w.Model == rd.LoadModel && load.Schema == tl.Schema && load.Unit == tl.Unit && load.Scope == tl.ScopeName && load.ConfigHash == tl.ConfigHash() && load.Evidence["calendarIdentity"] == tl.CalendarIdentity(loc, row.EventStart.Time, row.EventEnd.Time)
			w.Conflict = !w.Compatible
			for _, reason := range load.ReasonCodes {
				if reason == "parameter_conflict" {
					w.Compatible = false
					w.Conflict = true
				}
			}
			if load.HRReference != nil {
				w.ParameterID = load.HRReference.ID
			}
			// Consume the formal immutable M2 daily ledger, never reconstruct load.
			var ledger []byte
			if e = tx.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_object('day',local_date::text,'load',contribution,'quality',quality) ORDER BY local_date),'[]'::jsonb) FROM analysis_load_ledger WHERE dataset_id=$1 AND run_id=$2 AND workout_uuid=$3 AND model_version=$4`, l.DatasetID, w.RunID, w.ID, rd.LoadModel).Scan(&ledger); e != nil {
				return e
			}
			if json.Unmarshal(ledger, &w.Days) != nil {
				return analysis.Invalid()
			}
			expected := []rd.DailyLoad{}
			for _, d := range load.Days {
				expected = append(expected, rd.DailyLoad{Day: d.Day, AU: d.Load, Quality: d.Quality})
			}
			if rd.Hash(expected) != rd.Hash(w.Days) {
				return analysis.Invalid()
			}
		} else {
			// A known Workout lacking a published ledger makes its completion day
			// unknown. Never silently omit it or turn it into a zero-load day.
			for _, day := range x.Days {
				if row.EventStart.Time.UnixMicro() < day.End && row.EventEnd.Time.UnixMicro() >= day.Start {
					w.Days = append(w.Days, rd.DailyLoad{Day: day.Date, Quality: "insufficient"})
				}
			}
		}
		x.Loads = append(x.Loads, w)
	}
	*x = rd.Canonical(*x)
	in.ReadinessInput = x
	in.InputVersion = rd.InputSchema
	return nil
}
