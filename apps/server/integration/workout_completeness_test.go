//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"net/http/httptest"
	"os"
	"rai/server/internal/admin"
	"rai/server/internal/httpapi"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
	"strings"
	"testing"
	"time"
)

func completenessManifest(f *fixture, bundles []syncing.WorkoutBundle) syncing.WorkoutSyncCompletenessManifest {
	now := time.Now().Add(-time.Second).UnixMicro()
	m := syncing.WorkoutSyncCompletenessManifest{Contract: syncing.WorkoutCompletenessContract, ProtocolVersion: 1, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch, ManifestID: syncing.NewID(), Generation: 1, StreamSetVersion: 1, WindowFromUs: now - 200*86400000000, KnownCutoffUs: now, CreatedAtUs: now + 1000, SupportedStreams: syncing.WorkoutCompletenessTypes(), UnsupportedStreams: append([]string{}, syncing.WorkoutCompletenessUnsupported...), NotApplicableStreams: []string{}, Streams: []syncing.CompletenessStream{}, Workouts: []syncing.CompletenessWorkout{}}
	page := func(adds []string) syncing.CompletenessPage {
		return syncing.CompletenessPage{PageID: syncing.NewID(), Sequence: 1, CandidateAnchorSHA256: strings.Repeat("a", 64), QueryStartedAtUs: now, QueryCompletedAtUs: now + 1, Additions: adds, Deletions: []string{}}
	}
	roots := []string{}
	for _, b := range bundles {
		roots = append(roots, b.WorkoutID)
	}
	m.Streams = append(m.Streams, syncing.CompletenessStream{Key: "workouts", SampleType: syncing.WorkoutType, QueryFingerprint: syncing.CompletenessFingerprint(m.WindowFromUs, "workouts"), Pages: []syncing.CompletenessPage{page(roots)}})
	for _, b := range bundles {
		raw, _ := json.Marshal(b)
		w := syncing.CompletenessWorkout{WorkoutID: b.WorkoutID, BundleSHA256: syncing.BundleDigest(raw), RouteState: "absent", Series: []syncing.CompletenessSeries{}}
		if len(b.Routes) > 0 {
			w.RouteState = "present"
		}
		for _, kind := range append(append([]string{}, syncing.WorkoutCompletenessQuantities...), syncing.RouteType) {
			ids := []string{}
			for _, s := range b.Samples {
				if s.Type == kind {
					ids = append(ids, s.UUID)
				}
			}
			if kind == syncing.RouteType {
				for _, r := range b.Routes {
					ids = append(ids, r.RouteID)
				}
			}
			id := b.WorkoutID
			key := syncing.CompletenessKey(kind, id)
			m.Streams = append(m.Streams, syncing.CompletenessStream{Key: key, SampleType: kind, WorkoutID: &id, QueryFingerprint: syncing.CompletenessFingerprint(m.WindowFromUs, key), Pages: []syncing.CompletenessPage{page(ids)}})
			if kind != syncing.RouteType {
				hash, count := syncing.CompletenessSeriesDigest(b.Series, kind)
				w.Series = append(w.Series, syncing.CompletenessSeries{SampleType: kind, SHA256: hash, PointCount: count, QueryCompletedAtUs: now + 2})
			}
		}
		m.Workouts = append(m.Workouts, w)
	}
	if len(bundles) == 0 {
		m.NotApplicableStreams = append(append([]string{}, syncing.WorkoutCompletenessQuantities...), syncing.RouteType)
	}
	return m
}
func completenessBundle(t *testing.T, f *fixture, empty, routeAbsent bool) syncing.WorkoutBundle {
	n := 1
	if empty {
		n = 0
	}
	b, _ := testWorkoutBundle(t, f, n)
	if routeAbsent {
		b.Routes = []syncing.WorkoutBundleRoute{}
	}
	b.CompleteRoutes = true
	phase3Observation(&b)
	for _, kind := range syncing.WorkoutCompletenessQuantities {
		count := 0
		for _, s := range b.Samples {
			if s.Type == kind {
				count++
			}
		}
		state := "no_records_observed"
		if count > 0 {
			state = "complete"
		}
		b.Observation.Coverage[kind] = syncing.BundleCoverage{State: state, SampleCount: count, QueryCompleted: true, LocalPersisted: true}
		_, points := syncing.CompletenessSeriesDigest(b.Series, kind)
		state = "no_records_observed"
		if points > 0 {
			state = "complete"
		}
		b.Observation.Coverage["series:"+kind] = syncing.BundleCoverage{State: state, SampleCount: points, QueryCompleted: true, LocalPersisted: true}
	}
	state := "no_records_observed"
	if len(b.Routes) > 0 {
		state = "complete"
	}
	points := 0
	for _, route := range b.Routes {
		points += len(route.Points)
	}
	b.Observation.Coverage["route"] = syncing.BundleCoverage{State: state, SampleCount: points, QueryCompleted: true, LocalPersisted: true}
	importPhase3(t, f, b)
	return b
}
func commitCompleteness(t *testing.T, f *fixture, m syncing.WorkoutSyncCompletenessManifest) ([]byte, error) {
	t.Helper()
	ctx := context.Background()
	lease, err := f.s.AcquireUpload(ctx, f.p)
	if err != nil {
		return nil, err
	}
	defer f.s.ReleaseUpload(ctx, f.p, lease)
	raw, _ := json.Marshal(m)
	return f.s.CommitWorkoutCompleteness(ctx, f.p, raw, lease)
}
func nextCompleteness(m syncing.WorkoutSyncCompletenessManifest) syncing.WorkoutSyncCompletenessManifest {
	raw, _ := json.Marshal(m)
	var n syncing.WorkoutSyncCompletenessManifest
	_ = json.Unmarshal(raw, &n)
	hash := syncing.BundleDigest(raw)
	n.PreviousManifestSHA256 = &hash
	n.Generation++
	n.ManifestID = syncing.NewID()
	n.KnownCutoffUs = time.Now().UnixMicro()
	n.CreatedAtUs = n.KnownCutoffUs + 1000
	for i, s := range n.Streams {
		last := s.Pages[len(s.Pages)-1]
		ph, ah := syncing.CompletenessPageDigest(last), last.CandidateAnchorSHA256
		n.Streams[i].Pages = []syncing.CompletenessPage{{PageID: syncing.NewID(), Sequence: last.Sequence + 1, PreviousPageSHA256: &ph, PreviousAnchorSHA256: &ah, CandidateAnchorSHA256: strings.Repeat("b", 64), QueryStartedAtUs: n.KnownCutoffUs, QueryCompletedAtUs: n.KnownCutoffUs + 1, Additions: []string{}, Deletions: []string{}}}
	}
	for i := range n.Workouts {
		for j := range n.Workouts[i].Series {
			n.Workouts[i].Series[j].QueryCompletedAtUs = n.KnownCutoffUs + 2
		}
	}
	return n
}

func TestWorkoutCompletenessAllStreamsNonemptyEmptyAndRouteAbsent(t *testing.T) {
	for _, tc := range []struct {
		name          string
		empty, absent bool
	}{{"nonempty", false, false}, {"supported_empty", true, false}, {"route_absent", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			b := completenessBundle(t, f, tc.empty, tc.absent)
			m := completenessManifest(f, []syncing.WorkoutBundle{b})
			ack, err := commitCompleteness(t, f, m)
			if err != nil {
				t.Fatal(err)
			}
			retry, err := commitCompleteness(t, f, m)
			if err != nil || !bytes.Equal(ack, retry) {
				t.Fatal("immutable retry", err)
			}
			n := nextCompleteness(m)
			if _, err = commitCompleteness(t, f, n); err != nil {
				t.Fatal("incremental committed anchor chain", err)
			}
			if count(t, f, "workout_sync_completeness") != 2 || count(t, f, "workout_imports") != 1 {
				t.Fatal("repeat duplicated facts/import")
			}
		})
	}
}
func TestWorkoutCompletenessRejectsIncompleteAndForgedEvidence(t *testing.T) {
	for _, name := range []string{"pending", "sequence_gap", "anchor_conflict", "receipt_missing", "series_mismatch", "route_pending", "unsupported_required_stream", "new_stream_set", "unknown_workout", "fake_client_flag"} {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			b := completenessBundle(t, f, false, false)
			m := completenessManifest(f, []syncing.WorkoutBundle{b})
			switch name {
			case "pending":
				m.Streams[1].Pages[0].HasMore = true
			case "sequence_gap":
				m.Streams[0].Pages[0].Sequence = 2
			case "anchor_conflict":
				x := strings.Repeat("c", 64)
				m.Streams[0].Pages[0].PreviousAnchorSHA256 = &x
			case "receipt_missing":
				m.Workouts[0].BundleSHA256 = strings.Repeat("d", 64)
			case "series_mismatch":
				m.Workouts[0].Series[0].SHA256 = strings.Repeat("d", 64)
			case "route_pending":
				m.Workouts[0].RouteState = "pending"
			case "unsupported_required_stream":
				m.UnsupportedStreams = append(m.UnsupportedStreams, syncing.WorkoutCompletenessQuantities[0])
			case "new_stream_set":
				m.StreamSetVersion = 2
			case "unknown_workout":
				m.Streams[0].Pages[0].Additions = append(m.Streams[0].Pages[0].Additions, syncing.NewID())
			case "fake_client_flag":
				raw, _ := json.Marshal(m)
				raw = append([]byte(`{"complete":true,`), raw[1:]...)
				lease, err := f.s.AcquireUpload(context.Background(), f.p)
				if err != nil {
					t.Fatal(err)
				}
				defer f.s.ReleaseUpload(context.Background(), f.p, lease)
				if _, err = f.s.CommitWorkoutCompleteness(context.Background(), f.p, raw, lease); err == nil {
					t.Fatal("client-only flag accepted")
				}
				return
			}
			if _, err := commitCompleteness(t, f, m); err == nil {
				t.Fatal("invalid evidence accepted")
			}
			if count(t, f, "workout_sync_completeness") != 0 {
				t.Fatal("failed manifest persisted")
			}
		})
	}
}
func TestWorkoutCompletenessAtomicFaultsAndBackendCrash(t *testing.T) {
	for _, target := range []string{"workout_sync_completeness", "change_events", "dirty_scopes", "jobs", "backend_crash"} {
		t.Run(target, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			if err := admin.QueueReadinessCandidate(ctx, f.s, f.p.DatasetID, syncing.NewID(), "synthetic completeness atomicity"); err != nil {
				t.Fatal(err)
			}
			m := completenessManifest(f, nil)
			var before int64
			if err := f.s.Pool.QueryRow(ctx, `SELECT commit_rev FROM datasets WHERE id=$1`, f.p.DatasetID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			table, body := target, "RAISE EXCEPTION 'synthetic_completeness_failure';"
			if target == "backend_crash" {
				table = "change_events"
				body = "PERFORM pg_terminate_backend(pg_backend_pid());"
			}
			sql := `CREATE FUNCTION completeness_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN ` + body + ` RETURN NEW; END $$; CREATE TRIGGER completeness_fault BEFORE INSERT OR UPDATE ON ` + table + ` FOR EACH ROW EXECUTE FUNCTION completeness_fault()`
			if _, err := f.s.Pool.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
			if _, err := commitCompleteness(t, f, m); err == nil {
				t.Fatal("fault was hidden")
			}
			if count(t, f, "workout_sync_completeness") != 0 {
				t.Fatal("partial receipt survived")
			}
			var after int64
			_ = f.s.Pool.QueryRow(ctx, `SELECT commit_rev FROM datasets WHERE id=$1`, f.p.DatasetID).Scan(&after)
			if before != after {
				t.Fatal("failed commit advanced revision")
			}
			if _, err := f.s.Pool.Exec(ctx, `DROP TRIGGER completeness_fault ON `+table+`; DROP FUNCTION completeness_fault()`); err != nil {
				t.Fatal(err)
			}
			if _, err := commitCompleteness(t, f, m); err != nil {
				t.Fatal("retry failed", err)
			}
		})
	}
}
func TestWorkoutCompletenessRealCoverageConsumptionAndCollectorFencing(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if err := admin.QueueReadinessCandidate(ctx, f.s, f.p.DatasetID, syncing.NewID(), "synthetic verified transport"); err != nil {
		t.Fatal(err)
	}
	m := completenessManifest(f, nil)
	if _, err := commitCompleteness(t, f, m); err != nil {
		t.Fatal(err)
	}
	runReadinessLatest(t, f)
	r, _ := readReadiness(t, f)
	if !r.Coverage.CaughtUp || r.Coverage.Synthetic || strings.Contains(strings.Join(r.Reasons, ","), "load_sync_incomplete") || r.Baseline.KnownZeroDays != 42 {
		t.Fatal("verified empty running coverage not consumed")
	}
	stale := m
	stale.ManifestID = syncing.NewID()
	if _, err := commitCompleteness(t, f, stale); err == nil {
		t.Fatal("stale generation accepted")
	}
	newCollector := syncing.NewID()
	if _, err := f.s.Pool.Exec(ctx, `UPDATE collector_leases SET collector_epoch=$2 WHERE dataset_id=$1`, f.p.DatasetID, newCollector); err != nil {
		t.Fatal(err)
	}
	if _, err := commitCompleteness(t, f, m); err == nil {
		t.Fatal("old collector replay accepted")
	}
	runReadinessLatest(t, f)
	r, _ = readReadiness(t, f)
	if r.Coverage.CaughtUp || !strings.Contains(strings.Join(r.Reasons, ","), "load_sync_incomplete") {
		t.Fatal("new collector inherited old completeness")
	}
}
func TestWorkoutCompletenessHTTPAndRuntimeRoles(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	suffix := strings.ReplaceAll(syncing.NewID(), "-", "")
	ar, wr := "rai_api_"+suffix, "rai_worker_"+suffix
	raw, err := os.ReadFile("../../../deploy/postgres/roles.sql")
	if err != nil {
		t.Fatal(err)
	}
	grants := strings.ReplaceAll(strings.ReplaceAll(string(raw), "rai_api", ar), "rai_worker", wr)
	if _, err = f.s.Pool.Exec(ctx, grants); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, e := f.s.Pool.Exec(context.Background(), "DROP OWNED BY "+pgx.Identifier{ar}.Sanitize()+", "+pgx.Identifier{wr}.Sanitize()+"; DROP ROLE "+pgx.Identifier{ar}.Sanitize()+", "+pgx.Identifier{wr}.Sanitize())
		if e != nil {
			t.Error(e)
		}
	})
	api, err := postgres.Open(ctx, f.s.Pool.Config().ConnString()+"&options=-crole%3D"+ar, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(api.Pool.Close)
	worker, err := postgres.Open(ctx, f.s.Pool.Config().ConnString()+"&options=-crole%3D"+wr, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(worker.Pool.Close)
	if err = api.RuntimeRoleSafe(ctx); err != nil {
		t.Fatal(err)
	}
	m := completenessManifest(f, nil)
	body, _ := json.Marshal(m)
	req := httptest.NewRequest(http.MethodPost, "/v2/sync/workout-completeness", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	httpapi.New(api, analysisLog).ServeHTTP(response, req)
	if response.Code != 200 {
		t.Fatal("restricted API", response.Code, response.Body.String())
	}
	for _, store := range []*postgres.Store{api, worker} {
		for _, q := range []string{`UPDATE workout_sync_completeness SET generation=2`, `DELETE FROM workout_sync_completeness`} {
			if _, err = store.Pool.Exec(ctx, q); err == nil {
				t.Fatal("immutable lineage mutable")
			}
		}
	}
	if _, err = worker.Pool.Exec(ctx, `INSERT INTO workout_sync_completeness SELECT * FROM workout_sync_completeness`); err == nil {
		t.Fatal("worker can forge completeness")
	}
	if _, err = f.s.Pool.Exec(ctx, `UPDATE workout_sync_completeness SET generation=2`); err == nil {
		t.Fatal("owner bypassed immutable history")
	}
	var coded *syncing.Error
	changed := m
	changed.CreatedAtUs++
	_, err = commitCompleteness(t, f, changed)
	if !errors.As(err, &coded) || coded.Code != "manifest_identity_conflict" {
		t.Fatal("identity conflict", err)
	}
}
