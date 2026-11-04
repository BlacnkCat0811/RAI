//go:build integration

package integration

import (
	"context"
	"encoding/json"
	ve "rai/server/internal/vo2maxexternal"
	"testing"
	"time"
)

func TestVO2ExternalBoundedIndexPlans(t *testing.T) {
	f, b, _ := externalFixture(t)
	ctx := context.Background()
	// Index-cost fixtures only: no projection is run against these owner-created
	// synthetic rows. Disable enrollment so bulk fixture setup creates no jobs.
	if _, e := f.s.Pool.Exec(ctx, `UPDATE analysis_model_channels SET extension_configs=extension_configs-$2 WHERE dataset_id=$1 AND channel='candidate'`, f.p.DatasetID, ve.Model); e != nil {
		t.Fatal(e)
	}
	_, e := f.s.Pool.Exec(ctx, `INSERT INTO health_samples(dataset_id,sample_type,sample_uuid,event_start,event_end,value_numeric,unit,source_bundle_id,content_sha256,entity_revision,metadata)
 SELECT x.dataset_id,x.sample_type,gen_random_uuid(),x.event_start-(i+100)*interval '1 day',x.event_end-(i+100)*interval '1 day',x.value_numeric,x.unit,x.source_bundle_id,x.content_sha256,x.entity_revision,
 jsonb_set(x.metadata,ARRAY['RAI:vo2maxFactV1','typedMetadata','HKMetadataKeySyncIdentifier'],jsonb_build_object('type','string','value','synthetic-index-'||(i%30)))
 FROM health_samples x CROSS JOIN generate_series(1,4000) i WHERE x.dataset_id=$1 AND x.sample_uuid=$2`, f.p.DatasetID, b.Facts[0].UUID)
	if e != nil {
		t.Fatal(e)
	}
	// BEFORE INSERT captures B2 origin context from actual corresponding rows.
	_, e = f.s.Pool.Exec(ctx, `INSERT INTO entity_tombstones(dataset_id,sample_type,sample_uuid,deleted_rev,previous_start,previous_end,reason)
 SELECT dataset_id,sample_type,sample_uuid,100,event_start,event_end,'synthetic index withdrawal' FROM health_samples WHERE dataset_id=$1 AND sample_uuid<>$2 LIMIT 2000`, f.p.DatasetID, b.Facts[0].UUID)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.s.Pool.Exec(ctx, `INSERT INTO entity_tombstones(dataset_id,sample_type,sample_uuid,deleted_rev,reason) SELECT $1,'HKQuantityTypeIdentifierVO2Max',gen_random_uuid(),i,'synthetic unknown' FROM generate_series(101,2100)i`, f.p.DatasetID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Pool.Exec(ctx, `ANALYZE health_samples; ANALYZE entity_tombstones`); e != nil {
		t.Fatal(e)
	}
	lower := time.Now().Add(-90 * 24 * time.Hour)
	epoch := b.Facts[0].Provenance.EpochID()
	for _, q := range []struct {
		name, sql string
		args      []any
	}{
		{"window", `SELECT sample_uuid FROM health_samples WHERE dataset_id=$1 AND sample_type='HKQuantityTypeIdentifierVO2Max' AND event_end>$2 ORDER BY event_end DESC,sample_uuid LIMIT 1025`, []any{f.p.DatasetID, lower}},
		{"epoch_end", `SELECT sample_uuid FROM health_samples WHERE dataset_id=$1 AND sample_type='HKQuantityTypeIdentifierVO2Max' AND metadata->'RAI:vo2maxFactV1'->>'sourceEpochID'=$2 AND event_end<=$3 ORDER BY event_end DESC LIMIT 1`, []any{f.p.DatasetID, epoch, lower}},
		{"sync", `SELECT sample_uuid FROM health_samples WHERE dataset_id=$1 AND sample_type='HKQuantityTypeIdentifierVO2Max' AND source_bundle_id=$2 AND metadata->'RAI:vo2maxFactV1'->'typedMetadata'->'HKMetadataKeySyncIdentifier'=$3::jsonb LIMIT 1025`, []any{f.p.DatasetID, b.Facts[0].Provenance.SourceRevision.Source.BundleIdentifier, `{"type":"string","value":"synthetic-index-1"}`}},
		{"withdrawal", `SELECT sample_uuid FROM entity_tombstones WHERE dataset_id=$1 AND sample_type='HKQuantityTypeIdentifierVO2Max' AND previous_end>$2 ORDER BY previous_end DESC LIMIT 1025`, []any{f.p.DatasetID, lower}},
		{"withdrawal_epoch", `SELECT sample_uuid FROM entity_tombstones WHERE dataset_id=$1 AND sample_type='HKQuantityTypeIdentifierVO2Max' AND provenance_snapshot->>'sourceEpochID'=$2 AND previous_end<=$3 ORDER BY previous_end DESC LIMIT 1`, []any{f.p.DatasetID, epoch, lower}},
		{"unknown_delete", `SELECT sample_uuid FROM entity_tombstones WHERE dataset_id=$1 AND sample_type='HKQuantityTypeIdentifierVO2Max' AND previous_end IS NULL ORDER BY deleted_rev DESC LIMIT 1`, []any{f.p.DatasetID}},
	} {
		var raw []byte
		if e = f.s.Pool.QueryRow(ctx, `EXPLAIN (ANALYZE,FORMAT JSON) `+q.sql, q.args...).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		var plans []struct {
			Plan map[string]any `json:"Plan"`
			Time float64        `json:"Execution Time"`
		}
		if json.Unmarshal(raw, &plans) != nil {
			t.Fatal("explain decode")
		}
		names := []string{}
		var walk func(map[string]any)
		walk = func(p map[string]any) {
			if name, ok := p["Index Name"].(string); ok {
				names = append(names, name)
			}
			if cs, ok := p["Plans"].([]any); ok {
				for _, c := range cs {
					walk(c.(map[string]any))
				}
			}
		}
		walk(plans[0].Plan)
		// Do not print Index Cond / Filter: they contain synthetic health identifiers.
		found := false
		for _, name := range names {
			if name == "vo2_external_"+q.name {
				found = true
			}
		}
		if !found {
			t.Fatalf("query %s did not use required bounded index: %v", q.name, names)
		}
		t.Logf("query=%s indexes=%v execution_ms=%.3f", q.name, names, plans[0].Time)
	}
}
