-- +goose Up
-- Research artifacts share the day release but are not product metric
-- families; count only the six model_product_v2 families when deciding whether
-- a backfill day is ready.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rai_backfill_progress(p_dataset uuid,p_id uuid,p_retry boolean)
RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE o model_backfill_operations%ROWTYPE; item jsonb; days_ready boolean; pending jsonb;
 completed jsonb:='[]'; done_count int:=0; new_state text; day date; scope text; job_id uuid;
 timezone_name text; today date; waiting_failed boolean:=false; scope_prefix text;
BEGIN
 SELECT timezone INTO timezone_name FROM datasets WHERE id=p_dataset AND synthetic_test AND retired_at IS NULL FOR UPDATE;
 IF NOT FOUND THEN RETURN; END IF;
 SELECT * INTO o FROM model_backfill_operations WHERE dataset_id=p_dataset AND id=p_id FOR UPDATE;
 IF NOT FOUND OR o.status='cancelled' OR o.view<>'revised' THEN RETURN; END IF;
 IF NOT EXISTS(SELECT 1 FROM analysis_model_channels m WHERE m.dataset_id=p_dataset AND m.channel='candidate'
  AND m.stack_id=o.stack_id AND m.config_sha256=o.manifest_sha256 AND m.extension_configs->>'rai.model_manifest/1'=encode(o.manifest_sha256,'hex')) THEN
  RETURN;
 END IF;
 today:=(clock_timestamp() AT TIME ZONE timezone_name)::date;
 scope_prefix:='model/'||o.stack_id||'/athlete/day/v2/';
 FOR item IN SELECT value FROM jsonb_array_elements(o.checkpoint->'blocks') LOOP
  SELECT NOT EXISTS (
   SELECT 1 FROM generate_series(0,(item->>'through')::date-(item->>'from')::date) n
   WHERE NOT EXISTS (
    SELECT 1 FROM dirty_scopes d
    JOIN analysis_publications p ON p.dataset_id=d.dataset_id AND p.scope_key=d.scope_key AND p.generation=d.generation
    JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id AND r.scope_key=d.scope_key AND r.target_generation=d.generation
    JOIN jobs j ON j.dataset_id=r.dataset_id AND j.id=r.job_id AND j.scope_key=d.scope_key AND j.target_generation=d.generation
    WHERE d.dataset_id=p_dataset AND d.scope_key=scope_prefix||((item->>'from')::date+n)::text
     AND d.generation=d.clean_generation AND r.status='SUCCEEDED' AND r.lineage->>'manifestSHA256'=encode(o.manifest_sha256,'hex')
     AND j.kind='analysis.model_stack_v2' AND j.state='succeeded' AND j.payload->>'backfillID'=p_id::text
    GROUP BY d.dataset_id,d.scope_key HAVING count(DISTINCT p.metric_family) FILTER (WHERE p.metric_family LIKE 'model_product_v2/%')=6
   )
  ) INTO days_ready;
  IF days_ready THEN
   done_count:=done_count+1;
   completed:=completed||jsonb_build_array(item||'{"status":"complete"}');
  ELSIF pending IS NULL THEN
   pending:=item;
  END IF;
 END LOOP;

 IF pending IS NULL THEN
  new_state:='candidate_ready';
 ELSE
  new_state:='running';
  FOR day IN SELECT (pending->>'from')::date+n FROM generate_series(0,(pending->>'through')::date-(pending->>'from')::date) n LOOP
   IF day>today THEN RAISE EXCEPTION 'backfill_future' USING ERRCODE='RA083'; END IF;
   scope:=scope_prefix||day::text;
   IF EXISTS(SELECT 1 FROM jobs j JOIN dirty_scopes d USING(dataset_id,scope_key)
    WHERE j.dataset_id=p_dataset AND j.scope_key=scope AND j.target_generation=d.generation AND j.kind='analysis.model_stack_v2'
      AND j.payload->>'backfillID'=p_id::text AND
       (j.state IN ('queued','running') OR (j.state='succeeded' AND d.generation=d.clean_generation))) THEN
    CONTINUE;
   END IF;
   IF NOT p_retry AND EXISTS(SELECT 1 FROM jobs j JOIN dirty_scopes d USING(dataset_id,scope_key)
    WHERE j.dataset_id=p_dataset AND j.scope_key=scope AND j.target_generation=d.generation AND j.kind='analysis.model_stack_v2'
      AND j.payload->>'backfillID'=p_id::text AND j.state IN ('failed','repair_required')) THEN
    waiting_failed:=true;
    CONTINUE;
   END IF;
   job_id:=rai_schedule_model_day(p_dataset,o.stack_id,day,clock_timestamp());
   UPDATE jobs SET payload=jsonb_build_object('backfillID',p_id) WHERE id=job_id;
  END LOOP;
  IF waiting_failed THEN new_state:='paused'; END IF;
 END IF;
 UPDATE model_backfill_operations SET status=new_state,completed_blocks=done_count,
  checkpoint=jsonb_set(checkpoint,'{completed}',completed),updated_at=clock_timestamp()
  WHERE dataset_id=p_dataset AND id=p_id;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE FUNCTION rai_backfill_progress(p_dataset uuid,p_id uuid,p_retry boolean)
RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE o model_backfill_operations%ROWTYPE; item jsonb; days_ready boolean; pending jsonb;
 completed jsonb:='[]'; done_count int:=0; new_state text; day date; scope text; job_id uuid;
 timezone_name text; today date; waiting_failed boolean:=false; scope_prefix text;
BEGIN
 SELECT timezone INTO timezone_name FROM datasets WHERE id=p_dataset AND synthetic_test AND retired_at IS NULL FOR UPDATE;
 IF NOT FOUND THEN RETURN; END IF;
 SELECT * INTO o FROM model_backfill_operations WHERE dataset_id=p_dataset AND id=p_id FOR UPDATE;
 IF NOT FOUND OR o.status='cancelled' OR o.view<>'revised' THEN RETURN; END IF;
 IF NOT EXISTS(SELECT 1 FROM analysis_model_channels m WHERE m.dataset_id=p_dataset AND m.channel='candidate'
  AND m.stack_id=o.stack_id AND m.config_sha256=o.manifest_sha256 AND m.extension_configs->>'rai.model_manifest/1'=encode(o.manifest_sha256,'hex')) THEN
  RETURN;
 END IF;
 today:=(clock_timestamp() AT TIME ZONE timezone_name)::date;
 scope_prefix:='model/'||o.stack_id||'/athlete/day/v2/';
 FOR item IN SELECT value FROM jsonb_array_elements(o.checkpoint->'blocks') LOOP
  SELECT NOT EXISTS (
   SELECT 1 FROM generate_series(0,(item->>'through')::date-(item->>'from')::date) n
   WHERE NOT EXISTS (
    SELECT 1 FROM dirty_scopes d
    JOIN analysis_publications p ON p.dataset_id=d.dataset_id AND p.scope_key=d.scope_key AND p.generation=d.generation
    JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id AND r.scope_key=d.scope_key AND r.target_generation=d.generation
    JOIN jobs j ON j.dataset_id=r.dataset_id AND j.id=r.job_id AND j.scope_key=d.scope_key AND j.target_generation=d.generation
    WHERE d.dataset_id=p_dataset AND d.scope_key=scope_prefix||((item->>'from')::date+n)::text
     AND d.generation=d.clean_generation AND r.status='SUCCEEDED' AND r.lineage->>'manifestSHA256'=encode(o.manifest_sha256,'hex')
     AND j.kind='analysis.model_stack_v2' AND j.state='succeeded' AND j.payload->>'backfillID'=p_id::text
    GROUP BY d.dataset_id,d.scope_key HAVING count(DISTINCT p.metric_family)=6
   )
  ) INTO days_ready;
  IF days_ready THEN
   done_count:=done_count+1;
   completed:=completed||jsonb_build_array(item||'{"status":"complete"}');
  ELSIF pending IS NULL THEN
   pending:=item;
  END IF;
 END LOOP;

 IF pending IS NULL THEN
  new_state:='candidate_ready';
 ELSE
  new_state:='running';
  FOR day IN SELECT (pending->>'from')::date+n FROM generate_series(0,(pending->>'through')::date-(pending->>'from')::date) n LOOP
   IF day>today THEN RAISE EXCEPTION 'backfill_future' USING ERRCODE='RA083'; END IF;
   scope:=scope_prefix||day::text;
   IF EXISTS(SELECT 1 FROM jobs j JOIN dirty_scopes d USING(dataset_id,scope_key)
    WHERE j.dataset_id=p_dataset AND j.scope_key=scope AND j.target_generation=d.generation AND j.kind='analysis.model_stack_v2'
      AND j.payload->>'backfillID'=p_id::text AND
       (j.state IN ('queued','running') OR (j.state='succeeded' AND d.generation=d.clean_generation))) THEN
    CONTINUE;
   END IF;
   IF NOT p_retry AND EXISTS(SELECT 1 FROM jobs j JOIN dirty_scopes d USING(dataset_id,scope_key)
    WHERE j.dataset_id=p_dataset AND j.scope_key=scope AND j.target_generation=d.generation AND j.kind='analysis.model_stack_v2'
      AND j.payload->>'backfillID'=p_id::text AND j.state IN ('failed','repair_required')) THEN
    waiting_failed:=true;
    CONTINUE;
   END IF;
   job_id:=rai_schedule_model_day(p_dataset,o.stack_id,day,clock_timestamp());
   UPDATE jobs SET payload=jsonb_build_object('backfillID',p_id) WHERE id=job_id;
  END LOOP;
  IF waiting_failed THEN new_state:='paused'; END IF;
 END IF;
 UPDATE model_backfill_operations SET status=new_state,completed_blocks=done_count,
  checkpoint=jsonb_set(checkpoint,'{completed}',completed),updated_at=clock_timestamp()
  WHERE dataset_id=p_dataset AND id=p_id;
END $$;
-- +goose StatementEnd
