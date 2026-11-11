-- +goose Up
-- Daily health facts are written one statement per row, and every row used to
-- run the whole model-stack invalidation: a 500-fact page bumped each affected
-- day scope 500 times and queued 500 day jobs. The coverage row, rewritten by
-- every page (also the empty heartbeat), invalidated the last 366 days.
--
-- Fact and coverage triggers now only widen one pending range per dataset and
-- transaction. A deferred trigger applies it once at commit, inside the same
-- transaction as the facts, so a page costs one invalidation and one day job.
-- Coverage describes how fresh the sync is right now; it reschedules today and
-- leaves published history alone.
CREATE TABLE model_stack_pending_invalidations (
    dataset_id uuid NOT NULL REFERENCES datasets(id),
    txid bigint NOT NULL,
    from_at timestamptz NOT NULL,
    through_at timestamptz NOT NULL,
    PRIMARY KEY (dataset_id, txid),
    CHECK (from_at <= through_at)
);

-- +goose StatementBegin
CREATE FUNCTION rai_model_stack_defer_invalidation(p_dataset uuid,p_from timestamptz,p_through timestamptz)
RETURNS void LANGUAGE sql AS $$
 INSERT INTO model_stack_pending_invalidations(dataset_id,txid,from_at,through_at)
 VALUES(p_dataset,txid_current(),LEAST(p_from,p_through),GREATEST(p_from,p_through))
 ON CONFLICT(dataset_id,txid) DO UPDATE SET
  from_at=LEAST(model_stack_pending_invalidations.from_at,EXCLUDED.from_at),
  through_at=GREATEST(model_stack_pending_invalidations.through_at,EXCLUDED.through_at)
$$;
-- +goose StatementEnd

-- Fires once per dataset and transaction, at commit: the row is inserted by the
-- first deferred change and only updated by later ones.
-- +goose StatementBegin
CREATE FUNCTION rai_model_stack_flush_invalidation() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE r record;
BEGIN
 DELETE FROM model_stack_pending_invalidations WHERE dataset_id=NEW.dataset_id AND txid=NEW.txid
  RETURNING from_at,through_at INTO r;
 IF FOUND THEN
  PERFORM rai_model_stack_invalidate(NEW.dataset_id,r.from_at,r.through_at,NULL);
 END IF;
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER model_stack_flush_invalidation AFTER INSERT ON model_stack_pending_invalidations
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION rai_model_stack_flush_invalidation();

-- Same rules as schema 41 except for the two deferred sources.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rai_model_stack_dirty_trigger() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE ds uuid; from_at timestamptz; through_at timestamptz; wid text; deferred boolean:=false;
BEGIN
 IF TG_TABLE_NAME='dirty_scopes' THEN
  IF TG_OP='UPDATE' AND NEW.generation=OLD.generation THEN RETURN NEW; END IF;
  IF NEW.scope_key LIKE 'workout:HKWorkoutTypeIdentifier:%' THEN
   wid:=right(NEW.scope_key,36);
  ELSIF NOT (NEW.scope_key LIKE 'daily/raw/%' OR NEW.scope_key IN
   ('external/vo2max/facts/v1','external/vo2max/selection/v1','readiness/load-coverage/v1')) THEN
   RETURN NEW;
  END IF;
  ds:=NEW.dataset_id;
  from_at:=NEW.dirty_from;
  through_at:=NEW.dirty_through;
 ELSIF TG_TABLE_NAME='athlete_model_inputs' THEN
  ds:=NEW.dataset_id;
  from_at:=LEAST(NEW.effective_from,clock_timestamp());
  through_at:=clock_timestamp();
  IF NEW.revokes_id IS NOT NULL THEN
   SELECT LEAST(effective_from,from_at) INTO from_at FROM athlete_model_inputs
    WHERE dataset_id=ds AND id=NEW.revokes_id;
  END IF;
 ELSIF TG_TABLE_NAME='health_samples' THEN
  IF TG_OP='DELETE' THEN
   IF OLD.sample_type NOT IN ('HKQuantityTypeIdentifierHeartRateVariabilitySDNN','HKQuantityTypeIdentifierRestingHeartRate','HKCategoryTypeIdentifierSleepAnalysis','HKQuantityTypeIdentifierVO2Max') THEN RETURN NULL; END IF;
   ds:=OLD.dataset_id; from_at:=OLD.event_start; through_at:=OLD.event_end;
  ELSE
   IF NEW.sample_type NOT IN ('HKQuantityTypeIdentifierHeartRateVariabilitySDNN','HKQuantityTypeIdentifierRestingHeartRate','HKCategoryTypeIdentifierSleepAnalysis','HKQuantityTypeIdentifierVO2Max') THEN RETURN NULL; END IF;
   ds:=NEW.dataset_id; from_at:=NEW.event_start; through_at:=NEW.event_end;
  END IF;
  from_at:=from_at-interval '1 day';
  deferred:=true;
 ELSIF TG_TABLE_NAME='daily_health_coverage' THEN
  ds:=NEW.dataset_id;
  from_at:=clock_timestamp();
  through_at:=clock_timestamp();
  deferred:=true;
 ELSIF TG_TABLE_NAME='devices' THEN
  IF NEW.revoked_at IS NOT DISTINCT FROM OLD.revoked_at AND NEW.can_collect IS NOT DISTINCT FROM OLD.can_collect THEN RETURN NEW; END IF;
  ds:=NEW.dataset_id;
  from_at:=clock_timestamp()-interval '366 days';
  through_at:=clock_timestamp();
 ELSIF TG_TABLE_NAME='datasets' THEN
  IF NEW.timezone IS NOT DISTINCT FROM OLD.timezone AND NEW.epoch IS NOT DISTINCT FROM OLD.epoch
   AND NEW.retired_at IS NOT DISTINCT FROM OLD.retired_at THEN RETURN NEW; END IF;
  ds:=NEW.id;
  from_at:=clock_timestamp()-interval '366 days';
  through_at:=clock_timestamp();
 ELSE
  IF TG_OP='DELETE' THEN ds:=OLD.dataset_id; ELSE ds:=NEW.dataset_id; END IF;
  from_at:=clock_timestamp()-interval '366 days';
  through_at:=clock_timestamp();
 END IF;
 IF EXISTS(SELECT 1 FROM analysis_model_channels WHERE dataset_id=ds AND extension_configs ? 'rai.model_manifest/1') THEN
  IF deferred THEN
   PERFORM rai_model_stack_defer_invalidation(ds,from_at,through_at);
  ELSE
   PERFORM rai_model_stack_invalidate(ds,from_at,through_at,wid);
  END IF;
 END IF;
 RETURN NULL;
END $$;
-- +goose StatementEnd

-- A fact that arrives late (last night's sleep, a run uploaded the next day)
-- invalidates day releases that were already published. The worker calls this
-- on its recovery tick to queue a bounded number of them again, newest first,
-- and only while no day job of the dataset is waiting or running, so a long
-- backfill is not recomputed page by page. Days that were never published are
-- not created here.
-- +goose StatementBegin
CREATE FUNCTION rai_model_stack_rebuild_history(p_dataset uuid,p_stack text,p_at timestamptz,p_limit integer)
RETURNS integer LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE tz text; today date; prefix text; queued integer;
BEGIN
 SELECT timezone INTO tz FROM datasets WHERE id=p_dataset AND retired_at IS NULL FOR UPDATE;
 IF NOT FOUND THEN RETURN 0; END IF;
 IF NOT EXISTS(SELECT 1 FROM analysis_model_channels WHERE dataset_id=p_dataset AND stack_id=p_stack
  AND extension_configs ? 'rai.model_manifest/1') THEN RETURN 0; END IF;
 IF EXISTS(SELECT 1 FROM jobs WHERE dataset_id=p_dataset AND kind='analysis.model_stack_v2'
  AND state IN ('queued','running')) THEN RETURN 0; END IF;
 today:=(p_at AT TIME ZONE tz)::date;
 prefix:='model/'||p_stack||'/athlete/day/v2/';
 WITH stale AS (
  SELECT d.scope_key,d.generation FROM dirty_scopes d
  WHERE d.dataset_id=p_dataset AND d.generation<>d.clean_generation
   AND d.scope_key >= prefix||(today-365)::text AND d.scope_key < prefix||today::text
   AND left(d.scope_key,length(prefix))=prefix
   AND EXISTS(SELECT 1 FROM analysis_publications p WHERE p.dataset_id=d.dataset_id AND p.scope_key=d.scope_key)
   AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.dataset_id=d.dataset_id AND j.kind='analysis.model_stack_v2'
    AND j.scope_key=d.scope_key AND j.target_generation=d.generation)
  ORDER BY d.scope_key DESC LIMIT GREATEST(p_limit,0)
 ), added AS (
  INSERT INTO jobs(id,dataset_id,kind,scope_key,target_generation)
  SELECT gen_random_uuid(),p_dataset,'analysis.model_stack_v2',scope_key,generation FROM stale
  ON CONFLICT(dataset_id,kind,scope_key,target_generation) DO NOTHING RETURNING 1
 ) SELECT count(*) INTO queued FROM added;
 RETURN queued;
END $$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION rai_model_stack_defer_invalidation(uuid,timestamptz,timestamptz),
 rai_model_stack_flush_invalidation(),rai_model_stack_rebuild_history(uuid,text,timestamptz,integer) FROM PUBLIC;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='rai_worker') THEN
  GRANT EXECUTE ON FUNCTION rai_model_stack_rebuild_history(uuid,text,timestamptz,integer) TO rai_worker;
 END IF;
END $$;
-- +goose StatementEnd

-- Day jobs left behind by the per-row trigger: a queued job whose scope already
-- has a newer job can only end as SUPERSEDED. History is kept.
UPDATE jobs j SET state='failed',last_error_code='superseded'
 WHERE j.state='queued' AND j.kind='analysis.model_stack_v2'
  AND EXISTS(SELECT 1 FROM jobs n WHERE n.dataset_id=j.dataset_id AND n.kind=j.kind
   AND n.scope_key=j.scope_key AND n.target_generation>j.target_generation);

-- +goose Down
-- No Down: the schema 41 trigger body remains in migration 00041. An older
-- binary keeps working against this schema; restoring per-row invalidation
-- would only bring back the duplicate day jobs.
