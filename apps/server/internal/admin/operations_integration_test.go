//go:build integration

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestOperationsSeparatesRetiredModelFailuresFromLiveFailures(t *testing.T) {
	ctx := context.Background()
	dsn := isolatedDatabase(t)
	if err := Migrate(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	if _, err := c.Exec(ctx, `INSERT INTO datasets(id,epoch,service_id) VALUES
 ('10000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000002','10000000-0000-4000-8000-000000000003');
 INSERT INTO analysis_model_channels(dataset_id,channel,stack_id,config_sha256,assignment_version)
 VALUES('10000000-0000-4000-8000-000000000001','active','live',decode(repeat('00',32),'hex'),1);
 INSERT INTO jobs(id,dataset_id,kind,scope_key,target_generation,state,last_error_code)
 SELECT gen_random_uuid(),'10000000-0000-4000-8000-000000000001','analysis.model_stack_v2',
  'model/retired/athlete/day/v2/2026-10-11',n,'failed','analysis_unsupported_algorithm' FROM generate_series(1,63) n;
 INSERT INTO jobs(id,dataset_id,kind,scope_key,target_generation,state,last_error_code)
 VALUES(gen_random_uuid(),'10000000-0000-4000-8000-000000000001','analysis.model_stack_v2',
  'model/live/athlete/day/v2/2026-10-11',1,'failed','analysis_invalid_input')`); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := Operations(ctx, dsn, []string{"snapshot"}, &b); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Action     int `json:"failed_jobs_needing_action"`
		Historical int `json:"historical_failed_jobs"`
		Jobs       struct {
			Failed int `json:"failed"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(b.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Action != 1 || out.Historical != 63 || out.Jobs.Failed != 64 {
		t.Fatalf("failed job accounting: %+v", out)
	}
}
