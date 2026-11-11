-- +goose Up
-- Schema3 model artifacts are candidate-only until owner activation.
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
 (artifact_key='load_settling' AND schema_version='rai.load_settling/1' AND octet_length(payload::text)<=2097152)
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
  WHEN 'load_settling' THEN 'rai.load_settling' END;
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
     'vo2max_submax','physiological_recovery','training_readiness','session_recovery','running_ability_estimate','load_focus')
 AND EXISTS (
    SELECT 1
    FROM analysis_publications p
    JOIN dirty_scopes d ON d.dataset_id=p.dataset_id AND d.scope_key=p.scope_key
      AND d.generation=p.generation AND d.clean_generation=p.generation
    WHERE p.dataset_id=r.dataset_id AND p.published_run_id=r.id
      AND p.scope_key=r.scope_key AND p.generation=r.target_generation
 );


-- +goose Down
-- Published model identities are retained for binary rollback.
