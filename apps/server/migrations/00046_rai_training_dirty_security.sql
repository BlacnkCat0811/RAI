-- +goose Up
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rai_training_dirty(p_dataset uuid,p_plan uuid) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE g bigint; scope text := 'plan:'||p_plan::text;
BEGIN
 IF NOT EXISTS(SELECT 1 FROM training_plans WHERE dataset_id=p_dataset AND id=p_plan AND active) THEN RETURN; END IF;
 INSERT INTO dirty_scopes(dataset_id,scope_key,generation,dirty_from,dirty_through)
 VALUES(p_dataset,scope,1,now(),now())
 ON CONFLICT(dataset_id,scope_key) DO UPDATE SET generation=dirty_scopes.generation+1,dirty_through=now()
 RETURNING generation INTO g;
 UPDATE jobs SET target_generation=g,run_after=now()+interval '1 second'
 WHERE id=(SELECT id FROM jobs WHERE dataset_id=p_dataset AND scope_key=scope AND kind='training.plan' AND state='queued' ORDER BY target_generation DESC LIMIT 1 FOR UPDATE);
 IF NOT FOUND THEN
  INSERT INTO jobs(id,dataset_id,kind,scope_key,target_generation,run_after)
  VALUES(gen_random_uuid(),p_dataset,'training.plan',scope,g,now()+interval '1 second');
 END IF;
END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION rai_training_dirty(uuid,uuid) FROM PUBLIC;

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rai_training_dirty(p_dataset uuid,p_plan uuid) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE g bigint; scope text := 'plan:'||p_plan::text;
BEGIN
 IF NOT EXISTS(SELECT 1 FROM training_plans WHERE dataset_id=p_dataset AND id=p_plan AND active) THEN RETURN; END IF;
 INSERT INTO dirty_scopes(dataset_id,scope_key,generation,dirty_from,dirty_through)
 VALUES(p_dataset,scope,1,now(),now())
 ON CONFLICT(dataset_id,scope_key) DO UPDATE SET generation=dirty_scopes.generation+1,dirty_through=now()
 RETURNING generation INTO g;
 UPDATE jobs SET target_generation=g,run_after=now()+interval '1 second'
 WHERE id=(SELECT id FROM jobs WHERE dataset_id=p_dataset AND scope_key=scope AND kind='training.plan' AND state='queued' ORDER BY target_generation DESC LIMIT 1 FOR UPDATE);
 IF NOT FOUND THEN
  INSERT INTO jobs(id,dataset_id,kind,scope_key,target_generation,run_after)
  VALUES(gen_random_uuid(),p_dataset,'training.plan',scope,g,now()+interval '1 second');
 END IF;
END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION rai_training_dirty(uuid,uuid) FROM PUBLIC;
