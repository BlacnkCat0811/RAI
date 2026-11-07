//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"rai/server/internal/syncing"
)

// TestV2ArtifactSchemaAndRunIdentityAreTransactional proves migration 37
// admits a v2 payload only under its own RUNNING run identity. The rollback
// leaves no analysis row behind; no score is computed or published here.
func TestV2ArtifactSchemaAndRunIdentityAreTransactional(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	tx, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	jobID, runID := syncing.NewID(), syncing.NewID()
	if _, err = tx.Exec(ctx, `INSERT INTO jobs(id,dataset_id,kind,scope_key,target_generation,state,lease_until,fencing_token)
VALUES($1,$2,'analysis.load_state','model/test/athlete/load',1,'running',clock_timestamp()+interval '1 hour',1)`, jobID, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO analysis_runs(dataset_id,id,job_id,fencing_token,scope_key,target_generation,algorithm_name,algorithm_version,input_version,input_hash,input_from,input_through,lineage,status)
VALUES($1,$2,$3,1,'model/test/athlete/load',1,'rai.load_state.exp_finite','v2','test',decode(repeat('00',32),'hex'),clock_timestamp()-interval '1 day',clock_timestamp(),'{}','RUNNING')`, f.p.DatasetID, runID, jobID); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"schemaVersion": "rai.load_state/2", "datasetID": f.p.DatasetID, "generation": 1, "days": []any{}})
	h := sha256.Sum256(payload)
	if _, err = tx.Exec(ctx, `INSERT INTO analysis_artifacts(dataset_id,run_id,artifact_key,schema_version,content_sha256,payload)
VALUES($1,$2,'load_state','rai.load_state/2',decode($3,'hex'),$4)`, f.p.DatasetID, runID, hex.EncodeToString(h[:]), payload); err != nil {
		t.Fatal(err)
	}
	// A v1 run must not accept the v2 payload identity.
	badJob := syncing.NewID()
	if _, err = tx.Exec(ctx, `INSERT INTO jobs(id,dataset_id,kind,scope_key,target_generation,state,lease_until,fencing_token)
VALUES($1,$2,'analysis.load_state','model/test/athlete/load-v1',1,'running',clock_timestamp()+interval '1 hour',2)`, badJob, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	badRun := syncing.NewID()
	if _, err = tx.Exec(ctx, `INSERT INTO analysis_runs(dataset_id,id,job_id,fencing_token,scope_key,target_generation,algorithm_name,algorithm_version,input_version,input_hash,input_from,input_through,lineage,status)
VALUES($1,$2,$3,2,'model/test/athlete/load-v1',1,'rai.load_state.exp_finite','v1','test',decode(repeat('00',32),'hex'),clock_timestamp()-interval '1 day',clock_timestamp(),'{}','RUNNING')`, f.p.DatasetID, badRun, badJob); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO analysis_artifacts(dataset_id,run_id,artifact_key,schema_version,content_sha256,payload)
VALUES($1,$2,'load_state','rai.load_state/2',decode($3,'hex'),$4)`, f.p.DatasetID, badRun, hex.EncodeToString(h[:]), payload); err == nil {
		t.Fatal("v2 artifact accepted under a v1 run")
	}
	_ = time.Now()
}
