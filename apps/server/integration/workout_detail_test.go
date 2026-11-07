//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"rai/server/internal/httpapi"
	"rai/server/internal/syncing"
	wf "rai/server/internal/workoutfeatures"
)

func TestWorkoutDetailFactsPendingAndFreshness(t *testing.T) {
	f, w := analysisFixture(t)
	ctx := context.Background()
	detail, err := f.s.WorkoutDetail(ctx, f.p, w.UUID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Workout.WorkoutID != w.UUID || detail.Workout.DistanceM == nil || *detail.Workout.DistanceM != 10000 {
		t.Fatalf("detail facts %+v", detail.Workout)
	}
	if detail.Workout.AnalysisStatus != "pending" || detail.Workout.AveragePaceSPerKm == nil || *detail.Workout.AveragePaceSPerKm != 360 {
		t.Fatalf("pending detail fabricated analysis: %+v", detail.Workout)
	}
	if detail.Route.PointCount != 0 || detail.Summary.GradeAdjustedPaceSPerKm != nil {
		t.Fatalf("route or grade fabricated: %+v %+v", detail.Route, detail.Summary)
	}
	handler := httpapi.New(f.s, analysisLog)
	for _, tc := range []struct {
		id   string
		code int
	}{{syncing.NewID(), 404}, {"not-a-uuid", 422}} {
		r := httptest.NewRequest("GET", "/v2/workouts/"+tc.id+"/detail", nil)
		r.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, r)
		if out.Code != tc.code {
			t.Fatalf("detail id=%s got %d want %d", tc.id, out.Code, tc.code)
		}
	}
	other := f.p
	other.DatasetID = syncing.NewID()
	if _, err := f.s.WorkoutDetail(ctx, other, w.UUID); err == nil {
		t.Fatal("cross-dataset workout detail exposed")
	}
	series, err := f.s.WorkoutSeries(ctx, f.p, w.UUID)
	if err != nil || len(series.Splits) != 0 || series.Series.HeartRate.TotalSamples != 0 {
		t.Fatalf("series without facts %+v %v", series, err)
	}
	fresh, err := f.s.WorkoutFreshness(ctx, f.p, w.UUID)
	if err != nil || fresh.DeliveryState != "delivered" || fresh.AnalysisState != "pending" {
		t.Fatalf("freshness %+v %v", fresh, err)
	}
	for _, path := range []string{"/v2/workouts/" + w.UUID + "/detail", "/v2/workouts/" + w.UUID + "/series", "/v2/workouts/" + w.UUID + "/freshness"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, r)
		if out.Code != 200 {
			t.Fatalf("%s %d %s", path, out.Code, out.Body.String())
		}
		r = httptest.NewRequest("GET", path+"?unexpected=1", nil)
		r.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
		out = httptest.NewRecorder()
		handler.ServeHTTP(out, r)
		if out.Code != 422 {
			t.Fatalf("%s accepted unknown query", path)
		}
	}
	runOne(t, f, "analysis.workout")
	detail, err = f.s.WorkoutDetail(ctx, f.p, w.UUID)
	if err != nil || detail.Workout.AnalysisStatus != "published" || detail.Workout.ModelVersion == "" {
		t.Fatalf("published detail %+v %v", detail.Workout, err)
	}
}

func TestWorkoutDetailSeriesRouteAssociationAndSplits(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	start := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	end := start.Add(time.Hour)
	startUs, endUs := start.UnixMicro(), end.UnixMicro()

	// Heart-rate parent + sealed series.
	hrStream := f.stream
	parentID, seriesID := syncing.NewID(), syncing.NewID()
	f.commit(t, []syncing.Event{seriesParent(parentID, startUs, endUs, 150)}, 1, nil)
	registerAndSealSeries(t, f, kind, parentID, seriesID, startUs, endUs, "count/min", seriesPoints(startUs, 150, 150, 150, 150, 150, 150, 150, 150, 150, 150))

	// Running speed parent + sealed series.
	speedType := "HKQuantityTypeIdentifierRunningSpeed"
	speedStream, err := f.s.RegisterStream(ctx, f.p, syncing.StreamRequest{DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch, StreamID: syncing.NewID(), SampleType: speedType, QueryFingerprint: "sha256:" + repeatChar('c', 64)})
	if err != nil {
		t.Fatal(err)
	}
	f.stream = speedStream
	speedParent, speedSeries := syncing.NewID(), syncing.NewID()
	speedValues := make([]float64, 10)
	for i := range speedValues {
		speedValues[i] = 1000.0 / 360.0
	}
	speedPoints := make([]syncing.SeriesPoint, len(speedValues))
	for i, v := range speedValues {
		speedPoints[i] = syncing.SeriesPoint{StartUs: startUs + int64(i)*360*1_000_000, EndUs: startUs + int64(i+1)*360*1_000_000, Value: v, Unit: "m/s"}
	}
	f.commit(t, []syncing.Event{{Op: "upsert", Kind: "sample", Type: speedType, UUID: speedParent, StartUs: &startUs, EndUs: &endUs, Quantity: &syncing.Quantity{Value: 2.777, Unit: "m/s"}, Source: &syncing.Source{BundleID: "synthetic.test"}}}, 1, nil)
	registerAndSealSeries(t, f, speedType, speedParent, speedSeries, startUs, endUs, "m/s", speedPoints)

	// Workout + explicit associations.
	f.stream = hrStream
	workoutStream(t, f)
	w := workoutAt(start, 3600)
	w.Workout.TotalDistance = &syncing.Quantity{Value: 10000, Unit: "m"}
	f.commit(t, []syncing.Event{w}, 1, nil)
	for _, a := range []struct{ typ, id string }{{kind, parentID}, {speedType, speedParent}} {
		if _, err = f.s.AssociateSample(ctx, f.p, syncing.AssociationRequest{ProtocolVersion: 2, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch, AssociationID: syncing.NewID(), SampleType: a.typ, SampleUUID: a.id, WorkoutUUID: w.UUID, Source: &syncing.Source{BundleID: "synthetic.test"}}); err != nil {
			t.Fatal(err)
		}
	}

	// Sealed route with altitude and a stable sequence.
	routeID := syncing.NewID()
	points := make([]syncing.RoutePoint, 100)
	for i := range points {
		alt := 10 + float64(i)
		points[i] = syncing.RoutePoint{Sequence: int64(i), RecordedAtUs: startUs + int64(i)*36*1_000_000, Latitude: 31.2304 + float64(i)*0.00001, Longitude: 121.4737 + float64(i)*0.00001, Altitude: &alt}
	}
	registerTestRoute(t, f, w.UUID, routeID, start, end, 1, int64(len(points)))
	uploadTestRouteChunk(t, f, routeID, 0, points)
	manifest, payload := routeStateChains(t, f, routeID)
	sealTestRoute(t, f, routeID, 1, int64(len(points)), manifest, payload)

	detail, err := f.s.WorkoutDetail(ctx, f.p, w.UUID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Route.PointCount != 100 || len(detail.Route.Points) != 100 || detail.Summary.ElevationGainM != nil || detail.Summary.GradeAdjustedPaceSPerKm != nil {
		t.Fatalf("route/grade detail %+v summary=%+v", detail.Route, detail.Summary)
	}
	if detail.Workout.HeartRateSampleCount < 10 || detail.Workout.AverageHeartRateBpm == nil {
		t.Fatalf("series summary %+v", detail.Workout)
	}
	series, err := f.s.WorkoutSeries(ctx, f.p, w.UUID)
	if err != nil {
		t.Fatal(err)
	}
	if len(series.Splits) != 10 || series.Series.Pace.TotalSamples != 10 || series.Series.HeartRate.TotalSamples != 10 {
		t.Fatalf("splits/series %+v %+v", series.Splits, series.Series)
	}
	if series.Grade != nil || series.Dynamics != nil {
		t.Fatal("unpublished series analysis exposed")
	}
	runOne(t, f, "analysis.workout")
	detail, err = f.s.WorkoutDetail(ctx, f.p, w.UUID)
	if err != nil || detail.Workout.AnalysisStatus != "published" || detail.Summary.ElevationGainM == nil || detail.Summary.GradeAdjustedPaceSPerKm == nil {
		t.Fatalf("published detail unavailable: %v", err)
	}
	series, err = f.s.WorkoutSeries(ctx, f.p, w.UUID)
	if err != nil || series.Grade == nil || series.Dynamics == nil {
		t.Fatalf("published series unavailable: %v", err)
	}
	fresh, err := f.s.WorkoutFreshness(ctx, f.p, w.UUID)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, s := range fresh.Series {
		states[s.Metric] = s.State
	}
	if states["heartRate"] != "present" || states["pace"] != "present" || states["elevation"] != "present" {
		t.Fatalf("freshness states %+v", states)
	}
	if _, err = json.Marshal(series); err != nil {
		t.Fatal("series not serializable", err)
	}
}

func registerAndSealSeries(t *testing.T, f *fixture, sampleType, parentID, seriesID string, startUs, endUs int64, unit string, points []syncing.SeriesPoint) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.s.RegisterSeries(ctx, f.p, syncing.SeriesRequest{DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch, SeriesID: seriesID, SampleType: sampleType, SampleUUID: parentID}); err != nil {
		t.Fatal(err)
	}
	r, _ := commitSeries(t, f, seriesID, 0, points)
	if _, err := f.s.SealSeries(ctx, f.p, seriesID, syncing.SeriesSeal{ChunkCount: 1, TotalPoints: int64(len(points)), ManifestSHA256: r.ManifestSHA256}); err != nil {
		t.Fatal(err)
	}
	_ = unit
}

func repeatChar(c byte, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = c
	}
	return string(b)
}

func TestRouteClockSkewAnalysisPreservesRawFacts(t *testing.T) {
	f, w := analysisFixture(t)
	ctx := context.Background()
	start, end := time.UnixMicro(*w.StartUs), time.UnixMicro(*w.EndUs)
	route := syncing.NewID()
	registerTestRoute(t, f, w.UUID, route, start.Add(-5*time.Second), end.Add(5*time.Second), 1, 4)
	points := testRoutePoints(start, 4, 0)
	points[0].RecordedAtUs = start.Add(-5 * time.Second).UnixMicro()
	points[1].RecordedAtUs = start.UnixMicro()
	points[2].RecordedAtUs = start.Add(time.Second).UnixMicro()
	points[3].RecordedAtUs = end.Add(5 * time.Second).UnixMicro()
	uploadTestRouteChunk(t, f, route, 0, points)
	manifest, payload := routeStateChains(t, f, route)
	sealTestRoute(t, f, route, 1, 4, manifest, payload)
	runOne(t, f, "analysis.workout")
	detail, err := f.s.WorkoutDetail(ctx, f.p, w.UUID)
	if err != nil || !detail.Workout.AnalysisAvailable || detail.Route.PointCount != 4 {
		t.Fatalf("clock skew blocked publication or lost facts: %v", err)
	}
	var raw int
	if err = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM route_points WHERE dataset_id=$1 AND route_uuid=$2`, f.p.DatasetID, route).Scan(&raw); err != nil || raw != 4 {
		t.Fatal("raw route changed", err)
	}
	gotManifest, gotPayload := routeStateChains(t, f, route)
	if gotManifest != manifest || gotPayload != payload {
		t.Fatal("immutable route identity changed")
	}
	for _, offset := range []time.Duration{-5*time.Second - time.Microsecond, 5*time.Second + time.Microsecond} {
		first, last := start, end
		if offset < 0 {
			first = start.Add(offset)
		} else {
			last = end.Add(offset)
		}
		_, err = f.s.RegisterRoute(ctx, f.p, syncing.RouteManifest{ProtocolVersion: 2, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch, RouteID: syncing.NewID(), WorkoutID: w.UUID, Source: &syncing.Source{BundleID: "synthetic.test"}, ExpectedChunkCount: 1, ExpectedPointCount: 2, FirstRecordedAtUs: first.UnixMicro(), LastRecordedAtUs: last.UnixMicro()})
		if err == nil {
			t.Fatal("out-of-tolerance route accepted")
		}
	}
}

func TestWorkoutDetailReadsPublishedFeatureArtifact(t *testing.T) {
	f, w := analysisFixture(t)
	ctx := context.Background()
	id := w.UUID
	now := time.Now().UTC()
	payload, err := json.Marshal(wf.WorkoutFeatures{
		Schema: wf.Schema, Algorithm: wf.Algorithm, Version: wf.Version,
		StackID: wf.StackID(), DatasetID: f.p.DatasetID, WorkoutID: id, Generation: 1,
		Timing:    wf.Timing{Active: func() *float64 { v := 600.0; return &v }()},
		HeartRate: wf.SignalSummary{ObservedCoverage: 0.9},
		Speed:     wf.SignalSummary{ObservedCoverage: 0.8},
		Route:     wf.RouteSummary{Coverage: 0.7},
		Reasons:   []wf.Reason{wf.MissingHR},
	})
	if err != nil {
		t.Fatal(err)
	}
	runID, jobID := syncing.NewID(), syncing.NewID()
	tx, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO jobs(id,dataset_id,kind,scope_key,target_generation,state) VALUES($1,$2,$3,$4,1,'queued')`, jobID, f.p.DatasetID, wf.JobKind, wf.Scope(id)); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO analysis_runs(dataset_id,id,job_id,fencing_token,scope_key,target_generation,algorithm_name,algorithm_version,input_version,input_hash,input_from,input_through,lineage,status)
		VALUES($1,$2,$3,1,$4,1,$5,$6,'feature-v1',decode(repeat('01',32),'hex'),$7,$8,'{}'::jsonb,'RUNNING')`, f.p.DatasetID, runID, jobID, wf.Scope(id), wf.Algorithm, wf.Version, now.Add(-time.Hour), now); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	if _, err = tx.Exec(ctx, `INSERT INTO analysis_artifacts(dataset_id,run_id,artifact_key,schema_version,content_sha256,payload) VALUES($1,$2,$3,$4,$5,$6::jsonb)`, f.p.DatasetID, runID, wf.ArtifactKey, wf.Schema, sum[:], payload); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE analysis_runs SET status='SUCCEEDED',finished_at=now() WHERE dataset_id=$1 AND id=$2`, f.p.DatasetID, runID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO dirty_scopes(dataset_id,scope_key,generation,clean_generation,dirty_from,dirty_through) VALUES($1,$2,1,1,$3,$3) ON CONFLICT(dataset_id,scope_key) DO UPDATE SET generation=1,clean_generation=1`, f.p.DatasetID, wf.Scope(id), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO analysis_publications(dataset_id,scope_key,metric_family,day,published_run_id,generation) VALUES($1,$2,'workout_model',$3::date,$4,1)`, f.p.DatasetID, wf.Scope(id), now.Format("2006-01-02"), runID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	hidden, err := f.s.WorkoutDetail(ctx, f.p, id)
	if err != nil || hidden.Analysis != nil {
		t.Fatalf("inactive feature leaked: analysis=%v err=%v", hidden.Analysis, err)
	}
	if _, err := f.s.Pool.Exec(ctx, `INSERT INTO analysis_model_channels(dataset_id,channel,stack_id,config_sha256,assignment_version)
		VALUES($1,'active',$2,decode(repeat('ab',32),'hex'),1)`, f.p.DatasetID, wf.StackID()); err != nil {
		t.Fatal(err)
	}
	detail, err := f.s.WorkoutDetail(ctx, f.p, id)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Analysis == nil || detail.Analysis.Schema != wf.Schema || detail.Analysis.HeartRateCoverage != 0.9 || detail.Analysis.SpeedCoverage != 0.8 || detail.Analysis.RouteCoverage != 0.7 || detail.Analysis.ActiveSeconds == nil || *detail.Analysis.ActiveSeconds != 600 || len(detail.Analysis.Reasons) != 1 || detail.Analysis.Reasons[0] != string(wf.MissingHR) {
		t.Fatalf("published feature analysis %+v", detail.Analysis)
	}
	handler := httpapi.New(f.s, analysisLog)
	r := httptest.NewRequest("GET", "/v2/workouts/"+id+"/detail", nil)
	r.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, r)
	if out.Code != 200 {
		t.Fatalf("detail http %d %s", out.Code, out.Body.String())
	}
	if _, err = f.s.Pool.Exec(ctx, `UPDATE dirty_scopes SET generation=2 WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, wf.Scope(id)); err != nil {
		t.Fatal(err)
	}
	detail, err = f.s.WorkoutDetail(ctx, f.p, id)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Analysis != nil {
		t.Fatalf("dirty feature leaked: %+v", detail.Analysis)
	}
}
