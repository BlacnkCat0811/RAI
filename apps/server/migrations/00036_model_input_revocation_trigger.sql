-- +goose Up
-- A revocation is an append-only event whose kind is 'revocation' but whose
-- schema is the schema of the input it revokes. The original trigger required
-- target.kind = NEW.kind, which made that representation impossible.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rai_model_input_insert_v2() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target record;
BEGIN
 IF NEW.revokes_id IS NOT NULL THEN
  SELECT kind,subject_key,schema_version INTO target FROM athlete_model_inputs WHERE dataset_id=NEW.dataset_id AND id=NEW.revokes_id;
  IF NOT FOUND OR target.subject_key<>NEW.subject_key THEN
   RAISE EXCEPTION 'model_input_revokes_invalid' USING ERRCODE='RA043';
  END IF;
  IF NEW.kind='revocation' THEN
   IF NEW.schema_version<>target.schema_version THEN
    RAISE EXCEPTION 'model_input_revokes_invalid' USING ERRCODE='RA043';
   END IF;
  ELSIF target.kind<>NEW.kind THEN
   RAISE EXCEPTION 'model_input_revokes_invalid' USING ERRCODE='RA043';
  END IF;
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rai_model_input_insert_v2() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target record;
BEGIN
 IF NEW.revokes_id IS NOT NULL THEN
  SELECT kind,subject_key INTO target FROM athlete_model_inputs WHERE dataset_id=NEW.dataset_id AND id=NEW.revokes_id;
  IF NOT FOUND OR target.kind<>NEW.kind OR target.subject_key<>NEW.subject_key THEN
   RAISE EXCEPTION 'model_input_revokes_invalid' USING ERRCODE='RA043';
  END IF;
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
