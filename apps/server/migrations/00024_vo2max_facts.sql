-- +goose Up
-- Additive raw transport only. No projection, selection, artifact or handler.
ALTER TABLE daily_health_coverage
 ADD COLUMN flow_created_at_us bigint,
 ADD COLUMN query_completed_at_us bigint,
 ADD COLUMN anchor_present boolean,
 ADD COLUMN source_summary jsonb,
 ADD COLUMN summary_observation_seq bigint;
ALTER TABLE daily_health_coverage ADD CONSTRAINT vo2_observation_context CHECK (
 flow_created_at_us IS NULL OR (
 flow_created_at_us-bootstrap_start_us=7776000000000 AND
 query_completed_at_us>=flow_created_at_us AND anchor_present IS TRUE AND
 jsonb_typeof(source_summary)='array' AND jsonb_array_length(source_summary)<=128 AND
 summary_observation_seq>=0));
ALTER TABLE health_samples ADD CONSTRAINT vo2_fact_context CHECK (
 NOT (metadata ? 'RAI:vo2maxFactV1') OR COALESCE((
 sample_type='HKQuantityTypeIdentifierVO2Max' AND unit='mL/kg/min' AND value_numeric>0 AND
 jsonb_typeof(metadata->'RAI:vo2maxFactV1')='object' AND
 metadata->'RAI:vo2maxFactV1'->>'schemaVersion'='rai.healthkit.vo2max_fact/1' AND
 metadata->'RAI:vo2maxFactV1'->'provenance'->>'schemaVersion'='rai.healthkit.provenance/2' AND
 metadata->'RAI:vo2maxFactV1'->'provenance'->'sourceRevision'->'source'->>'bundleIdentifier'=source_bundle_id AND
 metadata->'RAI:vo2maxFactV1'->>'sourceEpochID' ~ '^sha256:[0-9a-f]{64}$' AND
 jsonb_typeof(metadata->'RAI:vo2maxFactV1'->'typedMetadata')='object' AND
 octet_length((metadata->'RAI:vo2maxFactV1')::text)<=65536),false));

-- Necessary: live raw rows are removed on deletion. Capture only descriptor,
-- interval/hash and origin inputs before removal for every deletion entry point.
-- No root value or associated HKVO2MaxValue is copied into the tombstone.
ALTER TABLE entity_tombstones ADD COLUMN provenance_snapshot jsonb;
-- +goose StatementBegin
CREATE FUNCTION rai_vo2_delete_context() RETURNS trigger LANGUAGE plpgsql
SET search_path=pg_catalog,public AS $$
DECLARE c jsonb; s timestamptz; e timestamptz; h bytea;
BEGIN
 IF NEW.sample_type <> 'HKQuantityTypeIdentifierVO2Max' THEN RETURN NEW; END IF;
 SELECT metadata->'RAI:vo2maxFactV1',event_start,event_end,content_sha256 INTO c,s,e,h
 FROM public.health_samples WHERE dataset_id=NEW.dataset_id AND sample_type=NEW.sample_type AND sample_uuid=NEW.sample_uuid;
 NEW.provenance_snapshot := NULL;
 IF c IS NOT NULL THEN
  NEW.provenance_snapshot := jsonb_build_object('schemaVersion','rai.healthkit.vo2max_withdrawal/1',
   'provenance',c->'provenance','sourceEpochID',c->'sourceEpochID','contentSHA256',encode(h,'hex'),
   'startUs',(extract(epoch FROM s)*1000000)::bigint,'endUs',(extract(epoch FROM e)*1000000)::bigint,
   'originInputs', jsonb_strip_nulls(jsonb_build_object('HKVO2MaxTestType',c->'typedMetadata'->'HKVO2MaxTestType',
     'HKWasUserEntered',c->'typedMetadata'->'HKWasUserEntered',
     'HKMetadataKeyAppleDeviceCalibrated',c->'typedMetadata'->'HKMetadataKeyAppleDeviceCalibrated')));
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION rai_vo2_delete_context() FROM PUBLIC;
CREATE TRIGGER vo2_delete_context BEFORE INSERT ON entity_tombstones FOR EACH ROW EXECUTE FUNCTION rai_vo2_delete_context();
-- Existing PK indexes cover UUID, tombstone, stream sequence and coverage.
-- Candidate epoch/end indexes belong to B3, after measured query evidence.
