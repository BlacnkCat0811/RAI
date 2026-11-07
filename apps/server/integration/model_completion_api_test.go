//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"rai/server/internal/modelinput"
)

func TestModelInputIdempotencyAndConflict(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := "6f9619ff-8b86-4d01-b42d-00cf4fc964ff"
	r := modelinput.Request{ID: id, Kind: "sleep_target", SubjectKey: "athlete", EffectiveFrom: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), Provenance: "user_form", Payload: modelinput.Payload{"hours": 8.0}}
	a, err := f.s.CommitModelInput(ctx, f.p, r)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.s.CommitModelInput(ctx, f.p, r)
	if err != nil {
		t.Fatal(err)
	}
	if a.ContentHash != b.ContentHash || a.AcceptedAtUS != b.AcceptedAtUS || len(a.JobIDs) != 0 {
		t.Fatal("retry did not return original receipt", a, b)
	}
	r.Payload = modelinput.Payload{"hours": 7.0}
	if _, err = f.s.CommitModelInput(ctx, f.p, r); !errors.Is(err, modelinput.ErrConflict) {
		t.Fatal("changed body accepted", err)
	}
	var n int
	if err = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM athlete_model_inputs WHERE dataset_id=$1 AND id=$2`, f.p.DatasetID, id).Scan(&n); err != nil || n != 1 {
		t.Fatal("immutable input count", n, err)
	}
}

func TestProductReaderRejectsLegacyActive(t *testing.T) {
	f := setup(t)
	_, err := f.s.ReadAthleteProduct(context.Background(), f.p, time.Now().UTC())
	if err == nil {
		t.Fatal("legacy active incorrectly exposed as schema2")
	}
}

func TestModelInputRevocationIsAppendOnlyAndIdempotent(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := "6f9619ff-8b86-4d01-b42d-00cf4fc964fe"
	r := modelinput.Request{ID: id, Kind: "sleep_target", SubjectKey: "athlete", EffectiveFrom: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), Provenance: "user_form", Payload: modelinput.Payload{"hours": 8.0}}
	if _, err := f.s.CommitModelInput(ctx, f.p, r); err != nil {
		t.Fatal(err)
	}
	a, err := f.s.RevokeModelInput(ctx, f.p, id, "user correction")
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.s.RevokeModelInput(ctx, f.p, id, "user correction")
	if err != nil {
		t.Fatal(err)
	}
	if a.InputID != b.InputID || a.ContentHash != b.ContentHash {
		t.Fatal("revocation retry was not idempotent", a, b)
	}
	var kind, schema string
	var payload []byte
	if err = f.s.Pool.QueryRow(ctx, `SELECT kind,schema_version,payload FROM athlete_model_inputs WHERE dataset_id=$1 AND id=$2`, f.p.DatasetID, a.InputID).Scan(&kind, &schema, &payload); err != nil {
		t.Fatal(err)
	}
	if kind != "revocation" || schema != modelinput.SchemaSleepTarget {
		t.Fatalf("revocation identity= %s %s", kind, schema)
	}
	var target int
	if err = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM athlete_model_inputs WHERE dataset_id=$1 AND id=$2`, f.p.DatasetID, id).Scan(&target); err != nil || target != 1 {
		t.Fatal("target input was mutated or deleted", target, err)
	}
}

func TestModelInputReceiptFailureRollsBackInputAndDirty(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.s.Pool.Exec(ctx, `CREATE FUNCTION test_fail_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic_receipt_failure'; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(ctx, `CREATE TRIGGER test_fail_receipt BEFORE INSERT ON model_input_receipts FOR EACH ROW EXECUTE FUNCTION test_fail_receipt()`); err != nil {
		t.Fatal(err)
	}
	id := "6f9619ff-8b86-4d01-b42d-00cf4fc964f0"
	r := modelinput.Request{ID: id, Kind: "sleep_target", SubjectKey: "athlete", EffectiveFrom: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), Provenance: "user_form", Payload: modelinput.Payload{"hours": 8.0}}
	if _, err := f.s.CommitModelInput(ctx, f.p, r); err == nil {
		t.Fatal("receipt trigger failure was ignored")
	}
	var inputs, dirty int
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM athlete_model_inputs WHERE dataset_id=$1 AND id=$2`, f.p.DatasetID, id).Scan(&inputs); err != nil || inputs != 0 {
		t.Fatal("input escaped failed transaction", inputs, err)
	}
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM dirty_scopes WHERE dataset_id=$1 AND scope_key='athlete/default'`, f.p.DatasetID).Scan(&dirty); err != nil || dirty != 0 {
		t.Fatal("dirty escaped failed transaction", dirty, err)
	}
	if _, err := f.s.Pool.Exec(ctx, `DROP TRIGGER test_fail_receipt ON model_input_receipts`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(ctx, `DROP FUNCTION test_fail_receipt()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.CommitModelInput(ctx, f.p, r); err != nil {
		t.Fatal("retry after fault failed", err)
	}
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM athlete_model_inputs WHERE dataset_id=$1 AND id=$2`, f.p.DatasetID, id).Scan(&inputs); err != nil || inputs != 1 {
		t.Fatal("retry did not persist exactly one input", inputs, err)
	}
}

func TestModelInputCannotCrossDataset(t *testing.T) {
	f1 := setup(t)
	f2 := setup(t)
	ctx := context.Background()
	id := "6f9619ff-8b86-4d01-b42d-00cf4fc964f1"
	r := modelinput.Request{ID: id, Kind: "sleep_target", SubjectKey: "athlete", EffectiveFrom: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), Provenance: "user_form", Payload: modelinput.Payload{"hours": 8.0}}
	if _, err := f1.s.CommitModelInput(ctx, f1.p, r); err != nil {
		t.Fatal(err)
	}
	if _, err := f2.s.RevokeModelInput(ctx, f2.p, id, "cross dataset"); !errors.Is(err, modelinput.ErrNotFound) {
		t.Fatalf("cross-dataset revocation did not return not found: %v", err)
	}
	var n int
	if err := f2.s.Pool.QueryRow(ctx, `SELECT count(*) FROM athlete_model_inputs WHERE dataset_id=$1 AND id=$2`, f2.p.DatasetID, id).Scan(&n); err != nil || n != 0 {
		t.Fatal("cross-dataset input leaked", n, err)
	}
	if err := f1.s.Pool.QueryRow(ctx, `SELECT count(*) FROM athlete_model_inputs WHERE dataset_id=$1 AND id=$2`, f1.p.DatasetID, id).Scan(&n); err != nil || n != 1 {
		t.Fatal("source dataset input changed", n, err)
	}
}
