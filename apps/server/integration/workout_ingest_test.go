//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http/httptest"
	"net/textproto"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/admin"
	"rai/server/internal/httpapi"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
)

// ingestRaw builds the exact raw protocol-6 bytes a client would upload.
func ingestRaw(t *testing.T, datasetID, datasetEpoch, collectorEpoch, workoutID string, value float64) []byte {
	t.Helper()
	start := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	startUs := start.UnixMicro()
	source := `{"bundleID":"com.apple.health","productType":"Watch7,1"}`
	return []byte(fmt.Sprintf(`{
		"protocolVersion":6,
		"datasetID":%q,"datasetEpoch":%q,"collectorEpoch":%q,"workoutID":%q,
		"startUs":%d,"endUs":%d,
		"source":%s,
		"workout":{"activityType":37,"durationUs":60000000},
		"streams":[{"type":%q,"unit":"count/min","status":"complete",
			"samples":[[0,500000,%g]]}],
		"routes":[]
	}`, datasetID, datasetEpoch, collectorEpoch, workoutID,
		startUs, start.Add(time.Minute).UnixMicro(), source, kind, value))
}

func ingestEnvelope(t *testing.T, raw []byte) syncing.WorkoutIngestEnvelope {
	t.Helper()
	envelope, err := syncing.DecodeWorkoutIngestEnvelope(raw)
	if err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	return envelope
}

func acceptIngest(t *testing.T, f *fixture, p syncing.Principal, raw []byte) syncing.WorkoutIngestReceipt {
	t.Helper()
	body, err := f.s.AcceptWorkoutIngest(context.Background(), p, ingestEnvelope(t, raw), raw)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	var receipt syncing.WorkoutIngestReceipt
	if err = json.Unmarshal(body, &receipt); err != nil {
		t.Fatalf("decode acceptance: %v (%s)", err, body)
	}
	return receipt
}

func ingestStatus(t *testing.T, s *postgres.Store, p syncing.Principal, workoutID string) syncing.WorkoutIngestStatus {
	t.Helper()
	status, err := s.WorkoutIngest(context.Background(), p, workoutID)
	if err != nil {
		t.Fatalf("status %s: %v", workoutID, err)
	}
	return status
}

func processIngest(t *testing.T, s *postgres.Store) syncing.WorkoutIngestOutcome {
	t.Helper()
	outcome, err := s.ProcessNextWorkoutIngest(context.Background())
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if !outcome.Claimed {
		t.Fatalf("ingest not claimed: %+v", outcome)
	}
	return outcome
}

// processAllIngests drains the due queue for a test that only needs terminal
// states, returning the outcomes in claim order.
func processAllIngests(t *testing.T, s *postgres.Store) []syncing.WorkoutIngestOutcome {
	t.Helper()
	var outcomes []syncing.WorkoutIngestOutcome
	for i := 0; i < 20; i++ {
		outcome, err := s.ProcessNextWorkoutIngest(context.Background())
		if err != nil {
			t.Fatalf("process: %v", err)
		}
		if !outcome.Claimed {
			return outcomes
		}
		outcomes = append(outcomes, outcome)
	}
	t.Fatal("ingest queue did not drain")
	return nil
}

func ingestRow(t *testing.T, s *postgres.Store, dataset, workoutID string) (raw []byte, state string, attempts int, next *time.Time) {
	t.Helper()
	err := s.Pool.QueryRow(context.Background(), `SELECT raw_json,state,attempts,next_attempt_at FROM workout_ingests
		WHERE dataset_id=$1 AND workout_uuid=$2`, dataset, workoutID).Scan(&raw, &state, &attempts, &next)
	if err != nil {
		t.Fatalf("ingest row: %v", err)
	}
	return raw, state, attempts, next
}

func ingestRowCount(t *testing.T, s *postgres.Store, dataset string) int {
	t.Helper()
	var n int
	if err := s.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM workout_ingests WHERE dataset_id=$1`, dataset).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func countFor(t *testing.T, s *postgres.Store, table, dataset, workoutID string) int {
	t.Helper()
	var n int
	if err := s.Pool.QueryRow(context.Background(), `SELECT count(*) FROM `+pgx.Identifier{table}.Sanitize()+
		` x WHERE x.dataset_id=$1 AND x.workout_uuid=$2`, dataset, workoutID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestWorkoutUUIDIngestAcceptCommitReplay(t *testing.T) {
	f := setup(t)
	workoutID := syncing.NewID()
	raw := ingestRaw(t, f.p.DatasetID, f.enrollment.DatasetEpoch, f.enrollment.CollectorEpoch, workoutID, 121)

	receipt := acceptIngest(t, f, f.p, raw)
	if receipt.WorkoutID != workoutID || receipt.IngestStatus != "accepted" || receipt.AcceptedAt.IsZero() {
		t.Fatalf("acceptance: %+v", receipt)
	}
	if status := ingestStatus(t, f.s, f.p, workoutID); status.IngestStatus != "accepted" || status.AcceptedAt == nil {
		t.Fatalf("status before worker: %+v", status)
	}
	stored, state, attempts, _ := ingestRow(t, f.s, f.p.DatasetID, workoutID)
	if !bytes.Equal(stored, raw) || state != "accepted" || attempts != 0 {
		t.Fatalf("stored raw not exact: state=%s attempts=%d equal=%v", state, attempts, bytes.Equal(stored, raw))
	}

	outcome := processIngest(t, f.s)
	if outcome.Status != "committed" || outcome.WorkoutID != workoutID {
		t.Fatalf("worker outcome: %+v", outcome)
	}
	status := ingestStatus(t, f.s, f.p, workoutID)
	if status.IngestStatus != "committed" || status.AnalysisStatus != "queued" || len(status.AnalysisJobIDs) != 1 ||
		status.CommitRevision == nil || *status.CommitRevision < 1 {
		t.Fatalf("committed status: %+v", status)
	}
	if count(t, f, "health_samples") != 1 || count(t, f, "workout_samples") != 1 || count(t, f, "workouts") != 1 {
		t.Fatalf("facts missing: samples=%d associations=%d workouts=%d",
			count(t, f, "health_samples"), count(t, f, "workout_samples"), count(t, f, "workouts"))
	}
	stored, state, _, _ = ingestRow(t, f.s, f.p.DatasetID, workoutID)
	if state != "committed" || !bytes.Equal(stored, raw) {
		t.Fatalf("committed row: state=%s raw_retained=%v", state, bytes.Equal(stored, raw))
	}

	// Different bytes for the same UUID replay the first acceptance and never
	// replace the stored payload.
	raw2 := ingestRaw(t, f.p.DatasetID, f.enrollment.DatasetEpoch, f.enrollment.CollectorEpoch, workoutID, 199)
	replay := acceptIngest(t, f, f.p, raw2)
	if replay.IngestStatus != "accepted" || !replay.AcceptedAt.Equal(receipt.AcceptedAt) {
		t.Fatalf("replay changed acceptance: %+v", replay)
	}
	if ingestRowCount(t, f.s, f.p.DatasetID) != 1 {
		t.Fatal("replay created a second ingest row")
	}
	stored, _, _, _ = ingestRow(t, f.s, f.p.DatasetID, workoutID)
	if !bytes.Equal(stored, raw) {
		t.Fatal("replay replaced the original raw payload")
	}
	if count(t, f, "health_samples") != 1 {
		t.Fatal("replay wrote facts")
	}
}

func TestWorkoutUUIDIngestHTTPAcceptAndStatus(t *testing.T) {
	f := setup(t)
	workoutID := syncing.NewID()
	raw := ingestRaw(t, f.p.DatasetID, f.enrollment.DatasetEpoch, f.enrollment.CollectorEpoch, workoutID, 121)
	server := httpapi.New(f.s, slog.New(slog.NewJSONHandler(io.Discard, nil)))

	post := func(id string, body []byte) *httptest.ResponseRecorder {
		var wire bytes.Buffer
		writer := multipart.NewWriter(&wire)
		part, err := writer.CreatePart(textproto.MIMEHeader{
			"Content-Disposition": {`form-data; name="file"; filename="workout.json"`},
			"Content-Type":        {"application/json"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write(body); err != nil {
			t.Fatal(err)
		}
		if err = writer.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/v2/sync/workout-ingests/"+id, &wire)
		req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, req)
		return recorder
	}
	status := func(id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/v2/sync/workout-ingests/"+id, nil)
		req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, req)
		return recorder
	}

	first := post(workoutID, raw)
	if first.Code != 200 {
		t.Fatalf("POST: %d %s", first.Code, first.Body.String())
	}
	var accepted syncing.WorkoutIngestReceipt
	if err := json.Unmarshal(first.Body.Bytes(), &accepted); err != nil ||
		accepted.IngestStatus != "accepted" || accepted.WorkoutID != workoutID {
		t.Fatalf("acceptance body: %v %s", err, first.Body.String())
	}
	if replay := post(workoutID, ingestRaw(t, f.p.DatasetID, f.enrollment.DatasetEpoch,
		f.enrollment.CollectorEpoch, workoutID, 199)); replay.Code != 200 {
		t.Fatalf("replay POST: %d %s", replay.Code, replay.Body.String())
	} else {
		var receipt syncing.WorkoutIngestReceipt
		if err := json.Unmarshal(replay.Body.Bytes(), &receipt); err != nil ||
			!receipt.AcceptedAt.Equal(accepted.AcceptedAt) {
			t.Fatalf("replay acceptance changed: %v %s", err, replay.Body.String())
		}
	}
	if mismatch := post(syncing.NewID(), raw); mismatch.Code != 422 ||
		!bytes.Contains(mismatch.Body.Bytes(), []byte(`"workout_ingest_mismatch"`)) {
		t.Fatalf("path/body mismatch: %d %s", mismatch.Code, mismatch.Body.String())
	}

	before := status(workoutID)
	if before.Code != 200 || !bytes.Contains(before.Body.Bytes(), []byte(`"ingestStatus":"accepted"`)) {
		t.Fatalf("status before worker: %d %s", before.Code, before.Body.String())
	}
	if outcome := processIngest(t, f.s); outcome.Status != "committed" {
		t.Fatalf("worker: %+v", outcome)
	}
	after := status(workoutID)
	if after.Code != 200 {
		t.Fatalf("status after worker: %d %s", after.Code, after.Body.String())
	}
	var committed syncing.WorkoutIngestStatus
	if err := json.Unmarshal(after.Body.Bytes(), &committed); err != nil {
		t.Fatal(err)
	}
	if committed.IngestStatus != "committed" || committed.AnalysisStatus != "queued" ||
		len(committed.AnalysisJobIDs) != 1 || committed.CommitRevision == nil {
		t.Fatalf("committed status: %+v", committed)
	}
	missing := status(syncing.NewID())
	if missing.Code != 404 || !bytes.Contains(missing.Body.Bytes(), []byte(`"workout_ingest_not_found"`)) {
		t.Fatalf("missing status: %d %s", missing.Code, missing.Body.String())
	}
}

func TestWorkoutUUIDIngestDatasetScoped(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	workoutID := syncing.NewID()
	rawA := ingestRaw(t, f.p.DatasetID, f.enrollment.DatasetEpoch, f.enrollment.CollectorEpoch, workoutID, 121)
	acceptIngest(t, f, f.p, rawA)
	if outcome := processIngest(t, f.s); outcome.Status != "committed" {
		t.Fatalf("dataset A worker: %+v", outcome)
	}

	credential := preparedCredential(t)
	result, err := admin.NewDataset(ctx, f.s.Pool, newDatasetRequest(f), credential)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.s.Authenticate(ctx, credential.Token)
	if err != nil {
		t.Fatal(err)
	}
	if b.DatasetID != result.Enrollment.DatasetID || b.DatasetID == f.p.DatasetID {
		t.Fatalf("dataset B identity: %+v", b)
	}
	if _, err = f.s.WorkoutIngest(ctx, b, workoutID); err == nil {
		t.Fatal("dataset B sees dataset A ingest status")
	} else {
		requireSyncCode(t, err, "workout_ingest_not_found")
	}
	// The same UUID is a fresh identity in dataset B, not an already-imported
	// duplicate of dataset A.
	rawB := ingestRaw(t, b.DatasetID, credential.DatasetEpoch, credential.CollectorEpoch, workoutID, 144)
	receipt := acceptIngest(t, f, b, rawB)
	if receipt.IngestStatus != "accepted" {
		t.Fatalf("dataset B acceptance: %+v", receipt)
	}
	var outcome syncing.WorkoutIngestOutcome
	for i := 0; i < 5; i++ {
		outcome = processIngest(t, f.s)
		if outcome.WorkoutID == workoutID {
			break
		}
	}
	if outcome.Status != "committed" {
		t.Fatalf("dataset B worker: %+v", outcome)
	}
	if status := ingestStatus(t, f.s, b, workoutID); status.IngestStatus != "committed" {
		t.Fatalf("dataset B status: %+v", status)
	}
	if got := countFor(t, f.s, "workouts", b.DatasetID, workoutID); got != 1 {
		t.Fatalf("dataset B workouts=%d", got)
	}
}

func TestWorkoutUUIDIngestLeaseRecoveryAndInvalidRaw(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	leased := syncing.NewID()
	acceptIngest(t, f, f.p, ingestRaw(t, f.p.DatasetID, f.enrollment.DatasetEpoch, f.enrollment.CollectorEpoch, leased, 121))

	if _, err := f.s.Pool.Exec(ctx, `UPDATE workout_ingests SET attempts=1,
		lease_until=clock_timestamp()+interval '1 hour' WHERE dataset_id=$1 AND workout_uuid=$2`,
		f.p.DatasetID, leased); err != nil {
		t.Fatal(err)
	}
	if outcome, err := f.s.ProcessNextWorkoutIngest(ctx); err != nil || outcome.Claimed {
		t.Fatalf("valid lease was claimed: %+v %v", outcome, err)
	}
	if _, err := f.s.Pool.Exec(ctx, `UPDATE workout_ingests SET attempts=3,
		lease_until=clock_timestamp()-interval '1 second' WHERE dataset_id=$1 AND workout_uuid=$2`,
		f.p.DatasetID, leased); err != nil {
		t.Fatal(err)
	}
	if n, err := f.s.RecoverExpiredWorkoutIngests(ctx); err != nil || n != 1 {
		t.Fatalf("recovery: %d %v", n, err)
	}
	status := ingestStatus(t, f.s, f.p, leased)
	if status.IngestStatus != "rejected" || status.ErrorCode != "processing_attempts_exhausted" {
		t.Fatalf("exhausted status: %+v", status)
	}
	if raw, state, attempts, _ := ingestRow(t, f.s, f.p.DatasetID, leased); raw == nil || state != "rejected" || attempts != 3 {
		t.Fatalf("exhausted row: state=%s attempts=%d raw=%v", state, attempts, raw)
	}

	invalidID := syncing.NewID()
	invalidRaw := []byte(`{"protocolVersion":6}`)
	envelope := syncing.WorkoutIngestEnvelope{DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch,
		CollectorEpoch: f.enrollment.CollectorEpoch, WorkoutID: invalidID, ProtocolVersion: 6}
	if _, err := f.s.AcceptWorkoutIngest(ctx, f.p, envelope, invalidRaw); err != nil {
		t.Fatal(err)
	}
	outcomes := processAllIngests(t, f.s)
	if len(outcomes) != 1 || outcomes[0].Status != "rejected" || outcomes[0].ErrorCode != "unsupported_payload" {
		t.Fatalf("invalid raw outcome: %+v", outcomes)
	}
	if raw, state, _, _ := ingestRow(t, f.s, f.p.DatasetID, invalidID); raw == nil || state != "rejected" {
		t.Fatalf("invalid raw row: state=%s raw=%v", state, raw)
	}
	if status := ingestStatus(t, f.s, f.p, invalidID); status.ErrorCode != "unsupported_payload" {
		t.Fatalf("invalid raw status: %+v", status)
	}

	mismatched := syncing.NewID()
	foreignRaw := ingestRaw(t, f.p.DatasetID, f.enrollment.DatasetEpoch, f.enrollment.CollectorEpoch, syncing.NewID(), 121)
	mismatchEnvelope := ingestEnvelope(t, foreignRaw)
	mismatchEnvelope.WorkoutID = mismatched
	if _, err := f.s.AcceptWorkoutIngest(ctx, f.p, mismatchEnvelope, foreignRaw); err != nil {
		t.Fatal(err)
	}
	outcomes = processAllIngests(t, f.s)
	if len(outcomes) != 1 || outcomes[0].Status != "rejected" || outcomes[0].ErrorCode != "workout_ingest_mismatch" {
		t.Fatalf("mismatched outcome: %+v", outcomes)
	}
	if countFor(t, f.s, "workouts", f.p.DatasetID, mismatched) != 0 {
		t.Fatal("mismatched raw wrote a workout")
	}
}

func TestWorkoutUUIDIngestWorkerFenced(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	workoutID := syncing.NewID()
	acceptIngest(t, f, f.p, ingestRaw(t, f.p.DatasetID, f.enrollment.DatasetEpoch, f.enrollment.CollectorEpoch, workoutID, 121))
	if _, err := f.s.Pool.Exec(ctx, `UPDATE collector_leases SET collector_epoch=gen_random_uuid()
		WHERE dataset_id=$1 AND device_id=$2`, f.p.DatasetID, f.p.DeviceID); err != nil {
		t.Fatal(err)
	}
	outcome := processIngest(t, f.s)
	if outcome.Status != "rejected" || outcome.ErrorCode != "collector_fenced" {
		t.Fatalf("fenced worker: %+v", outcome)
	}
	if countFor(t, f.s, "workouts", f.p.DatasetID, workoutID) != 0 ||
		countFor(t, f.s, "workout_samples", f.p.DatasetID, workoutID) != 0 {
		t.Fatal("fenced worker wrote facts")
	}
	if status := ingestStatus(t, f.s, f.p, workoutID); status.IngestStatus != "rejected" || status.ErrorCode != "collector_fenced" {
		t.Fatalf("fenced status: %+v", status)
	}
	if raw, state, _, _ := ingestRow(t, f.s, f.p.DatasetID, workoutID); raw == nil || state != "rejected" {
		t.Fatalf("fenced row: state=%s raw=%v", state, raw)
	}
}

func runtimeRoles(t *testing.T, f *fixture) (api, worker *postgres.Store, workerRole string) {
	t.Helper()
	ctx := context.Background()
	suffix := strings.ReplaceAll(syncing.NewID(), "-", "")
	apiRole, workerRole := "rai_api_"+suffix, "rai_worker_"+suffix
	raw, err := os.ReadFile("../../../deploy/postgres/roles.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ReplaceAll(strings.ReplaceAll(string(raw), "rai_api", apiRole), "rai_worker", workerRole)
	if _, err = f.s.Pool.Exec(ctx, sql); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, e := f.s.Pool.Exec(context.Background(), "DROP OWNED BY "+pgx.Identifier{apiRole}.Sanitize()+", "+
			pgx.Identifier{workerRole}.Sanitize()+"; DROP ROLE "+pgx.Identifier{apiRole}.Sanitize()+", "+
			pgx.Identifier{workerRole}.Sanitize()); e != nil {
			t.Error(e)
		}
	})
	open := func(role string) *postgres.Store {
		dsn := f.s.Pool.Config().ConnString()
		sep := "&"
		if !strings.Contains(dsn, "?") {
			sep = "?"
		}
		s, e := postgres.Open(ctx, dsn+sep+"options=-crole%3D"+role, 2)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(s.Pool.Close)
		return s
	}
	api, worker = open(apiRole), open(workerRole)
	if err = api.RuntimeRoleSafe(ctx); err != nil {
		t.Fatal(err)
	}
	if err = worker.RuntimeRoleSafe(ctx); err != nil {
		t.Fatal(err)
	}
	return api, worker, workerRole
}

func TestWorkoutUUIDIngestRestrictedRuntimeRoles(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	api, worker, _ := runtimeRoles(t, f)
	workoutID := syncing.NewID()
	raw := ingestRaw(t, f.p.DatasetID, f.enrollment.DatasetEpoch, f.enrollment.CollectorEpoch, workoutID, 121)
	body, err := api.AcceptWorkoutIngest(ctx, f.p, ingestEnvelope(t, raw), raw)
	if err != nil {
		t.Fatalf("API role acceptance: %v", err)
	}
	var receipt syncing.WorkoutIngestReceipt
	if err = json.Unmarshal(body, &receipt); err != nil || receipt.IngestStatus != "accepted" {
		t.Fatalf("API receipt: %v %s", err, body)
	}
	if _, err = api.ProcessNextWorkoutIngest(ctx); err == nil {
		t.Fatal("API role claimed ingest work")
	}
	outcome, err := worker.ProcessNextWorkoutIngest(ctx)
	if err != nil || !outcome.Claimed || outcome.Status != "committed" {
		t.Fatalf("worker role process: %+v %v", outcome, err)
	}
	status, err := api.WorkoutIngest(ctx, f.p, workoutID)
	if err != nil || status.IngestStatus != "committed" || len(status.AnalysisJobIDs) != 1 {
		t.Fatalf("API status: %+v %v", status, err)
	}
	if count(t, f, "health_samples") != 1 || count(t, f, "workout_samples") != 1 || count(t, f, "workouts") != 1 {
		t.Fatal("restricted worker facts missing")
	}
	fresh := syncing.NewID()
	freshRaw := ingestRaw(t, f.p.DatasetID, f.enrollment.DatasetEpoch, f.enrollment.CollectorEpoch, fresh, 122)
	if _, err = worker.AcceptWorkoutIngest(ctx, f.p, ingestEnvelope(t, freshRaw), freshRaw); err == nil {
		t.Fatal("worker role accepted a new ingest")
	}
}

func TestWorkoutUUIDIngestTransientRetryExhaustion(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	api, worker, workerRole := runtimeRoles(t, f)
	_ = api
	failing := syncing.NewID()
	raw := ingestRaw(t, f.p.DatasetID, f.enrollment.DatasetEpoch, f.enrollment.CollectorEpoch, failing, 121)
	if _, err := f.s.AcceptWorkoutIngest(ctx, f.p, ingestEnvelope(t, raw), raw); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(ctx, "REVOKE INSERT ON health_samples FROM "+pgx.Identifier{workerRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= syncing.WorkoutIngestAttempts; attempt++ {
		outcome, err := worker.ProcessNextWorkoutIngest(ctx)
		if err != nil || !outcome.Claimed {
			t.Fatalf("attempt %d: %+v %v", attempt, outcome, err)
		}
		if attempt < syncing.WorkoutIngestAttempts {
			if outcome.Status != "retry" || outcome.ErrorCode == "" {
				t.Fatalf("attempt %d outcome: %+v", attempt, outcome)
			}
			if _, err = f.s.Pool.Exec(ctx, `UPDATE workout_ingests SET next_attempt_at=clock_timestamp()
				WHERE dataset_id=$1 AND workout_uuid=$2`, f.p.DatasetID, failing); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if outcome.Status != "rejected" || outcome.ErrorCode != "processing_attempts_exhausted" {
			t.Fatalf("terminal outcome: %+v", outcome)
		}
	}
	if _, err := f.s.Pool.Exec(ctx, "GRANT INSERT ON health_samples TO "+pgx.Identifier{workerRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	status := ingestStatus(t, f.s, f.p, failing)
	if status.IngestStatus != "rejected" || status.ErrorCode != "processing_attempts_exhausted" {
		t.Fatalf("exhausted status: %+v", status)
	}
	if countFor(t, f.s, "workouts", f.p.DatasetID, failing) != 0 ||
		countFor(t, f.s, "workout_samples", f.p.DatasetID, failing) != 0 {
		t.Fatal("exhausted ingest left facts")
	}

	recovered := syncing.NewID()
	recoveredRaw := ingestRaw(t, f.p.DatasetID, f.enrollment.DatasetEpoch, f.enrollment.CollectorEpoch, recovered, 131)
	if _, err := f.s.AcceptWorkoutIngest(ctx, f.p, ingestEnvelope(t, recoveredRaw), recoveredRaw); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(ctx, "REVOKE INSERT ON health_samples FROM "+pgx.Identifier{workerRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	outcome, err := worker.ProcessNextWorkoutIngest(ctx)
	if err != nil || outcome.Status != "retry" {
		t.Fatalf("first transient attempt: %+v %v", outcome, err)
	}
	if _, err = f.s.Pool.Exec(ctx, "GRANT INSERT ON health_samples TO "+pgx.Identifier{workerRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(ctx, `UPDATE workout_ingests SET next_attempt_at=clock_timestamp()
		WHERE dataset_id=$1 AND workout_uuid=$2`, f.p.DatasetID, recovered); err != nil {
		t.Fatal(err)
	}
	outcome, err = worker.ProcessNextWorkoutIngest(ctx)
	if err != nil || outcome.Status != "committed" {
		t.Fatalf("recovered attempt: %+v %v", outcome, err)
	}
	if status := ingestStatus(t, f.s, f.p, recovered); status.IngestStatus != "committed" || status.AnalysisStatus != "queued" {
		t.Fatalf("recovered status: %+v", status)
	}
	if countFor(t, f.s, "workouts", f.p.DatasetID, recovered) != 1 {
		t.Fatal("recovered ingest missing workout")
	}
}
