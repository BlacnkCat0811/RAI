//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/modelinput"
	"rai/server/internal/modelmanifest"
	rd "rai/server/internal/readiness"
	rc "rai/server/internal/recovery"
	"rai/server/internal/syncing"
	tl "rai/server/internal/trainingload"
	wf "rai/server/internal/workoutfeatures"
)

// stackRun builds a protocol-6 running Workout with Apple Watch style 5 s HR
// intervals; the first one starts 3 s after the Workout, as on a real Watch.
// version is the Watch software version reported by the source.
func stackRun(t *testing.T, f *fixture, workoutID string, start time.Time, version string, hr func(i int) float64) []byte {
	t.Helper()
	const seconds = 30 * 60
	samples := []string{}
	for i, at := 0, 3; at+5 <= seconds; i, at = i+1, at+5 {
		samples = append(samples, fmt.Sprintf("[%d,%d,%g]", int64(at)*1_000_000, int64(at+5)*1_000_000, hr(i)))
	}
	source := fmt.Sprintf(`{"bundleID":"com.apple.health","version":%q,"productType":"Watch7,1","operatingSystemVersion":%q}`, version, version)
	return []byte(fmt.Sprintf(`{
		"protocolVersion":6,
		"datasetID":%q,"datasetEpoch":%q,"collectorEpoch":%q,"workoutID":%q,
		"startUs":%d,"endUs":%d,
		"source":%s,
		"metadata":{"HKTimeZone":"Asia/Shanghai","HKIndoorWorkout":false},
		"workout":{"activityType":37,"durationUs":%d},
		"streams":[{"type":%q,"unit":"count/min","status":"complete","samples":[%s]}],
		"routes":[]
	}`, f.p.DatasetID, f.enrollment.DatasetEpoch, f.enrollment.CollectorEpoch, workoutID,
		start.UnixMicro(), start.Add(seconds*time.Second).UnixMicro(), source, int64(seconds)*1_000_000,
		v6HRType, strings.Join(samples, ",")))
}

type stackDayRun struct {
	id      string
	day     int
	version string
	hr      func(int) float64
}

type stackDayOptions struct {
	corrupt      string // Workout whose stored facts are made unreadable
	maxOnly      bool   // reference without an explicit resting HR
	dailyRHRDays int    // synthetic daily resting-HR facts for the last N days
	dailyRHRAges []int  // sparse history, independent of the last seven days
	// seed runs inside the dataset before the stack is prepared (e.g. to
	// publish an earlier day's state the release must read).
	seed func(today time.Time)
	// payloads build whole protocol-6 Workouts by run id instead of stackRun.
	payloads map[string]func(f *fixture, id string, start time.Time) []byte
}

// runStackDay ingests the runs, applies the options, prepares the synthetic v2
// stack and processes today's release. It returns the reader for today's
// published artifacts.
func runStackDay(t *testing.T, f *fixture, runs []stackDayRun, opts stackDayOptions) (time.Time, func(string, any)) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.s.Pool.Exec(ctx, `UPDATE datasets SET synthetic_test=true WHERE id=$1`, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	measured := today.AddDate(0, 0, -90).Format(time.RFC3339)
	// Entered today, effective and measured 90 days ago.
	payload := modelinput.Payload{"maxHR": 190.0, "restingHR": 50.0, "measuredAt": measured, "restingMeasuredAt": measured, "method": "user_entered", "unit": "bpm"}
	if opts.maxOnly {
		delete(payload, "restingHR")
		delete(payload, "restingMeasuredAt")
	}
	if _, err := f.s.CommitModelInput(ctx, f.p, modelinput.Request{ID: syncing.NewID(), Kind: "hr_reference", SubjectKey: "athlete",
		EffectiveFrom: measured, Provenance: "synthetic integration reference", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	for k := 1; k <= opts.dailyRHRDays; k++ {
		at := today.AddDate(0, 0, -k).Add(6 * time.Hour)
		if _, err := f.s.Pool.Exec(ctx, `INSERT INTO health_samples(dataset_id,sample_type,sample_uuid,event_start,event_end,value_numeric,unit,source_bundle_id,content_sha256,entity_revision,metadata)
 VALUES($1,'HKQuantityTypeIdentifierRestingHeartRate',$2::uuid,$3,$3,$4,'count/min','com.apple.health',sha256(convert_to($2::text,'UTF8')),1,
 '{"RAI:dailySource":{"bundleID":"com.apple.health","productType":"Watch7,1","version":"11.0"}}')`,
			f.p.DatasetID, syncing.NewID(), at, 48+float64(k%5)); err != nil {
			t.Fatal(err)
		}
	}
	if len(opts.dailyRHRAges) > 0 {
		// Use the real daily transaction so both the fact revisions and the
		// dataset watermark exist even when there have been no Workouts.
		start := now.AddDate(0, 0, -90).UnixMicro()
		batch := syncing.DailyBatch{Version: 1, DatasetID: f.enrollment.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch,
			StreamID: syncing.NewID(), BatchID: syncing.NewID(), ClientSeq: 1, SampleType: syncing.DailyRHR,
			QueryFingerprint: syncing.DailyFingerprint(syncing.DailyRHR, start), BootstrapStartUs: start, ObservedAtUs: now.UnixMicro(), DeletedUUIDs: []string{}}
		for _, age := range opts.dailyRHRAges {
			at := today.AddDate(0, 0, -age).Add(6 * time.Hour).UnixMicro()
			batch.Facts = append(batch.Facts, syncing.DailyFact{UUID: syncing.NewID(), StartUs: at, EndUs: at,
				Quantity: &syncing.Quantity{Value: 50, Unit: "count/min"}, Source: syncing.HealthKitSource{BundleID: "com.apple.health", ProductType: "Watch7,1", Version: "11.0"}})
		}
		raw, err := json.Marshal(batch)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := commitDaily(t, f, raw); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range runs {
		start := today.AddDate(0, 0, r.day).Add(8 * time.Hour)
		if build := opts.payloads[r.id]; build != nil {
			acceptIngest(t, f, f.p, build(f, r.id, start))
			continue
		}
		acceptIngest(t, f, f.p, stackRun(t, f, r.id, start, r.version, r.hr))
	}
	for _, o := range processAllIngests(t, f.s) {
		if o.Status != "committed" {
			t.Fatalf("ingest outcome %+v", o)
		}
	}
	if opts.corrupt != "" {
		if _, err := f.s.Pool.Exec(ctx, `UPDATE workouts SET payload='{}'::jsonb WHERE dataset_id=$1 AND workout_uuid=$2`, f.p.DatasetID, opts.corrupt); err != nil {
			t.Fatal(err)
		}
	}
	if opts.seed != nil {
		opts.seed(today)
	}
	if _, err := f.s.PrepareModelStack(ctx, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	succeeded := 0
	for i := 0; i < 10; i++ {
		l, err := f.s.Claim(ctx, []string{modelmanifest.StackJobKind})
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		status, err := f.s.ProcessModelStack(ctx, l, time.Now())
		if err != nil {
			t.Fatalf("day release failed: %v", err)
		}
		if status == "SUCCEEDED" {
			succeeded++
		}
	}
	if succeeded == 0 {
		t.Fatal("no day release succeeded")
	}
	scope := modelmanifest.DayScope(modelmanifest.StackID(), today.Format(time.DateOnly))
	return today, func(key string, v any) {
		t.Helper()
		var b []byte
		if err := f.s.Pool.QueryRow(ctx, `SELECT a.payload FROM analysis_publications p
 JOIN analysis_artifacts a ON a.dataset_id=p.dataset_id AND a.run_id=p.published_run_id AND a.artifact_key=$3
 WHERE p.dataset_id=$1 AND p.scope_key=$2 AND p.metric_family IN ($4,$5)`, f.p.DatasetID, scope, key, "model_product_v2/"+key, "model_product_v3/"+key).Scan(&b); err != nil {
			t.Fatalf("artifact %s: %v", key, err)
		}
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatal(err)
		}
	}
}

func steadyHR(int) float64 { return 150 }

func TestModelStackDayRecoveryWithoutNightHealth(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ages  []int
		basis string
	}{
		{"bootstrap", nil, "user_entered"},
		{"sparse_history", []int{30, 45, 60}, "recent_mean"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			_, artifact := runStackDay(t, f, nil, stackDayOptions{dailyRHRAges: tc.ages})
			var recovery rc.V4Result
			artifact("physiological_recovery", &recovery)
			if recovery.Schema != rc.SchemaV4 || recovery.Score == nil || recovery.Mode != "estimated" || recovery.Components["rhr"].InputBasis != tc.basis || recovery.Components["hrv"].InputBasis != "default_baseline" || recovery.Components["sleep"].InputBasis != "default_baseline" {
				t.Fatal("night-health fallback did not survive the real day publication")
			}
			if recovery.Components["rhr"].MeanSamples != len(tc.ages) {
				t.Fatal("sparse observations outside 28 days were not loaded")
			}
			var ready rd.V4Result
			artifact("training_readiness", &ready)
			if ready.Score == nil || *ready.Score != 82 || recovery.Score == nil || *recovery.Score != 82 || ready.Mode != "estimated" || ready.PhysiologicalCap == nil {
				t.Fatal("missing night-health still blocked readiness")
			}
		})
	}
}

// TestModelStackDayIsolatesWorkoutsAndKeepsSourceAcrossUpdates runs one real
// v2 day release over synthetic Workouts:
//   - a warm-up below the resting reference used to fail the whole day;
//   - a reference entered today must cover the earlier Workouts it is
//     effective for;
//   - a Watch software update must not split one device into two source epochs.
func TestModelStackDayIsolatesWorkoutsAndKeepsSourceAcrossUpdates(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	warmup := func(i int) float64 {
		if i < 12 {
			return 45 + float64(i)*2.5
		}
		return 150
	}
	runs := []stackDayRun{
		{syncing.NewID(), -3, "11.0", steadyHR},
		{syncing.NewID(), -2, "11.0", warmup},
		{syncing.NewID(), -1, "11.1", steadyHR}, // after a Watch software update
	}
	today, artifact := runStackDay(t, f, runs, stackDayOptions{})
	day := today.Format(time.DateOnly)
	var state tl.StateResult
	artifact("load_state", &state)
	if len(state.Days) != 1 || state.Days[0].Day != day || state.Days[0].UnknownWorkouts != 0 || state.Days[0].Acute.Value == nil || state.Days[0].Chronic.Value == nil {
		t.Fatalf("load state: %+v", state.Days)
	}
	if state.ConfigHash != tl.ConfigHashV2() {
		t.Fatal("load state carries a stale policy identity")
	}
	var ready rd.Result
	artifact("training_readiness", &ready)
	if ready.KnownLoads != 3 || ready.UnknownLoads != 0 || slices.Contains(ready.Reasons, "recent_load_unknown") {
		t.Fatalf("readiness loads known=%d unknown=%d reasons=%v", ready.KnownLoads, ready.UnknownLoads, ready.Reasons)
	}
	// One Watch, before and after its update, stays one source epoch.
	epochs := map[string]string{}
	rows, err := f.s.Pool.Query(ctx, `SELECT a.payload FROM analysis_publications p
 JOIN analysis_artifacts a ON a.dataset_id=p.dataset_id AND a.run_id=p.published_run_id AND a.artifact_key='workout_features'
 WHERE p.dataset_id=$1 AND p.scope_key LIKE $2`, f.p.DatasetID, "model/"+modelmanifest.StackID()+"/workout/%")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var b []byte
		var feature wf.WorkoutFeatures
		if err := rows.Scan(&b); err != nil || json.Unmarshal(b, &feature) != nil {
			t.Fatal(err)
		}
		epochs[feature.WorkoutID] = strings.Join(feature.Evidence.SourceEpochs, ",")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(epochs) != 3 || epochs[runs[0].id] == "" || epochs[runs[0].id] != epochs[runs[2].id] {
		t.Fatalf("source epochs split across a software update: %v", epochs)
	}
	// Settings readers: the effective reference, the observed 30 s maximum
	// (a suggestion only) and the VO2 source list.
	ref, err := f.s.CurrentHRReference(ctx, f.p)
	if err != nil || ref == nil || ref.MaxHR != 190 || ref.RestingHR == nil || *ref.RestingHR != 50 {
		t.Fatalf("current HR reference: %+v %v", ref, err)
	}
	observed, err := f.s.ObservedMaxHR(ctx, f.p)
	if err != nil || observed == nil || observed.Value != 150 {
		t.Fatalf("observed max HR: %+v %v", observed, err)
	}
	sources, err := f.s.VO2Sources(ctx, f.p)
	if err != nil || len(sources) != 0 {
		t.Fatalf("vo2 sources: %+v %v", sources, err)
	}
}

// A Workout whose stored facts cannot be evaluated is an unknown load on that
// Workout only; the day release still publishes every product artifact.
func TestModelStackDayIsolatesCorruptWorkout(t *testing.T) {
	f := setup(t)
	corrupt := syncing.NewID()
	runs := []stackDayRun{
		{corrupt, -6, "11.0", steadyHR},
		{syncing.NewID(), -2, "11.0", steadyHR},
		{syncing.NewID(), -1, "11.0", steadyHR},
	}
	_, artifact := runStackDay(t, f, runs, stackDayOptions{corrupt: corrupt})
	var state tl.StateResult
	artifact("load_state", &state)
	d := state.Days[0]
	if d.Acute.Value == nil || d.Chronic.Value == nil || !d.Imputed || d.Acute.UnknownWeightMass <= 0 {
		t.Fatalf("corrupt workout blanked or vanished from the load state: %+v", d)
	}
	var ready rd.Result
	artifact("training_readiness", &ready)
	if ready.KnownLoads != 2 || ready.UnknownLoads != 1 {
		t.Fatalf("readiness loads known=%d unknown=%d", ready.KnownLoads, ready.UnknownLoads)
	}
	// The Phase 0 health report sees the same release and the isolated input.
	h, err := f.s.ModelStackHealth(context.Background(), f.p.DatasetID, "candidate", 30, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !h.StackIsCode || h.Published["load_state"].PublishedDays != 1 || h.Published["load_state"].MissingDays != 29 ||
		h.Published["training_readiness"].PublishedDays != 1 || h.Workouts.Workouts != 3 ||
		h.Workouts.InputFailures["analysis_invalid_input"] != 1 || h.Workouts.LoadTier["complete"] != 2 || h.Workouts.LoadTier["unknown"] != 1 ||
		h.Workouts.LoadReasons["workout_invalid_input"] != 1 || h.Workouts.StableSourceSets != 1 {
		b, _ := json.Marshal(h)
		t.Fatalf("health report: %s", b)
	}
	b, _ := json.Marshal(h)
	for _, id := range []string{corrupt, runs[1].id, f.p.DatasetID} {
		if strings.Contains(string(b), id) {
			t.Fatal("health report leaked an identifier")
		}
	}
}

// Without an explicit resting HR each Workout's dose uses the 28-day resting
// baseline before it. Workouts weeks back need facts older than the evaluated
// day's own recovery window.
func TestModelStackDayRestingBaselineCoversHistory(t *testing.T) {
	f := setup(t)
	runs := []stackDayRun{
		{syncing.NewID(), -40, "11.0", steadyHR},
		{syncing.NewID(), -25, "11.0", steadyHR},
		{syncing.NewID(), -3, "11.0", steadyHR},
	}
	_, artifact := runStackDay(t, f, runs, stackDayOptions{maxOnly: true, dailyRHRDays: 80})
	var ready rd.Result
	artifact("training_readiness", &ready)
	if ready.KnownLoads != 3 || ready.UnknownLoads != 0 {
		t.Fatalf("historical Workouts lost their resting baseline: known=%d unknown=%d", ready.KnownLoads, ready.UnknownLoads)
	}
}

// processStackJobs releases every queued v2 day and returns the successes.
func processStackJobs(t *testing.T, f *fixture) int {
	t.Helper()
	ctx := context.Background()
	succeeded := 0
	for i := 0; i < 10; i++ {
		l, err := f.s.Claim(ctx, []string{modelmanifest.StackJobKind})
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		status, err := f.s.ProcessModelStack(ctx, l, time.Now())
		if err != nil {
			t.Fatalf("day release failed: %v", err)
		}
		if status == "SUCCEEDED" {
			succeeded++
		}
	}
	return succeeded
}

// Schema 53: each running Workout's feature, HR exposure and v2 load are kept
// per stack and fact generation. A later release of the same day reuses the
// cached feature until the Workout's facts change; the product readers serve
// the active stack's load; no retired analysis job is ever queued.
func TestModelStackDayCachesWorkoutResults(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	runs := []stackDayRun{
		{syncing.NewID(), -2, "11.0", steadyHR},
		{syncing.NewID(), -1, "11.0", steadyHR},
	}
	today, artifact := runStackDay(t, f, runs, stackDayOptions{})
	stack := modelmanifest.StackID()
	var state tl.StateResult
	artifact("load_state", &state)
	if state.Checkpoint == nil || state.Checkpoint.Day != today.Format(time.DateOnly) || state.Checkpoint.LedgerHash == "" {
		t.Fatalf("load state checkpoint: %+v", state.Checkpoint)
	}
	type cached struct {
		generation, scope int64
		load              *float64
		max               float64
	}
	read := func(id string) cached {
		t.Helper()
		var c cached
		if err := f.s.Pool.QueryRow(ctx, `SELECT r.fact_generation,d.generation,(r.load->>'load')::float8,(r.feature->'heartRate'->>'robustMax30sMedian')::float8
 FROM model_workout_results r JOIN dirty_scopes d ON d.dataset_id=r.dataset_id AND d.scope_key='workout:HKWorkoutTypeIdentifier:'||r.workout_uuid::text
 WHERE r.dataset_id=$1 AND r.stack_id=$2 AND r.workout_uuid=$3`, f.p.DatasetID, stack, id).Scan(&c.generation, &c.scope, &c.load, &c.max); err != nil {
			t.Fatalf("cached result %s: %v", id, err)
		}
		return c
	}
	for _, r := range runs {
		c := read(r.id)
		if c.generation != c.scope || c.load == nil || *c.load <= 0 || c.max != 150 {
			t.Fatalf("cached result %s: %+v", r.id, c)
		}
	}
	// Mark the cached feature; a new release of the same day must reuse it.
	marker := func(id string) {
		if _, err := f.s.Pool.Exec(ctx, `UPDATE model_workout_results SET feature=jsonb_set(feature,'{heartRate,robustMax30sMedian}','151')
 WHERE dataset_id=$1 AND stack_id=$2 AND workout_uuid=$3`, f.p.DatasetID, stack, id); err != nil {
			t.Fatal(err)
		}
	}
	marker(runs[0].id)
	if _, err := f.s.Pool.Exec(ctx, `SELECT rai_schedule_model_day($1,$2,$3::date,now())`, f.p.DatasetID, stack, today.Format(time.DateOnly)); err != nil {
		t.Fatal(err)
	}
	if processStackJobs(t, f) == 0 {
		t.Fatal("second release did not succeed")
	}
	if c := read(runs[0].id); c.max != 151 || c.load == nil {
		t.Fatalf("unchanged Workout was recomputed: %+v", c)
	}
	// A fact change bumps the Workout's generation and invalidates the cache.
	if _, err := f.s.Pool.Exec(ctx, `SELECT rai_analysis_dirty($1,$2,'analysis.workout',now()-interval '2 days',now()-interval '2 days')`,
		f.p.DatasetID, "workout:HKWorkoutTypeIdentifier:"+runs[0].id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(ctx, `SELECT rai_schedule_model_day($1,$2,$3::date,now())`, f.p.DatasetID, stack, today.Format(time.DateOnly)); err != nil {
		t.Fatal(err)
	}
	if processStackJobs(t, f) == 0 {
		t.Fatal("third release did not succeed")
	}
	if c := read(runs[0].id); c.max != 150 || c.generation != c.scope || c.load == nil {
		t.Fatalf("changed Workout kept a stale cache: %+v", c)
	}
	var retired int
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE dataset_id=$1 AND kind LIKE 'analysis.%' AND kind<>$2`, f.p.DatasetID, modelmanifest.StackJobKind).Scan(&retired); err != nil || retired != 0 {
		t.Fatalf("retired analysis jobs queued: %d %v", retired, err)
	}
	// Product readers follow the active channel only.
	page, err := f.s.ProductWorkouts(ctx, f.p, "", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range page.Items {
		if len(w.Metrics) != 0 {
			t.Fatalf("candidate-only load leaked to the product list: %+v", w)
		}
	}
	if _, err := f.s.Pool.Exec(ctx, `INSERT INTO analysis_model_channels SELECT (jsonb_populate_record(NULL::analysis_model_channels, to_jsonb(c)||'{"channel":"active"}')).*
 FROM analysis_model_channels c WHERE c.dataset_id=$1 AND c.channel='candidate'
 ON CONFLICT (dataset_id,channel) DO UPDATE SET stack_id=EXCLUDED.stack_id`, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	page, err = f.s.ProductWorkouts(ctx, f.p, "", 10, "")
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("product list: %+v %v", page, err)
	}
	for _, w := range page.Items {
		want := read(w.ID).load
		if len(w.Metrics) != 1 || w.Metrics[0].Name != "workout_load" || w.Metrics[0].Value == nil || *w.Metrics[0].Value != *want || w.AnalysisStatus != "published" {
			b, _ := json.Marshal(w)
			t.Fatalf("product workout: %s", b)
		}
	}
	d, err := f.s.WorkoutDetail(ctx, f.p, runs[1].id)
	if err != nil || d.TrainingLoad == nil || d.TrainingLoad.Load == nil || *d.TrainingLoad.Load != *read(runs[1].id).load {
		b, _ := json.Marshal(d.TrainingLoad)
		t.Fatalf("workout detail load: %s %v", b, err)
	}
	// The detail says whether the active stack admits the run, and why not.
	q := d.ModelQualification
	known := map[string]bool{"eligible": true, "conditional": true, "ineligible": true}
	if q == nil || !known[q.TrainingLoad.Status] || q.TrainingLoad.Status != "eligible" || q.TrainingLoad.ReasonCodes == nil {
		b, _ := json.Marshal(q)
		t.Fatalf("workout detail qualification: %s", b)
	}
	if d.RunFitness != nil && d.RunFitness.Single.WorkoutID != runs[1].id {
		t.Fatal("run fitness of another Workout attached to the detail")
	}
}
