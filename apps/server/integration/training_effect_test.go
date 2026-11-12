//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"rai/server/internal/modelmanifest"
	rf "rai/server/internal/runfitness"
	"rai/server/internal/syncing"
	te "rai/server/internal/trainingeffect"
)

// intervalRun is a synthetic 40-minute session: 10 min warm-up at 3.0 m/s,
// 8 x (77 s at 5.2 m/s + 90 s jog at 2.6 m/s), then an easy finish. Speed,
// power and steps come in native 2.56 s intervals; HR in 5 s intervals follows
// speed through a first-order response, as on a real Watch.
func intervalRun(f *fixture, id string, start time.Time) []byte {
	const seconds = 40 * 60
	speedAt := func(sec float64) float64 {
		if sec < 600 {
			return 3.0
		}
		k := sec - 600
		if k < 8*167 {
			if math.Mod(k, 167) < 77 {
				return 5.2
			}
			return 2.6
		}
		return 3.0
	}
	hr := make([]float64, seconds+1)
	h := .5
	for s := 0; s <= seconds; s++ {
		target := math.Min(1, .92*speedAt(float64(s))/4.3-.04)
		tc := 30.0
		if target < h {
			tc = 50
		}
		h += (target - h) * (1 - math.Exp(-1/tc))
		hr[s] = 50 + 140*h
	}
	var hrs, speeds, powers, steps []string
	for a := 0; a+5 <= seconds; a += 5 {
		hrs = append(hrs, fmt.Sprintf("[%d,%d,%.1f]", int64(a)*1e6, int64(a+5)*1e6, hr[a+2]))
	}
	const step = int64(2_560_000)
	for a := int64(0); a+step <= seconds*1e6; a += step {
		v := speedAt(float64(a+step/2) / 1e6)
		speeds = append(speeds, fmt.Sprintf("[%d,%d,%.3f]", a, a+step, v))
		powers = append(powers, fmt.Sprintf("[%d,%d,%.1f]", a, a+step, 70*v))
		steps = append(steps, fmt.Sprintf("[%d,%d,%.2f]", a, a+step, 2.56*(150+6*v)/60))
	}
	stream := func(kind, unit string, samples []string) string {
		return fmt.Sprintf(`{"type":%q,"unit":%q,"status":"complete","samples":[%s]}`, kind, unit, strings.Join(samples, ","))
	}
	return []byte(fmt.Sprintf(`{
		"protocolVersion":6,
		"datasetID":%q,"datasetEpoch":%q,"collectorEpoch":%q,"workoutID":%q,
		"startUs":%d,"endUs":%d,
		"source":{"bundleID":"com.apple.health","version":"11.0","productType":"Watch7,1","operatingSystemVersion":"11.0"},
		"metadata":{"HKTimeZone":"Asia/Shanghai","HKIndoorWorkout":false},
		"workout":{"activityType":37,"durationUs":%d},
		"streams":[%s,%s,%s,%s],
		"routes":[]
	}`, f.p.DatasetID, f.enrollment.DatasetEpoch, f.enrollment.CollectorEpoch, id,
		start.UnixMicro(), start.Add(seconds*time.Second).UnixMicro(), int64(seconds)*1_000_000,
		stream(v6HRType, "count/min", hrs), stream("HKQuantityTypeIdentifierRunningSpeed", "m/s", speeds),
		stream(v6PowerType, "W", powers), stream("HKQuantityTypeIdentifierStepCount", "count", steps)))
}

// seedFitness publishes a clean running-fitness day state (vT 4.3, v6 5.0)
// for the given day of the current stack, as an earlier release would have.
func seedFitness(t *testing.T, f *fixture, day time.Time) {
	t.Helper()
	ctx := context.Background()
	vt, v6 := 4.3, 5.0
	state := rf.State{Schema: rf.Schema, Algorithm: rf.Algorithm, Version: rf.Version, ConfigHash: rf.ConfigHash(), DatasetID: f.p.DatasetID, Generation: 1, Date: day.Format(time.DateOnly),
		AsOf: day.Add(12 * time.Hour).UnixMicro(), Status: "established", Params: rf.Params{VT: &vt, V6: &v6}}
	state.Dimensions.Speed.Status = "supported"
	payload, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := modelmanifest.CurrentHash()
	if err != nil {
		t.Fatal(err)
	}
	stack := modelmanifest.StackID()
	scope, run, job := modelmanifest.DayScope(stack, day.Format(time.DateOnly)), syncing.NewID(), syncing.NewID()
	tx, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO dirty_scopes(dataset_id,scope_key,generation,clean_generation,dirty_from,dirty_through) VALUES($1,$2,1,1,$3,$3)`, []any{f.p.DatasetID, scope, day}},
		{`INSERT INTO jobs(id,dataset_id,kind,scope_key,target_generation,state) VALUES($1,$2,$3,$4,1,'succeeded')`, []any{job, f.p.DatasetID, modelmanifest.StackJobKind, scope}},
		{`INSERT INTO analysis_runs(dataset_id,id,job_id,fencing_token,scope_key,target_generation,algorithm_name,algorithm_version,input_version,input_hash,input_from,input_through,lineage,status)
 VALUES($1,$2,$3,1,$4,1,$5,$6,'rai.model_stack.input/2',decode(repeat('00',32),'hex'),$7,$7,jsonb_build_object('manifestSHA256',$8::text),'RUNNING')`, []any{f.p.DatasetID, run, job, scope, rf.Algorithm, rf.Version, day, manifest}},
		{`INSERT INTO analysis_artifacts(dataset_id,run_id,artifact_key,schema_version,content_sha256,payload) VALUES($1,$2,$3,$4,decode($5,'hex'),$6::jsonb)`, []any{f.p.DatasetID, run, rf.ArtifactKey, rf.Schema, rf.Hash(state), payload}},
		{`UPDATE analysis_runs SET status='SUCCEEDED',finished_at=now() WHERE dataset_id=$1 AND id=$2`, []any{f.p.DatasetID, run}},
		{`INSERT INTO analysis_publications(dataset_id,scope_key,metric_family,day,published_run_id,generation,published_at) VALUES($1,$2,$3,$4,$5,1,$6)`, []any{f.p.DatasetID, scope, "model_product_v3/" + rf.ArtifactKey, day, run, day.Add(12 * time.Hour)}},
	} {
		if _, err := tx.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestModelStackDayTrainingEffect runs a real day release: the interval run
// after a published fitness state gets both scores; the HR-only run before it
// keeps a null anaerobic score with its reason, never 0. The detail API serves
// exactly the stored result.
func TestModelStackDayTrainingEffect(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	steady, intervals := syncing.NewID(), syncing.NewID()
	runs := []stackDayRun{
		{steady, -2, "11.0", steadyHR},
		{intervals, -1, "11.0", nil},
	}
	runStackDay(t, f, runs, stackDayOptions{seed: func(today time.Time) { seedFitness(t, f, today.AddDate(0, 0, -2)) },
		payloads: map[string]func(*fixture, string, time.Time) []byte{intervals: intervalRun}})
	stack := modelmanifest.StackID()
	read := func(id string) te.Result {
		t.Helper()
		var b []byte
		if err := f.s.Pool.QueryRow(ctx, `SELECT load->'trainingEffect' FROM model_workout_results WHERE dataset_id=$1 AND stack_id=$2 AND workout_uuid=$3`,
			f.p.DatasetID, stack, id).Scan(&b); err != nil {
			t.Fatalf("result %s: %v", id, err)
		}
		var r te.Result
		if err := json.Unmarshal(b, &r); err != nil || r.Schema != te.Schema || r.Policy != te.Policy {
			t.Fatalf("training effect %s: %s %v", id, b, err)
		}
		return r
	}
	s := read(steady)
	if s.Aerobic.Value == nil || *s.Aerobic.Value <= 0 || s.Anaerobic.Value != nil || s.Anaerobic.Status != "unavailable" || !reflect.DeepEqual(s.Anaerobic.ReasonCodes, []string{"speed_reference_unavailable"}) {
		t.Fatalf("HR-only run before any fitness state: %+v %+v", s.Aerobic, s.Anaerobic)
	}
	r := read(intervals)
	if r.Aerobic.Value == nil || r.Anaerobic.Value == nil || *r.Anaerobic.Value < 2.5 || *r.Aerobic.Value < 2 || r.Bouts != 8 {
		b, _ := json.Marshal(r)
		t.Fatalf("interval run: %s", b)
	}
	if r.AnaerobicReserve == nil || math.Abs(*r.AnaerobicReserve-(5.0-.97*4.3)*360) > .1 || r.LevelStatus != "estimated" && r.LevelStatus != "default" {
		t.Fatalf("reserve/level: %v %s", r.AnaerobicReserve, r.LevelStatus)
	}
	t.Logf("intervals: aerobic %.1f anaerobic %.1f EPOC %.0f bouts %d", *r.Aerobic.Value, *r.Anaerobic.Value, *r.EPOCPeak, r.Bouts)
	// Promote the candidate and read it through the detail projection.
	if _, err := f.s.Pool.Exec(ctx, `INSERT INTO analysis_model_channels SELECT (jsonb_populate_record(NULL::analysis_model_channels, to_jsonb(c)||'{"channel":"active"}')).*
 FROM analysis_model_channels c WHERE c.dataset_id=$1 AND c.channel='candidate'
 ON CONFLICT (dataset_id,channel) DO UPDATE SET stack_id=EXCLUDED.stack_id`, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	d, err := f.s.WorkoutDetail(ctx, f.p, intervals)
	if err != nil || d.TrainingEffect == nil || !reflect.DeepEqual(*d.TrainingEffect, r) {
		t.Fatalf("detail training effect differs from the stored result: %v", err)
	}
	b, _ := json.Marshal(d)
	if !strings.Contains(string(b), `"trainingEffect":{"schemaVersion":"rai.training_effect/1"`) {
		t.Fatal("detail JSON missing trainingEffect")
	}
}
