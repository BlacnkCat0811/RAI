//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"rai/server/internal/modelinput"
	"rai/server/internal/modelmanifest"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
)

// TestModelStackAutoActivation covers the deployment-time activation on a
// dataset that is not flagged synthetic (the real-dataset path; all values are
// synthetic): a stale candidate is replaced, nothing becomes readable before
// today's release and the history backfill are published, past days of the
// new stack are readable after the switch, the promotion is audited as automatic, a
// second run is a no-op, and a redeployed older image is not rolled back
// automatically.
func TestModelStackAutoActivation(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	ds := f.p.DatasetID
	if _, err := f.s.Pool.Exec(ctx, `UPDATE datasets SET synthetic_test=false WHERE id=$1`, ds); err != nil {
		t.Fatal(err)
	}
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	measured := today.AddDate(0, 0, -90).Format(time.RFC3339)
	if _, err := f.s.CommitModelInput(ctx, f.p, modelinput.Request{ID: syncing.NewID(), Kind: "hr_reference", SubjectKey: "athlete",
		EffectiveFrom: measured, Provenance: "synthetic integration reference",
		Payload: modelinput.Payload{"maxHR": 190.0, "restingHR": 50.0, "measuredAt": measured, "restingMeasuredAt": measured, "method": "user_entered", "unit": "bpm"}}); err != nil {
		t.Fatal(err)
	}
	// Runs 40 days and 1 day ago: history spans two backfill blocks (31 + 9).
	for _, age := range []int{40, 1} {
		acceptIngest(t, f, f.p, stackRun(t, f, syncing.NewID(), today.AddDate(0, 0, -age).Add(8*time.Hour), "11.0", steadyHR))
	}
	for _, o := range processAllIngests(t, f.s) {
		if o.Status != "committed" {
			t.Fatalf("ingest outcome %+v", o)
		}
	}
	// A candidate left behind by an earlier image.
	if _, err := f.s.Pool.Exec(ctx, `INSERT INTO analysis_model_channels(dataset_id,channel,stack_id,config_sha256,assignment_version)
 VALUES($1,'candidate','rai-v2-stale0000000000',decode(repeat('11',32),'hex'),1)`, ds); err != nil {
		t.Fatal(err)
	}
	stack := modelmanifest.StackID()
	channel := func(name string) string {
		t.Helper()
		var id string
		if err := f.s.Pool.QueryRow(ctx, `SELECT COALESCE((SELECT stack_id FROM analysis_model_channels WHERE dataset_id=$1 AND channel=$2),'')`, ds, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}

	r, err := f.s.AutoActivateModelStack(ctx, ds, "auto-deploy")
	if err != nil || r.Status != postgres.AutoActivationPending || r.ToStack != stack {
		t.Fatalf("first pass %+v %v", r, err)
	}
	if r.HistoryFrom != today.AddDate(0, 0, -40).Format(time.DateOnly) || r.HistoryThrough != today.AddDate(0, 0, -1).Format(time.DateOnly) || r.HistoryBlocks != 2 {
		t.Fatalf("history backfill plan %+v", r)
	}
	if channel("candidate") != stack {
		t.Fatal("stale candidate was not replaced by the deployed stack")
	}
	if a := channel("active"); a == stack {
		t.Fatal("candidate became active before its release was published")
	}
	if again, err := f.s.AutoActivateModelStack(ctx, ds, "auto-deploy"); err != nil || again.Status != postgres.AutoActivationPending {
		t.Fatalf("repeat pass before the release %+v %v", again, err)
	}
	// Process the first block only: not ready while history is incomplete.
	if processStackJobs(t, f) == 0 {
		t.Fatal("no day release succeeded")
	}
	if mid, err := f.s.AutoActivateModelStack(ctx, ds, "auto-deploy"); err != nil || mid.Status != postgres.AutoActivationPending || channel("active") == stack {
		t.Fatalf("activated before history was complete %+v %v", mid, err)
	}
	for i := 0; i < 20 && processStackJobs(t, f) > 0; i++ {
	}

	r, err = f.s.AutoActivateModelStack(ctx, ds, "auto-deploy")
	if err != nil || r.Status != postgres.AutoActivationActivated || r.Revision == 0 {
		t.Fatalf("activation pass %+v %v", r, err)
	}
	if channel("active") != stack || channel("candidate") != "" {
		t.Fatal("deployed stack is not the only active assignment")
	}
	if r.HistoryComplete != 2 {
		t.Fatalf("history blocks complete %+v", r)
	}
	var clean int
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM dirty_scopes WHERE dataset_id=$1 AND generation=clean_generation
 AND scope_key BETWEEN $2 AND $3`, ds, modelmanifest.DayScope(stack, today.AddDate(0, 0, -40).Format(time.DateOnly)),
		modelmanifest.DayScope(stack, today.Format(time.DateOnly))).Scan(&clean); err != nil {
		t.Fatal(err)
	}
	if clean != 41 {
		t.Fatalf("new stack has %d clean days of the last 41; history restarted on the activation day", clean)
	}
	var actor string
	var gate []byte
	if err := f.s.Pool.QueryRow(ctx, `SELECT actor,gate_summary FROM analysis_assignment_audit WHERE dataset_id=$1 AND revision=$2 AND action='promote'`, ds, r.Revision).Scan(&actor, &gate); err != nil {
		t.Fatal(err)
	}
	var summary map[string]any
	if err := json.Unmarshal(gate, &summary); err != nil || actor != "auto-deploy" || summary["autoActivated"] != true || summary["syntheticDataset"] != false || summary["standardGatesVerified"] != false {
		t.Fatalf("audit actor=%q gate=%s", actor, gate)
	}
	if r, err = f.s.AutoActivateModelStack(ctx, ds, "auto-deploy"); err != nil || r.Status != postgres.AutoActivationActive {
		t.Fatalf("second deployment of the same image %+v %v", r, err)
	}

	// An older image redeployed: the deployed stack is previous. Rollback
	// stays explicit; nothing is switched.
	tx, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`SET LOCAL rai.model_assignment='on'`,
		`DELETE FROM analysis_model_channels WHERE dataset_id='` + ds + `' AND channel='previous'`,
		`UPDATE analysis_model_channels SET channel='previous' WHERE dataset_id='` + ds + `' AND channel='active'`,
		`INSERT INTO analysis_model_channels(dataset_id,channel,stack_id,config_sha256,assignment_version) VALUES('` + ds + `','active','rai-v2-newer000000000',decode(repeat('22',32),'hex'),99)`} {
		if _, err := tx.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if r, err = f.s.AutoActivateModelStack(ctx, ds, "auto-deploy"); err != nil || r.Status != postgres.AutoActivationRollbackRequired {
		t.Fatalf("redeployed older image %+v %v", r, err)
	}
	if channel("active") != "rai-v2-newer000000000" || channel("candidate") != "" {
		t.Fatal("an older image changed the assignment")
	}
}
