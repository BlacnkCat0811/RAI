//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"rai/server/internal/admin"
)

func TestRestoredBackupIsNotActiveContinuation(t *testing.T) {
	ctx := context.Background()
	f, plan := trainingFixture(t)
	var version string
	if e := f.s.Pool.QueryRow(ctx, `SHOW server_version`).Scan(&version); e != nil || !strings.HasPrefix(version, "18.6") {
		t.Fatal("PostgreSQL 18.6 required")
	}
	for _, setting := range []string{"fsync", "full_page_writes", "synchronous_commit", "data_checksums"} {
		var value string
		if e := f.s.Pool.QueryRow(ctx, "SHOW "+setting).Scan(&value); e != nil || value != "on" {
			t.Fatal("unsafe restore test setting", setting)
		}
	}
	a, _ := testWorkoutBundle(t, f, 2)
	phase3Observation(&a)
	importPhase3(t, f, a)
	runOne(t, f, "analysis.workout")
	deleted, _ := testWorkoutBundle(t, f, 1)
	phase3Observation(&deleted)
	importPhase3(t, f, deleted)
	if _, e := f.s.DeleteWorkoutBundle(ctx, f.p, deleteBundleRequest(f, deleted.WorkoutID)); e != nil {
		t.Fatal(e)
	}
	atBackup := datasetSnapshot(t, f, f.p.DatasetID)
	metadata, e := f.s.Metadata(ctx, f.p)
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	dump := filepath.Join(dir, "synthetic.dump")
	manifest := filepath.Join(dir, "manifest.json")
	pgbin := os.Getenv("PGBIN")
	if pgbin == "" {
		t.Fatal("PGBIN required for real pg_dump/pg_restore")
	}
	dumpCmd := exec.Command(filepath.Join(pgbin, "pg_dump"), "--format=custom", "--file", dump, "--dbname", f.s.Pool.Config().ConnString())
	if output, e := dumpCmd.CombinedOutput(); e != nil {
		t.Fatalf("synthetic pg_dump failed: %v %s", e, output)
	}
	dumpBytes, e := os.ReadFile(dump)
	if e != nil {
		t.Fatal(e)
	}
	dumpHash := sha256.Sum256(dumpBytes)
	var tableCount, fkCount int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE'`).Scan(&tableCount); e != nil {
		t.Fatal(e)
	}
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE contype='f' AND connamespace='public'::regnamespace AND convalidated`).Scan(&fkCount); e != nil {
		t.Fatal(e)
	}
	mb, _ := json.Marshal(map[string]any{"dumpSHA256": hex.EncodeToString(dumpHash[:]), "schema": 23, "tables": tableCount, "foreignKeys": fkCount, "purpose": "quarantine_archive_only"})
	manifestHash := sha256.Sum256(mb)
	if e = os.WriteFile(manifest, mb, 0600); e != nil {
		t.Fatal(e)
	}
	// T2 B import, T3 A deletion, T4 training change after the backup point.
	b, _ := testWorkoutBundle(t, f, 1)
	phase3Observation(&b)
	importPhase3(t, f, b)
	if _, e = f.s.DeleteWorkoutBundle(ctx, f.p, deleteBundleRequest(f, a.WorkoutID)); e != nil {
		t.Fatal(e)
	}
	changed := editTraining(t, f, plan, plan.Effective.Sessions[0].ID, "distanceM", 7000, 0)
	if changed.ID == plan.Effective.ID {
		t.Fatal("post-backup training publication missing")
	}
	// Restore is into a different empty database, with no API listener at all.
	restored := freshRecoveryDatabase(t, false)
	verify := func(path string, want [32]byte) bool {
		bytes, e := os.ReadFile(path)
		return e == nil && sha256.Sum256(bytes) == want
	}
	if !verify(dump, dumpHash) || !verify(manifest, manifestHash) {
		t.Fatal("backup integrity mismatch")
	}
	corrupt := filepath.Join(dir, "corrupt-manifest.json")
	_ = os.WriteFile(corrupt, append(mb, byte(' ')), 0600)
	if verify(corrupt, manifestHash) {
		t.Fatal("corrupt manifest passed")
	}
	started := time.Now()
	restoreCmd := exec.Command(filepath.Join(pgbin, "pg_restore"), "--exit-on-error", "--single-transaction", "--dbname", restored.s.Pool.Config().ConnString(), dump)
	if output, e := restoreCmd.CombinedOutput(); e != nil {
		t.Fatalf("synthetic pg_restore failed: %v %s", e, output)
	}
	elapsed := time.Since(started)
	restored.p = f.p
	restored.enrollment = f.enrollment
	t.Run("RestoredBackupPreservesBackupPointFacts", func(t *testing.T) {
		if !reflect.DeepEqual(atBackup, datasetSnapshot(t, restored, f.p.DatasetID)) {
			t.Fatal("backup point scoped facts changed")
		}
		m, e := restored.s.Metadata(ctx, f.p)
		if e != nil || m != metadata {
			t.Fatal("backup dataset identity/revision changed")
		}
		status, e := admin.MigrationStatus(ctx, restored.s.Pool.Config().ConnString())
		if e != nil || len(status) != 23 {
			t.Fatal("restored schema mismatch", e)
		}
		var tables, fks, invalid int
		if e = restored.s.Pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE'`).Scan(&tables); e != nil {
			t.Fatal(e)
		}
		if e = restored.s.Pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE convalidated),count(*) FILTER(WHERE NOT convalidated) FROM pg_constraint WHERE contype='f' AND connamespace='public'::regnamespace`).Scan(&fks, &invalid); e != nil || invalid != 0 || fks != fkCount || tables != tableCount {
			t.Fatal("FK/table manifest mismatch")
		}
		for _, table := range []string{"workouts", "health_samples", "workout_routes", "workout_imports", "entity_tombstones", "analysis_runs", "analysis_publications", "training_plans", "training_effective_versions"} {
			if count(t, restored, table) == 0 {
				t.Fatal("backup fixture incomplete", table)
			}
		}
		var deletes int
		if e = restored.s.Pool.QueryRow(ctx, `SELECT count(*) FROM change_events WHERE entity_kind='workout' AND operation='delete' AND payload ? 'receipt'`).Scan(&deletes); e != nil || deletes == 0 {
			t.Fatal("delete receipt missing", e)
		}
	})
	t.Run("RestoredBackupMissesPostBackupDelete", func(t *testing.T) {
		var present, late, tombstone int
		e := restored.s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM workouts WHERE workout_uuid=$1),(SELECT count(*) FROM workouts WHERE workout_uuid=$2),(SELECT count(*) FROM entity_tombstones WHERE sample_uuid=$1)`, a.WorkoutID, b.WorkoutID).Scan(&present, &late, &tombstone)
		if e != nil || present != 1 || late != 0 || tombstone != 0 {
			t.Fatal("backup not at T1", e)
		}
	})
	t.Run("RestoredBackupCannotBePresentedAsCurrentTruth", func(t *testing.T) {
		current, e := f.s.Metadata(ctx, f.p)
		if e != nil || current.CommitRevision <= metadata.CommitRevision {
			t.Fatal("timeline not advanced")
		}
		if reflect.DeepEqual(atBackup, datasetSnapshot(t, f, f.p.DatasetID)) {
			t.Fatal("post-backup evidence not different")
		}
	})
	// Quarantine defense in depth: future connections default to read-only;
	// runbook additionally forbids starting API/Worker or exposing any port.
	dbname := restored.s.Pool.Config().ConnConfig.Database
	if _, e = restored.s.Pool.Exec(ctx, "ALTER DATABASE "+pgx.Identifier{dbname}.Sanitize()+" SET default_transaction_read_only=on"); e != nil {
		t.Fatal(e)
	}
	restored.s.Pool.Reset()
	var readonly string
	if e = restored.s.Pool.QueryRow(ctx, `SHOW transaction_read_only`).Scan(&readonly); e != nil || readonly != "on" {
		t.Fatal("restore not quarantined")
	}
	requireReadOnly := func(e error) {
		t.Helper()
		var pgerr *pgconn.PgError
		if !errors.As(e, &pgerr) || pgerr.Code != "25006" {
			t.Fatalf("expected read-only quarantine failure, got %v", e)
		}
	}
	if _, e = restored.s.DeleteWorkoutBundle(ctx, restored.p, deleteBundleRequest(restored, a.WorkoutID)); e == nil {
		t.Fatal("quarantine accepted old DELETE")
	}
	requireReadOnly(e)
	raw, _ := json.Marshal(b)
	if _, e = restored.s.ImportWorkoutBundle(ctx, restored.p, b, raw); e == nil {
		t.Fatal("quarantine accepted old POST")
	}
	requireReadOnly(e)
	after, e := restored.s.Metadata(ctx, restored.p)
	if e != nil || after.CommitRevision != metadata.CommitRevision {
		t.Fatal("quarantine revision advanced")
	}
	t.Logf("PHASE5B_RESTORE schema=23 tables=%d validated_FKs=%d manifest_sha256=%x dump_sha256=%x restore_ms=%d no_api_exposure=true read_only=true", tableCount, fkCount, manifestHash, dumpHash, elapsed.Milliseconds())
}
