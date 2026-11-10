//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/admin"
	"rai/server/internal/httpapi"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
)

// Creates schema zero in the disposable root cluster. Never accepts a runtime DSN.
func freshRecoveryDatabase(t *testing.T, migrate bool) *fixture {
	t.Helper()
	ctx := context.Background()
	dsn := os.Getenv("RAI_TEST_DATABASE_URL")
	cfg, e := pgx.ParseConfig(dsn)
	if e != nil || cfg.Database != "rai_test" {
		t.Fatal("disposable rai_test cluster required")
	}
	base, e := pgx.ConnectConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	name := "rai_test_" + strings.ReplaceAll(syncing.NewID(), "-", "")
	if _, e = base.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); e != nil {
		t.Fatal(e)
	}
	u, e := url.Parse(dsn)
	if e != nil {
		t.Fatal(e)
	}
	u.Path = "/" + name
	s, e := postgres.Open(ctx, u.String(), 8)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		s.Pool.Close()
		if _, e := base.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); e != nil {
			t.Error(e)
		}
		base.Close(context.Background())
	})
	if migrate {
		if e = admin.Migrate(ctx, u.String()); e != nil {
			t.Fatal(e)
		}
	}
	return &fixture{s: s}
}
func bootstrapRecovery(t *testing.T, f *fixture) ([]string, string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "private.json")
	args := []string{"--operation-id", syncing.NewID(), "--actor", "synthetic-operator", "--base-url", "https://synthetic.invalid", "--credential-file", file}
	var out bytes.Buffer
	if e := admin.InitCommand(context.Background(), f.s.Pool.Config().ConnString(), args, &out); e != nil {
		t.Fatal(e)
	}
	readRecoveryCredential(t, f, file)
	if bytes.Contains(out.Bytes(), []byte(f.enrollment.Token)) {
		t.Fatal("credential leaked to stdout")
	}
	return args, file
}
func readRecoveryCredential(t *testing.T, f *fixture, file string) {
	t.Helper()
	b, e := os.ReadFile(file)
	if e != nil {
		t.Fatal(e)
	}
	var packageValue struct {
		Enrollment admin.Enrollment `json:"enrollment"`
	}
	if e = json.Unmarshal(b, &packageValue); e != nil {
		t.Fatal(e)
	}
	f.enrollment = packageValue.Enrollment
	f.p, e = f.s.Authenticate(context.Background(), f.enrollment.Token)
	if e != nil {
		t.Fatal(e)
	}
}
func independentRecoveryIdentity(t *testing.T, a, b admin.Enrollment) {
	t.Helper()
	for name, pair := range map[string][2]string{"dataset": {a.DatasetID, b.DatasetID}, "epoch": {a.DatasetEpoch, b.DatasetEpoch}, "device": {a.DeviceID, b.DeviceID}, "token": {a.Token, b.Token}, "collector": {a.CollectorEpoch, b.CollectorEpoch}, "service": {a.ServiceID, b.ServiceID}} {
		if pair[0] == "" || pair[1] == "" || pair[0] == pair[1] {
			t.Fatal("inherited identity", name)
		}
	}
}
func assertOldIdentityFenced(t *testing.T, target *fixture, old *fixture, raw []byte) {
	t.Helper()
	handler := httpapi.New(target.s, analysisLog)
	// Each case models a separate recovery attempt; give it a fresh admission window.
	if _, e := target.s.Pool.Exec(context.Background(), `DELETE FROM sync_request_budgets`); e != nil {
		t.Fatal(e)
	}
	before := datasetSnapshot(t, target, target.p.DatasetID)
	for _, method := range []string{"GET", "POST"} {
		var q *http.Request
		if method == "GET" {
			q = httptest.NewRequest(method, "/v2/handshake", nil)
		} else {
			q = httptest.NewRequest(method, "/v2/sync/workout-ingests/"+syncing.NewID(), bytes.NewReader(raw))
			q.Header.Set("Content-Type", "application/json")
		}
		q.Header.Set("Authorization", "Bearer "+old.enrollment.Token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, q)
		if w.Code != 401 {
			t.Fatalf("unknown old token %s status=%d", method, w.Code)
		}
	}
	q := httptest.NewRequest("GET", "/v2/sync/workout-ingests/"+syncing.NewID(), nil)
	q.Header.Set("Authorization", "Bearer "+target.enrollment.Token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, q)
	if w.Code != 404 {
		t.Fatalf("recovered token ingest status=%d body=%s", w.Code, w.Body.String())
	}
	after := datasetSnapshot(t, target, target.p.DatasetID)
	for table, rows := range before {
		if rows != after[table] {
			t.Fatal("old identity mutated recovered dataset", table)
		}
	}
}
func TestFreshServerRecoveryCreatesIndependentIdentity(t *testing.T) {
	old := setup(t)
	bundle, raw := testWorkoutBundle(t, old, 1)
	if _, e := old.s.ImportWorkoutBundle(context.Background(), old.p, bundle, raw); e != nil {
		t.Fatal(e)
	}
	f := freshRecoveryDatabase(t, false)
	var tables int
	if e := f.s.Pool.QueryRow(context.Background(), `SELECT count(*) FROM information_schema.tables WHERE table_schema='public'`).Scan(&tables); e != nil || tables != 0 {
		t.Fatal("schema not zero")
	}
	if e := admin.Migrate(context.Background(), f.s.Pool.Config().ConnString()); e != nil {
		t.Fatal(e)
	}
	bootstrapRecovery(t, f)
	independentRecoveryIdentity(t, old.enrollment, f.enrollment)
	t.Run("RecoveredDatasetStartsEmpty", func(t *testing.T) {
		for _, table := range []string{"datasets", "devices", "collector_leases"} {
			if count(t, f, table) != 1 {
				t.Fatal(table)
			}
		}
		for table, rows := range datasetSnapshot(t, f, f.p.DatasetID) {
			if table != "devices" && table != "collector_leases" && rows != "[]" {
				t.Fatal("nonempty recovered namespace", table)
			}
		}
	})
	t.Run("RecoveredDatasetStartsIndependentRevision", func(t *testing.T) {
		m, e := f.s.Metadata(context.Background(), f.p)
		if e != nil || m.CommitRevision != 0 {
			t.Fatal("revision inherited")
		}
	})
	t.Run("OldTokenCannotAuthenticateRecoveredServer", func(t *testing.T) { assertOldIdentityFenced(t, f, old, raw) })
	t.Run("OldBundleCannotEnterRecoveredDataset", func(t *testing.T) { assertOldIdentityFenced(t, f, old, raw) })
	t.Run("RecoveryAdminAuditIsDurable", func(t *testing.T) {
		var n int
		e := f.s.Pool.QueryRow(context.Background(), `SELECT count(*) FROM admin_operations WHERE operation_type='disaster_recovery' AND source_dataset IS NULL AND target_dataset=$1 AND status='committed' AND actor='synthetic-operator' AND committed_at IS NOT NULL`, f.p.DatasetID).Scan(&n)
		if e != nil || n != 1 {
			t.Fatal("recovery audit missing")
		}
	})
}
func TestFreshServerInitIsIdempotent(t *testing.T) {
	f := freshRecoveryDatabase(t, true)
	args, file := bootstrapRecovery(t, f)
	original, _ := os.ReadFile(file)
	var wg sync.WaitGroup
	results := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- admin.InitCommand(context.Background(), f.s.Pool.Config().ConnString(), args, &bytes.Buffer{})
		}()
	}
	wg.Wait()
	close(results)
	for e := range results {
		if e != nil {
			t.Fatal(e)
		}
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(original, after) || count(t, f, "datasets") != 1 || count(t, f, "admin_operations") != 1 {
		t.Fatal("retry changed identity")
	}
	another := append([]string{}, args...)
	another[1] = syncing.NewID()
	another[len(another)-1] = filepath.Join(t.TempDir(), "other.json")
	if e := admin.InitCommand(context.Background(), f.s.Pool.Config().ConnString(), another, &bytes.Buffer{}); e == nil {
		t.Fatal("second init accepted")
	}
}
func TestFreshServerLostResponseRecoversCredential(t *testing.T) {
	f := freshRecoveryDatabase(t, true)
	file := filepath.Join(t.TempDir(), "private.json")
	args := []string{"--operation-id", syncing.NewID(), "--actor", "synthetic-operator", "--base-url", "https://synthetic.invalid", "--credential-file", file}
	// Fail the last INSERT: the private package survives and every DB write rolls back.
	_, e := f.s.Pool.Exec(context.Background(), `CREATE FUNCTION recovery_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic failure'; END $$; CREATE TRIGGER recovery_fail BEFORE INSERT ON admin_operations FOR EACH ROW EXECUTE FUNCTION recovery_fail()`)
	if e != nil {
		t.Fatal(e)
	}
	if e = admin.InitCommand(context.Background(), f.s.Pool.Config().ConnString(), args, &bytes.Buffer{}); e == nil {
		t.Fatal("fault ignored")
	}
	prepared, e := os.ReadFile(file)
	if e != nil || count(t, f, "datasets") != 0 || count(t, f, "devices") != 0 || count(t, f, "admin_operations") != 0 {
		t.Fatal("prepare not durable / transaction not atomic")
	}
	if _, e = f.s.Pool.Exec(context.Background(), `DROP TRIGGER recovery_fail ON admin_operations`); e != nil {
		t.Fatal(e)
	}
	if e = admin.InitCommand(context.Background(), f.s.Pool.Config().ConnString(), args, lostOutput{}); e == nil || count(t, f, "datasets") != 1 {
		t.Fatal("lost stdout did not follow commit")
	}
	var out bytes.Buffer
	if e = admin.InitCommand(context.Background(), f.s.Pool.Config().ConnString(), args, &out); e != nil {
		t.Fatal(e)
	}
	var result admin.NewDatasetResult
	if e = json.Unmarshal(out.Bytes(), &result); e != nil || !result.Replayed || result.Enrollment.Token != "" {
		t.Fatal("not same committed operation")
	}
	readRecoveryCredential(t, f, file)
	after, _ := os.ReadFile(file)
	if sha256.Sum256(prepared) != sha256.Sum256(after) || count(t, f, "datasets") != 1 {
		t.Fatal("credential changed after response loss")
	}
	info, _ := os.Stat(file)
	if info.Mode().Perm() != 0600 {
		t.Fatal("private mode")
	}
	if e = os.Remove(file); e != nil {
		t.Fatal(e)
	}
	if e = admin.InitCommand(context.Background(), f.s.Pool.Config().ConnString(), args, &out); e == nil {
		t.Fatal("must not regenerate missing credential")
	}
}
func TestSecondDisasterCreatesThirdIndependentDataset(t *testing.T) {
	a := setup(t)
	_, aRaw := testWorkoutBundle(t, a, 1)
	b := freshRecoveryDatabase(t, true)
	bootstrapRecovery(t, b)
	_, bRaw := testWorkoutBundle(t, b, 1)
	c := freshRecoveryDatabase(t, true)
	bootstrapRecovery(t, c)
	independentRecoveryIdentity(t, a.enrollment, b.enrollment)
	independentRecoveryIdentity(t, b.enrollment, c.enrollment)
	independentRecoveryIdentity(t, a.enrollment, c.enrollment)
	assertOldIdentityFenced(t, b, a, aRaw)
	assertOldIdentityFenced(t, c, a, aRaw)
	assertOldIdentityFenced(t, c, b, bRaw)
}

func TestSourceAvailableDisasterRecoveryAuditAndNewDeletionBarrier(t *testing.T) {
	ctx := context.Background()
	f, _ := trainingFixture(t)
	old, _ := testWorkoutBundle(t, f, 1)
	phase3Observation(&old)
	importPhase3(t, f, old)
	if _, e := f.s.DeleteWorkoutBundle(ctx, f.p, deleteBundleRequest(f, old.WorkoutID)); e != nil {
		t.Fatal(e)
	}
	before := datasetSnapshot(t, f, f.p.DatasetID)
	request := newDatasetRequest(f)
	request.OperationType = "disaster_recovery"
	credential := preparedCredential(t)
	if _, e := admin.NewDataset(ctx, f.s.Pool, request, credential); e != nil {
		t.Fatal(e)
	}
	var audit int
	if e := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM admin_operations WHERE operation_id=$1 AND operation_type='disaster_recovery' AND source_dataset=$2 AND target_dataset=$3 AND status='committed'`, request.OperationID, f.p.DatasetID, credential.DatasetID).Scan(&audit); e != nil || audit != 1 {
		t.Fatal("source available recovery audit missing")
	}
	nf := *f
	nf.enrollment = credential
	var e error
	nf.p, e = f.s.Authenticate(ctx, credential.Token)
	if e != nil {
		t.Fatal(e)
	}
	// A new synthetic reader sees the same HealthKit UUID; a new bundle is built
	// from facts under the new scope. The archived old JSON is not rewritten.
	fresh, _ := testWorkoutBundle(t, &nf, 1)
	fresh.WorkoutID = old.WorkoutID
	fresh.Workout.UUID = old.WorkoutID
	phase3Observation(&fresh)
	raw, _ := json.Marshal(fresh)
	if _, e = nf.s.ImportWorkoutBundle(ctx, nf.p, fresh, raw); e != nil {
		t.Fatal("old deletion incorrectly inherited", e)
	}
	runOne(t, &nf, "analysis.workout")
	var published int
	if e = nf.s.Pool.QueryRow(ctx, `SELECT count(*) FROM analysis_publications WHERE dataset_id=$1`, nf.p.DatasetID).Scan(&published); e != nil || published != 1 {
		t.Fatal("new analysis not published")
	}
	if _, e = nf.s.DeleteWorkoutBundle(ctx, nf.p, deleteBundleRequest(&nf, fresh.WorkoutID)); e != nil {
		t.Fatal(e)
	}
	_, e = nf.s.ImportWorkoutBundle(ctx, nf.p, fresh, raw)
	requireSyncCode(t, e, "workout_deleted")
	for table, rows := range before {
		if rows != datasetSnapshot(t, f, f.p.DatasetID)[table] {
			t.Fatal("archived source changed", table)
		}
	}
}

func TestSourceAvailableRecoveryCredentialPackageAndLostOutput(t *testing.T) {
	f := setup(t)
	request := newDatasetRequest(f)
	file := filepath.Join(t.TempDir(), "private.json")
	args := []string{"new-dataset", "--source-dataset", request.SourceDataset, "--operation-id", request.OperationID, "--actor", request.Actor, "--operation-type", "disaster_recovery", "--base-url", "https://synthetic.invalid", "--confirm-source", request.SourceDataset, "--confirm-phrase", "NEW-DATASET", "--credential-file", file}
	if e := admin.AdminCommand(context.Background(), f.s.Pool.Config().ConnString(), args, lostOutput{}); e == nil || count(t, f, "datasets") != 2 {
		t.Fatal("lost stdout did not follow atomic commit")
	}
	original, e := os.ReadFile(file)
	if e != nil {
		t.Fatal(e)
	}
	var p struct {
		OperationID, OperationType, BaseURL string
		ProtocolVersion                     int
		Enrollment                          admin.Enrollment
	}
	if e = json.Unmarshal(original, &p); e != nil || p.OperationID != request.OperationID || p.OperationType != "disaster_recovery" || p.BaseURL != "https://synthetic.invalid" || p.ProtocolVersion != 3 || p.Enrollment.Token == "" {
		t.Fatal("incomplete recovery package")
	}
	independentRecoveryIdentity(t, f.enrollment, p.Enrollment)
	var out bytes.Buffer
	if e = admin.AdminCommand(context.Background(), f.s.Pool.Config().ConnString(), args, &out); e != nil {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(original, after) || bytes.Contains(out.Bytes(), []byte(p.Enrollment.Token)) || count(t, f, "datasets") != 2 {
		t.Fatal("recovery retry changed credential or exposed it")
	}
}
