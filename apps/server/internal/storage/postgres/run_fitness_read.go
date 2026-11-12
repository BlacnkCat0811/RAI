package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	am "rai/server/internal/athletemodel"
	"rai/server/internal/modelmanifest"
	"rai/server/internal/modelrelease"
	rf "rai/server/internal/runfitness"
	"rai/server/internal/syncing"
	"rai/server/internal/workoutdetail"

	"github.com/jackc/pgx/v5"
)

type fitnessQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// publishedFitnessDays reads the clean published running-fitness states of the
// active stack for the given days, validated against the frozen release.
func publishedFitnessDays(ctx context.Context, q fitnessQuerier, dataset, stack, tz string, days []string) (map[string]rf.State, map[string]publishedModelArtifact, error) {
	states := map[string]rf.State{}
	arts := map[string]publishedModelArtifact{}
	if len(days) == 0 {
		return states, arts, nil
	}
	scopes := make([]string, len(days))
	for i, d := range days {
		scopes[i] = modelmanifest.DayScope(stack, d)
	}
	rows, err := q.Query(ctx, `SELECT p.day::text,a.payload,encode(a.content_sha256,'hex'),a.schema_version,r.algorithm_name,r.algorithm_version,r.target_generation,r.id::text,p.published_at,r.finished_at,r.lineage->>'manifestSHA256'
 FROM analysis_publications p
 JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id AND r.scope_key=p.scope_key AND r.target_generation=p.generation AND r.status='SUCCEEDED'
 JOIN analysis_published_artifacts a ON a.dataset_id=r.dataset_id AND a.run_id=r.id AND a.artifact_key=$3
 JOIN dirty_scopes d ON d.dataset_id=p.dataset_id AND d.scope_key=p.scope_key AND d.generation=p.generation AND d.clean_generation=p.generation
 WHERE p.dataset_id=$1 AND p.scope_key=ANY($2) ORDER BY p.day LIMIT 400`, dataset, scopes, rf.ArtifactKey)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var day string
		var a publishedModelArtifact
		if err := rows.Scan(&day, &a.Payload, &a.ContentHash, &a.Schema, &a.Algorithm, &a.Version, &a.Generation, &a.RunID, &a.PublishedAt, &a.FinishedAt, &a.ManifestSHA); err != nil {
			return nil, nil, err
		}
		var s rf.State
		if !validHistoryArtifactForStack("runningFitness", dataset, tz, stack, a) || json.Unmarshal(a.Payload, &s) != nil || s.Date != day {
			return nil, nil, ErrModelProjection
		}
		states[day], arts[day] = s, a
	}
	return states, arts, rows.Err()
}

type activeFitness struct {
	stack, manifest, tz string
	release             modelrelease.Release
}

func activeFitnessRelease(ctx context.Context, q fitnessQuerier, dataset string) (activeFitness, error) {
	var a activeFitness
	err := q.QueryRow(ctx, `SELECT m.stack_id,encode(m.config_sha256,'hex'),d.timezone FROM analysis_model_channels m JOIN datasets d ON d.id=m.dataset_id
 WHERE m.dataset_id=$1 AND m.channel='active' AND d.retired_at IS NULL`, dataset).Scan(&a.stack, &a.manifest, &a.tz)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (a.stack == "" || a.stack == "legacy") {
		return a, am.ErrNotActivated
	}
	if err != nil {
		return a, err
	}
	release, ok := modelrelease.Lookup(a.stack, a.manifest, a.tz)
	if !ok || release.Manifest.ProductSchema != am.Schema4 {
		return a, am.ErrUnsupportedRelease
	}
	a.release = release
	return a, nil
}

// loadRunFitnessDetail attaches the single result published on the run's
// local day and the fixed-policy reference of the day before it. A run whose
// day has not been (re)published yet simply has no runFitness block.
func loadRunFitnessDetail(ctx context.Context, tx pgx.Tx, p syncing.Principal, id string, d *workoutDetailData) error {
	act, err := activeFitnessRelease(ctx, tx, p.DatasetID)
	if errors.Is(err, am.ErrNotActivated) || errors.Is(err, am.ErrUnsupportedRelease) {
		return nil
	}
	if err != nil {
		return err
	}
	loc, err := time.LoadLocation(act.tz)
	if err != nil {
		return ErrModelProjection
	}
	day := d.Start.In(loc).Format(time.DateOnly)
	before := d.Start.In(loc).AddDate(0, 0, -1).Format(time.DateOnly)
	states, _, err := publishedFitnessDays(ctx, tx, p.DatasetID, act.stack, act.tz, []string{before, day})
	if err != nil {
		return err
	}
	s, ok := states[day]
	if !ok {
		return nil
	}
	for _, single := range s.Singles {
		if single.WorkoutID != strings.ToLower(id) {
			continue
		}
		out := &workoutdetail.RunFitness{Single: single, PublishedDate: day, AlgorithmVersion: rf.Version, RatingPolicy: rf.RatingPolicy}
		if prev, ok := states[before]; ok && prev.ReferenceDailyScore != nil {
			out.ReferenceDailyScore, out.ReferenceDate = prev.ReferenceDailyScore, before
			if single.HR != nil {
				delta := single.HR.Score - *prev.ReferenceDailyScore
				out.DeltaToReference = &delta
			}
		}
		d.RunFitness = out
		return nil
	}
	return nil
}

// ReadRunningFitnessHistory returns the revised history (current clean
// publications of past days) or the published history (the last result
// published for each day before that day ended, from immutable runs).
// "as_known" (inputs frozen at the time, replayed) is not reconstructible:
// facts, labels and parameters keep no knownAt version history, so it
// reports historical_input_unavailable instead of an invented past.
func (s *Store) ReadRunningFitnessHistory(ctx context.Context, p syncing.Principal, from, to, view, cursor string, limit int) (am.RunningFitnessHistory, error) {
	out := am.RunningFitnessHistory{SchemaVersion: rf.Schema, View: view, AlgorithmVersion: rf.Version, RatingPolicy: rf.RatingPolicy, Days: []am.RunningFitnessDay{}}
	if view != "revised" && view != "published" {
		return out, am.ErrHistoricalInputUnavailable
	}
	if limit < 1 || limit > 90 {
		return out, am.ErrHistoryCursor
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	act, err := activeFitnessRelease(ctx, tx, p.DatasetID)
	if err != nil {
		return out, err
	}
	loc, err := time.LoadLocation(act.tz)
	if err != nil {
		return out, ErrModelProjection
	}
	today := time.Now().In(loc).Format(time.DateOnly)
	if to == "" || to > today {
		to = today
	}
	last, err := time.Parse(time.DateOnly, to)
	if err != nil {
		return out, am.ErrHistoryCursor
	}
	if from == "" {
		from = last.AddDate(0, 0, -89).Format(time.DateOnly)
	}
	first, err := time.Parse(time.DateOnly, from)
	if err != nil || last.Before(first) || last.Sub(first) > 365*24*time.Hour {
		return out, am.ErrHistoryCursor
	}
	out.StackID, out.ManifestSHA, out.From, out.To = act.stack, act.manifest, from, to
	end := last
	if cursor != "" {
		c, err := decodeHistoryCursor(cursor)
		if err != nil || c.DatasetID != p.DatasetID || c.Metric != "runningFitness" || c.StackID != act.stack || c.View != view || c.From != from || c.To != to || c.Date < from || c.Date > to {
			return out, am.ErrHistoryCursor
		}
		cd, _ := time.Parse(time.DateOnly, c.Date)
		end = cd.AddDate(0, 0, -1)
	}
	var days []string
	for d := end; !d.Before(first) && len(days) < limit; d = d.AddDate(0, 0, -1) {
		days = append(days, d.Format(time.DateOnly))
	}
	states, arts, err := publishedFitnessDays(ctx, tx, p.DatasetID, act.stack, act.tz, days)
	if view == "published" {
		states, arts, err = publishedAtTheTime(ctx, tx, p.DatasetID, act.stack, act.tz, days)
	}
	if err != nil {
		return out, err
	}
	for _, d := range days {
		st, ok := states[d]
		if !ok {
			continue // missing days stay empty, never interpolated
		}
		var singles []rf.SingleBrief
		for _, b := range st.Observations {
			if b.Day == d {
				singles = append(singles, b)
			}
		}
		if singles == nil {
			singles = []rf.SingleBrief{}
		}
		a := arts[d]
		out.Days = append(out.Days, am.RunningFitnessDay{Date: d, Status: st.Status, Freshness: st.Freshness, Overall: st.Overall, ReferenceDailyScore: st.ReferenceDailyScore,
			Params: st.Params, Singles: singles, PublicationID: a.RunID, PublishedAt: a.PublishedAt.UnixMicro(), ConfigHash: st.ConfigHash})
	}
	if len(days) == limit {
		lastDay := days[len(days)-1]
		if lastDay > from {
			next := encodeHistoryCursor(historyCursor{DatasetID: p.DatasetID, Metric: "runningFitness", StackID: act.stack, View: view, From: from, To: to, Date: lastDay})
			out.NextCursor = &next
		}
	}
	return out, tx.Commit(ctx)
}

func (s *Store) ReadRunningFitnessEvidence(ctx context.Context, p syncing.Principal, date, dimension string) (am.RunningFitnessEvidence, error) {
	out := am.RunningFitnessEvidence{SchemaVersion: rf.Schema, Date: date, Dimension: dimension, Evidence: []rf.EvidenceRef{}}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	act, err := activeFitnessRelease(ctx, tx, p.DatasetID)
	if err != nil {
		return out, err
	}
	if date == "" {
		loc, err := time.LoadLocation(act.tz)
		if err != nil {
			return out, ErrModelProjection
		}
		date = time.Now().In(loc).Format(time.DateOnly)
		out.Date = date
	}
	states, arts, err := publishedFitnessDays(ctx, tx, p.DatasetID, act.stack, act.tz, []string{date})
	if err != nil {
		return out, err
	}
	st, ok := states[date]
	if !ok {
		return out, am.ErrRunningFitnessNotFound
	}
	for _, e := range st.Evidence {
		if dimension == "" || e.Dimension == dimension {
			out.Evidence = append(out.Evidence, e)
		}
	}
	out.Dimensions, out.Support, out.PublicationID = st.Dimensions, st.Support, arts[date].RunID
	return out, tx.Commit(ctx)
}

// RequestRunFitnessReanalysis records an idempotent request and invalidates
// the bounded affected range in the same transaction.
func (s *Store) RequestRunFitnessReanalysis(ctx context.Context, p syncing.Principal, key string, req am.ReanalysisRequest) (am.ReanalysisOperation, error) {
	if !syncing.ValidID(p.DatasetID) || key == "" || len(key) > 128 || req.View != "revised" || req.ReferencePolicy != "current_effective" || req.Algorithm != rf.Version ||
		len(req.WorkoutIDs) > 100 || len(req.WorkoutIDs) > 0 && (req.From != "" || req.To != "") || len(req.WorkoutIDs) == 0 && (req.From == "" || req.To == "") {
		return am.ReanalysisOperation{}, am.ErrReanalysisInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return am.ReanalysisOperation{}, err
	}
	defer tx.Rollback(ctx)
	act, err := activeFitnessRelease(ctx, tx, p.DatasetID)
	if err != nil {
		return am.ReanalysisOperation{}, err
	}
	loc, err := time.LoadLocation(act.tz)
	if err != nil {
		return am.ReanalysisOperation{}, ErrModelProjection
	}
	from, to := req.From, req.To
	if len(req.WorkoutIDs) > 0 {
		ids := make([]string, len(req.WorkoutIDs))
		for i, id := range req.WorkoutIDs {
			if !syncing.ValidID(id) {
				return am.ReanalysisOperation{}, am.ErrReanalysisInvalid
			}
			ids[i] = strings.ToLower(id)
		}
		slices.Sort(ids)
		ids = slices.Compact(ids)
		var minStart, maxStart time.Time
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*),COALESCE(min(event_start),'epoch'),COALESCE(max(event_start),'epoch') FROM workouts
 WHERE dataset_id=$1 AND workout_uuid::text=ANY($2) AND activity_type=37`, p.DatasetID, ids).Scan(&n, &minStart, &maxStart); err != nil {
			return am.ReanalysisOperation{}, err
		}
		if n != len(ids) {
			return am.ReanalysisOperation{}, am.ErrReanalysisInvalid
		}
		req.WorkoutIDs = ids
		from, to = minStart.In(loc).Format(time.DateOnly), maxStart.In(loc).Format(time.DateOnly)
	}
	if _, e1 := time.Parse(time.DateOnly, from); e1 != nil {
		return am.ReanalysisOperation{}, am.ErrReanalysisInvalid
	}
	if _, e2 := time.Parse(time.DateOnly, to); e2 != nil {
		return am.ReanalysisOperation{}, am.ErrReanalysisInvalid
	}
	body, _ := json.Marshal(req)
	digest := sha256.Sum256(body)
	var id string
	err = tx.QueryRow(ctx, `SELECT rai_run_fitness_reanalysis($1,$2,$3,$4,$5,$6::date,$7::date,$8,$9)::text`,
		p.DatasetID, syncing.NewID(), key, digest[:], json.RawMessage(body), from, to, req.ReferencePolicy, req.Algorithm).Scan(&id)
	if err != nil {
		switch {
		case strings.Contains(err.Error(), "reanalysis_idempotency_conflict"):
			return am.ReanalysisOperation{}, am.ErrReanalysisConflict
		case strings.Contains(err.Error(), "reanalysis_range_invalid"):
			return am.ReanalysisOperation{}, am.ErrReanalysisInvalid
		case strings.Contains(err.Error(), "reanalysis_not_activated"):
			return am.ReanalysisOperation{}, am.ErrNotActivated
		}
		return am.ReanalysisOperation{}, err
	}
	op, err := readReanalysis(ctx, tx, p.DatasetID, id)
	if err != nil {
		return op, err
	}
	return op, tx.Commit(ctx)
}

func (s *Store) ReadRunFitnessReanalysis(ctx context.Context, p syncing.Principal, id string) (am.ReanalysisOperation, error) {
	if !syncing.ValidID(id) {
		return am.ReanalysisOperation{}, am.ErrRunningFitnessNotFound
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return am.ReanalysisOperation{}, err
	}
	defer tx.Rollback(ctx)
	op, err := readReanalysis(ctx, tx, p.DatasetID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return op, am.ErrRunningFitnessNotFound
	}
	if err != nil {
		return op, err
	}
	return op, tx.Commit(ctx)
}

// readReanalysis derives status from the recorded target generations: a day
// is complete once its clean generation reached the target, failed when the
// job for that target failed. Nothing is reported complete by the request.
func readReanalysis(ctx context.Context, q fitnessQuerier, dataset, id string) (am.ReanalysisOperation, error) {
	op := am.ReanalysisOperation{ID: id}
	var from, through time.Time
	var created time.Time
	var targets []byte
	err := q.QueryRow(ctx, `SELECT o.view,o.stack_id,o.algorithm_version,o.reference_policy,o.from_day,o.through_day,o.created_at,o.targets
 FROM run_fitness_reanalysis_operations o WHERE o.dataset_id=$1 AND o.id=$2`, dataset, id).Scan(&op.View, &op.StackID, &op.AlgorithmVersion, &op.ReferencePolicy, &from, &through, &created, &targets)
	if err != nil {
		return op, err
	}
	op.From, op.Through, op.CreatedAt = from.Format(time.DateOnly), through.Format(time.DateOnly), created.UnixMicro()
	var running, queued int
	err = q.QueryRow(ctx, `SELECT count(*),
 count(*) FILTER (WHERE d.clean_generation>=t.target),
 count(*) FILTER (WHERE d.clean_generation<t.target AND EXISTS(SELECT 1 FROM jobs j WHERE j.dataset_id=d.dataset_id AND j.scope_key=d.scope_key AND j.target_generation>=t.target AND j.kind='analysis.model_stack_v2' AND j.state='failed')),
 count(*) FILTER (WHERE d.clean_generation<t.target AND EXISTS(SELECT 1 FROM jobs j WHERE j.dataset_id=d.dataset_id AND j.scope_key=d.scope_key AND j.target_generation>=t.target AND j.kind='analysis.model_stack_v2' AND j.state='running')),
 count(*) FILTER (WHERE d.clean_generation<t.target AND EXISTS(SELECT 1 FROM jobs j WHERE j.dataset_id=d.dataset_id AND j.scope_key=d.scope_key AND j.target_generation>=t.target AND j.kind='analysis.model_stack_v2' AND j.state='queued'))
 FROM jsonb_each_text($2::jsonb) t0(scope,target_text)
 CROSS JOIN LATERAL (SELECT t0.scope,t0.target_text::bigint AS target) t
 JOIN dirty_scopes d ON d.dataset_id=$1 AND d.scope_key=t.scope`, dataset, targets).Scan(&op.TotalDays, &op.CompletedDays, &op.FailedDays, &running, &queued)
	if err != nil {
		return op, err
	}
	switch {
	case op.FailedDays > 0:
		op.Status, op.ErrorCode = "failed", "day_release_failed"
	case op.CompletedDays == op.TotalDays:
		op.Status = "completed"
	case running > 0 || op.CompletedDays > 0:
		op.Status = "running"
	default:
		op.Status = "queued"
	}
	return op, nil
}

// publishedAtTheTime reads, per day, the last running-fitness run of the
// active stack that finished before that local day ended: what the product
// published for the day at the time, whether or not it was later replaced.
func publishedAtTheTime(ctx context.Context, q fitnessQuerier, dataset, stack, tz string, days []string) (map[string]rf.State, map[string]publishedModelArtifact, error) {
	states := map[string]rf.State{}
	arts := map[string]publishedModelArtifact{}
	if len(days) == 0 {
		return states, arts, nil
	}
	scopes := make([]string, len(days))
	for i, d := range days {
		scopes[i] = modelmanifest.DayScope(stack, d)
	}
	rows, err := q.Query(ctx, `SELECT DISTINCT ON (v.scope_key) right(v.scope_key,10),v.payload,encode(v.content_sha256,'hex'),v.schema_version,v.algorithm_name,v.algorithm_version,v.target_generation,v.run_id::text,v.finished_at,v.manifest_sha256
 FROM run_fitness_day_runs v
 WHERE v.dataset_id=$1 AND v.scope_key=ANY($2) AND v.finished_at < ((right(v.scope_key,10)::date+1)::timestamp AT TIME ZONE $3)
 ORDER BY v.scope_key,v.finished_at DESC,v.run_id LIMIT 400`, dataset, scopes, tz)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var day string
		var a publishedModelArtifact
		var finished time.Time
		if err := rows.Scan(&day, &a.Payload, &a.ContentHash, &a.Schema, &a.Algorithm, &a.Version, &a.Generation, &a.RunID, &finished, &a.ManifestSHA); err != nil {
			return nil, nil, err
		}
		a.PublishedAt, a.FinishedAt = finished, &finished
		var s rf.State
		if !validHistoryArtifactForStack("runningFitness", dataset, tz, stack, a) || json.Unmarshal(a.Payload, &s) != nil || s.Date != day {
			return nil, nil, ErrModelProjection
		}
		states[day], arts[day] = s, a
	}
	return states, arts, rows.Err()
}
