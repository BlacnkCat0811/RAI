-- +goose Up
-- Running fitness v1 (schema4 stack): the day release publishes one
-- running_fitness artifact (single results, long-term state, four
-- dimensions, predictions, zones) in the same generation as every other
-- product. Retired v60 artifacts stay readable rows for audit only.
ALTER TABLE analysis_artifacts DROP CONSTRAINT analysis_artifacts_check;
ALTER TABLE analysis_artifacts ADD CHECK(
 (artifact_key='workout_features' AND schema_version='rai.workout_features/1' AND octet_length(payload::text)<=1048576) OR
 (artifact_key='hr_exposure' AND schema_version='rai.hr_exposure/1' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='training_load' AND schema_version='rai.training_load/1' AND octet_length(payload::text)<=131072) OR
 (artifact_key='load_state' AND schema_version='rai.load_state/1' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='performance_evidence' AND schema_version='rai.performance_evidence/1' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='performance_curve' AND schema_version='rai.performance_curve/1' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='running_ability' AND schema_version='rai.running_ability/1' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='endurance' AND schema_version='rai.endurance/1' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='performance_state' AND schema_version='rai.performance/1' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='training_load' AND schema_version='rai.training_load/2' AND octet_length(payload::text)<=131072) OR
 (artifact_key='load_state' AND schema_version='rai.load_state/2' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='performance_state_v2' AND schema_version='rai.performance/2' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='performance_evidence_v2' AND schema_version='rai.performance_evidence/2' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='performance_curve_v2' AND schema_version='rai.performance_curve/2' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='running_ability_v2' AND schema_version='rai.running_ability/2' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='endurance_v2' AND schema_version='rai.endurance/2' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='vo2max_submax' AND schema_version='rai.vo2max.submax/1' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='physiological_recovery' AND schema_version IN ('rai.recovery.physiological/1','rai.recovery.physiological/2','rai.recovery.physiological/3') AND octet_length(payload::text)<=2097152) OR
 (artifact_key='training_readiness' AND schema_version='rai.readiness.training/2' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='training_readiness' AND schema_version IN ('rai.readiness.training/3','rai.readiness.training/4') AND octet_length(payload::text)<=2097152) OR
 (artifact_key='training_readiness' AND schema_version='rai.readiness.training/1' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='vo2max_external_projection' AND schema_version='rai.vo2max.external_projection/1' AND octet_length(payload::text)<=131072) OR
 ((artifact_key IN ('daily_health_features','daily_health_baseline') AND schema_version IN ('rai.daily_health_features/1','rai.daily_health_baseline/1')) AND octet_length(payload::text)<=2097152) OR
 (artifact_key='running_ability_maximal_v2' AND schema_version='rai.running_ability/2' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='workout_durability' AND schema_version='rai.workout_durability/1' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='aerobic_efficiency' AND schema_version='rai.aerobic_efficiency/1' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='critical_speed' AND schema_version='rai.critical_speed/1' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='session_recovery' AND schema_version='rai.session_recovery/1' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='running_ability_estimate' AND schema_version='rai.running_ability_estimate/1' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='load_focus' AND schema_version='rai.load_focus/1' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='load_settling' AND schema_version='rai.load_settling/1' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='running_fitness' AND schema_version='rai.running_fitness/1' AND octet_length(payload::text)<=2097152)
);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rai_feature_artifact_insert() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE r analysis_runs%ROWTYPE; alg text; ver text;
BEGIN
 SELECT * INTO r FROM analysis_runs WHERE dataset_id=NEW.dataset_id AND id=NEW.run_id FOR SHARE;
 alg:=CASE NEW.artifact_key
  WHEN 'training_readiness' THEN 'rai.readiness.training'
  WHEN 'daily_health_features' THEN 'rai.recovery.physiological'
  WHEN 'daily_health_baseline' THEN 'rai.recovery.physiological'
  WHEN 'physiological_recovery' THEN 'rai.recovery.physiological'
  WHEN 'vo2max_external_projection' THEN 'rai.vo2max.external_projection'
  WHEN 'vo2max_submax' THEN 'rai.vo2max.submax'
  WHEN 'workout_features' THEN 'rai.workout_features'
  WHEN 'hr_exposure' THEN 'rai.workout_features'
  WHEN 'training_load' THEN 'rai.training_load.hrr_integral'
  WHEN 'load_state' THEN 'rai.load_state.exp_finite'
  WHEN 'performance_evidence' THEN 'rai.performance_curve'
  WHEN 'performance_evidence_v2' THEN 'rai.performance_curve'
  WHEN 'performance_curve' THEN 'rai.performance_curve'
  WHEN 'performance_curve_v2' THEN 'rai.performance_curve'
  WHEN 'running_ability' THEN 'rai.performance_curve'
  WHEN 'running_ability_v2' THEN 'rai.performance_curve'
  WHEN 'running_ability_maximal_v2' THEN 'rai.performance_curve'
  WHEN 'endurance' THEN 'rai.performance_curve'
  WHEN 'endurance_v2' THEN 'rai.performance_curve'
  WHEN 'performance_state' THEN 'rai.performance_curve'
  WHEN 'performance_state_v2' THEN 'rai.performance_curve'
  WHEN 'workout_durability' THEN 'rai.workout_durability'
  WHEN 'aerobic_efficiency' THEN 'rai.aerobic_efficiency'
  WHEN 'critical_speed' THEN 'rai.critical_speed'
  WHEN 'session_recovery' THEN 'rai.session_recovery'
  WHEN 'running_ability_estimate' THEN 'rai.running_ability_estimate'
  WHEN 'load_focus' THEN 'rai.load_focus'
  WHEN 'load_settling' THEN 'rai.load_settling'
  WHEN 'running_fitness' THEN 'rai.running_fitness' END;
 ver:=CASE
  WHEN NEW.schema_version='rai.readiness.training/4' THEN 'v4'
  WHEN NEW.schema_version IN ('rai.readiness.training/3','rai.recovery.physiological/3') THEN 'v3'
  WHEN NEW.schema_version IN (
   'rai.training_load/2','rai.load_state/2','rai.performance/2','rai.performance_evidence/2',
   'rai.performance_curve/2','rai.running_ability/2','rai.endurance/2',
   'rai.recovery.physiological/2','rai.readiness.training/2') THEN 'v2'
  ELSE 'v1' END;
 IF NOT FOUND OR r.status<>'RUNNING' OR r.algorithm_name IS DISTINCT FROM alg OR r.algorithm_version IS DISTINCT FROM ver
 OR (NEW.payload->>'schemaVersion') IS DISTINCT FROM NEW.schema_version OR (NEW.payload->>'datasetID') IS DISTINCT FROM NEW.dataset_id::text
 OR COALESCE((NEW.payload->>'generation')::bigint,(NEW.payload->>'inputGeneration')::bigint) IS DISTINCT FROM r.target_generation THEN
 RAISE EXCEPTION 'feature_artifact_invalid_run' USING ERRCODE='RA041'; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd


CREATE OR REPLACE VIEW analysis_published_artifacts WITH (security_barrier=true) AS
SELECT a.dataset_id, a.run_id, a.artifact_key, a.schema_version, a.content_sha256, a.payload
FROM analysis_artifacts a
JOIN analysis_runs r ON r.dataset_id=a.dataset_id AND r.id=a.run_id AND r.status='SUCCEEDED'
JOIN analysis_model_channels m ON m.dataset_id=r.dataset_id AND m.channel='active' AND m.stack_id<>'legacy'
JOIN datasets ds ON ds.id=m.dataset_id AND ds.retired_at IS NULL
WHERE left(r.scope_key,length(m.stack_id)+7)='model/'||m.stack_id||'/'
 AND a.artifact_key IN ('workout_features','load_state','performance_state_v2','vo2max_external_projection',
     'vo2max_submax','physiological_recovery','training_readiness','session_recovery','running_ability_estimate','load_focus',
     'running_fitness')
 AND EXISTS (
    SELECT 1
    FROM analysis_publications p
    JOIN dirty_scopes d ON d.dataset_id=p.dataset_id AND d.scope_key=p.scope_key
      AND d.generation=p.generation AND d.clean_generation=p.generation
    WHERE p.dataset_id=r.dataset_id AND p.published_run_id=r.id
      AND p.scope_key=r.scope_key AND p.generation=r.target_generation
 );



-- Every successful running-fitness day run of the active stack, including
-- runs whose publication pointer was later replaced. Runs and artifacts are
-- immutable and never deleted, so "what was published for a day by the end
-- of that day" (the published history view) is reconstructible from them.
CREATE VIEW run_fitness_day_runs WITH (security_barrier=true) AS
SELECT a.dataset_id, a.run_id, r.scope_key, r.target_generation, r.finished_at, r.lineage->>'manifestSHA256' AS manifest_sha256,
 r.algorithm_name, r.algorithm_version, a.schema_version, a.content_sha256, a.payload
FROM analysis_artifacts a
JOIN analysis_runs r ON r.dataset_id=a.dataset_id AND r.id=a.run_id AND r.status='SUCCEEDED'
JOIN analysis_model_channels m ON m.dataset_id=r.dataset_id AND m.channel='active' AND m.stack_id<>'legacy'
JOIN datasets ds ON ds.id=m.dataset_id AND ds.retired_at IS NULL
WHERE a.artifact_key='running_fitness' AND left(r.scope_key,length(m.stack_id)+7)='model/'||m.stack_id||'/';

-- Explicit running-fitness recomputation requests (single re-analysis or a
-- date range). A request bumps the generation of already-published day
-- releases of the active stack in its bounded affected range; the worker's
-- history rebuild recomputes them newest first. Each day release is replaced
-- atomically; status is derived from the recorded target generations and is
-- never written as "completed" by the request itself.
CREATE TABLE run_fitness_reanalysis_operations (
    dataset_id uuid NOT NULL REFERENCES datasets(id),
    id uuid NOT NULL,
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    request_sha256 bytea NOT NULL CHECK (octet_length(request_sha256)=32),
    stack_id text NOT NULL,
    manifest_sha256 bytea NOT NULL CHECK (octet_length(manifest_sha256)=32),
    view text NOT NULL CHECK (view='revised'),
    reference_policy text NOT NULL,
    algorithm_version text NOT NULL,
    request jsonb NOT NULL,
    from_day date NOT NULL,
    through_day date NOT NULL CHECK (through_day >= from_day AND through_day - from_day <= 455),
    targets jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (dataset_id, id),
    UNIQUE (dataset_id, idempotency_key)
);
CREATE TRIGGER immutable_run_fitness_reanalysis BEFORE UPDATE OR DELETE ON run_fitness_reanalysis_operations
 FOR EACH ROW EXECUTE FUNCTION rai_analysis_immutable();

-- +goose StatementBegin
CREATE FUNCTION rai_run_fitness_reanalysis(p_dataset uuid,p_id uuid,p_key text,p_request_sha256 bytea,p_request jsonb,
 p_from date,p_through date,p_reference_policy text,p_algorithm text)
RETURNS uuid LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE tz text; today date; m record; prefix text; existing run_fitness_reanalysis_operations%ROWTYPE;
 affected_through date; targets jsonb;
BEGIN
 SELECT timezone INTO tz FROM datasets WHERE id=p_dataset AND retired_at IS NULL FOR UPDATE;
 IF NOT FOUND THEN RAISE EXCEPTION 'reanalysis_dataset_missing' USING ERRCODE='RA057'; END IF;
 SELECT * INTO existing FROM run_fitness_reanalysis_operations WHERE dataset_id=p_dataset AND idempotency_key=p_key;
 IF FOUND THEN
  IF existing.request_sha256<>p_request_sha256 THEN
   RAISE EXCEPTION 'reanalysis_idempotency_conflict' USING ERRCODE='RA058';
  END IF;
  RETURN existing.id;
 END IF;
 SELECT stack_id,config_sha256 INTO m FROM analysis_model_channels
  WHERE dataset_id=p_dataset AND channel='active' AND extension_configs ? 'rai.model_manifest/1';
 IF NOT FOUND THEN RAISE EXCEPTION 'reanalysis_not_activated' USING ERRCODE='RA059'; END IF;
 today:=(clock_timestamp() AT TIME ZONE tz)::date;
 IF p_from>p_through OR p_through>today OR p_from<today-365 THEN
  RAISE EXCEPTION 'reanalysis_range_invalid' USING ERRCODE='RA060';
 END IF;
 -- Confirmed race and test evidence lasts 90 days: d..d+89 bounds every
 -- downstream day of a changed single result.
 affected_through:=LEAST(today,p_through+89);
 prefix:='model/'||m.stack_id||'/athlete/day/v2/';
 UPDATE dirty_scopes SET generation=generation+1
  WHERE dataset_id=p_dataset AND scope_key>=prefix||p_from::text AND scope_key<=prefix||affected_through::text
   AND left(scope_key,length(prefix))=prefix
   AND EXISTS(SELECT 1 FROM analysis_publications p WHERE p.dataset_id=dirty_scopes.dataset_id AND p.scope_key=dirty_scopes.scope_key);
 IF today BETWEEN p_from AND affected_through THEN
  PERFORM rai_schedule_model_day(p_dataset,m.stack_id,today,clock_timestamp());
 END IF;
 SELECT COALESCE(jsonb_object_agg(scope_key,generation ORDER BY scope_key),'{}'::jsonb) INTO targets FROM dirty_scopes
  WHERE dataset_id=p_dataset AND scope_key>=prefix||p_from::text AND scope_key<=prefix||affected_through::text
   AND left(scope_key,length(prefix))=prefix;
 INSERT INTO run_fitness_reanalysis_operations(dataset_id,id,idempotency_key,request_sha256,stack_id,manifest_sha256,view,
  reference_policy,algorithm_version,request,from_day,through_day,targets)
 VALUES(p_dataset,p_id,p_key,p_request_sha256,m.stack_id,m.config_sha256,'revised',p_reference_policy,p_algorithm,p_request,
  p_from,affected_through,targets);
 RETURN p_id;
END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION rai_run_fitness_reanalysis(uuid,uuid,text,bytea,jsonb,date,date,text,text) FROM PUBLIC;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='rai_api') THEN
  GRANT EXECUTE ON FUNCTION rai_run_fitness_reanalysis(uuid,uuid,text,bytea,jsonb,date,date,text,text) TO rai_api;
  GRANT SELECT ON run_fitness_reanalysis_operations, run_fitness_day_runs TO rai_api;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='rai_worker') THEN
  GRANT SELECT ON run_fitness_reanalysis_operations TO rai_worker;
 END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- Published model identities are retained for binary rollback.
