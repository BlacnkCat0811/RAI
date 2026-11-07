-- +goose Up
-- Typed, append-only personal model inputs. v1 hr_reference rows stay readable
-- and are not reinterpreted. Every new kind uses the same fact table and the
-- same receipt/dirty/job transaction; no per-model queues are introduced.
ALTER TABLE athlete_model_inputs ADD COLUMN IF NOT EXISTS revokes_id uuid;
ALTER TABLE athlete_model_inputs ADD COLUMN IF NOT EXISTS input_schema text;
ALTER TABLE athlete_model_inputs DROP CONSTRAINT IF EXISTS athlete_model_inputs_kind_check;
ALTER TABLE athlete_model_inputs DROP CONSTRAINT IF EXISTS athlete_model_inputs_schema_version_check;
ALTER TABLE athlete_model_inputs DROP CONSTRAINT IF EXISTS athlete_model_inputs_payload_check;
ALTER TABLE athlete_model_inputs ADD CONSTRAINT athlete_model_inputs_kind_check CHECK(kind IN ('hr_reference','sleep_target','workout_effort','workout_overlap_resolution','source_context_selection','revocation'));
ALTER TABLE athlete_model_inputs ADD CONSTRAINT athlete_model_inputs_schema_version_check CHECK(schema_version IN ('rai.hr_reference/1','rai.hr_reference/2','rai.sleep_target/1','rai.workout_effort/1','rai.workout_overlap_resolution/1','rai.source_context_selection/1'));
ALTER TABLE athlete_model_inputs ADD CONSTRAINT athlete_model_inputs_payload_v2_check CHECK(jsonb_typeof(payload)='object' AND octet_length(payload::text)<=65536);
ALTER TABLE athlete_model_inputs ADD CONSTRAINT athlete_model_inputs_revokes_check CHECK(revokes_id IS NULL OR supersedes_id IS NULL);
ALTER TABLE athlete_model_inputs ADD CONSTRAINT athlete_model_inputs_revokes_fk FOREIGN KEY(dataset_id,revokes_id) REFERENCES athlete_model_inputs(dataset_id,id);
CREATE INDEX IF NOT EXISTS athlete_model_inputs_kind_subject_time ON athlete_model_inputs(dataset_id,kind,subject_key,effective_from DESC,id);

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
DROP TRIGGER IF EXISTS model_input_insert_v2 ON athlete_model_inputs;
CREATE TRIGGER model_input_insert_v2 BEFORE INSERT ON athlete_model_inputs FOR EACH ROW EXECUTE FUNCTION rai_model_input_insert_v2();

CREATE TABLE IF NOT EXISTS model_input_receipts (
 dataset_id uuid NOT NULL REFERENCES datasets(id), input_id uuid NOT NULL,
 content_sha256 bytea NOT NULL CHECK(octet_length(content_sha256)=32),
 receipt jsonb NOT NULL CHECK(jsonb_typeof(receipt)='object' AND octet_length(receipt::text)<=16384),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(dataset_id,input_id),
 FOREIGN KEY(dataset_id,input_id) REFERENCES athlete_model_inputs(dataset_id,id)
);
CREATE TRIGGER immutable_model_input_receipts BEFORE UPDATE OR DELETE ON model_input_receipts FOR EACH ROW EXECUTE FUNCTION rai_analysis_immutable();

-- Active stack is still an explicit assignment, not a boolean. The old
-- M1-only CHECK is replaced by one that accepts reviewed stack ids; promotion
-- authority remains in the admin executor and audit tables below.
ALTER TABLE analysis_model_channels DROP CONSTRAINT IF EXISTS analysis_model_channels_check;
ALTER TABLE analysis_model_channels ADD CONSTRAINT analysis_model_channels_stack_check CHECK(length(stack_id) BETWEEN 1 AND 96);

CREATE TABLE IF NOT EXISTS analysis_assignment_audit (
 dataset_id uuid NOT NULL REFERENCES datasets(id),
 revision bigint NOT NULL CHECK(revision>0),
 action text NOT NULL CHECK(action IN ('promote','rollback')),
 from_stack text,
 to_stack text NOT NULL,
 manifest_sha256 bytea NOT NULL CHECK(octet_length(manifest_sha256)=32),
 gate_summary jsonb NOT NULL CHECK(jsonb_typeof(gate_summary)='object' AND octet_length(gate_summary::text)<=65536),
 actor text NOT NULL CHECK(length(actor) BETWEEN 1 AND 200),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(dataset_id,revision)
);
CREATE TRIGGER immutable_analysis_assignment_audit BEFORE UPDATE OR DELETE ON analysis_assignment_audit FOR EACH ROW EXECUTE FUNCTION rai_analysis_immutable();

-- One generic, restartable operation ledger shared by plan/execute/resume/
-- status/diff/cancel. Blocks are fragments of the same manifest-bound run.
CREATE TABLE IF NOT EXISTS model_backfill_operations (
 dataset_id uuid NOT NULL REFERENCES datasets(id), id uuid NOT NULL,
 operation text NOT NULL CHECK(operation IN ('plan','execute','resume','cancel')),
 view text NOT NULL CHECK(view IN ('revised','as_known')),
 stack_id text NOT NULL,
 manifest_sha256 bytea NOT NULL CHECK(octet_length(manifest_sha256)=32),
 from_date date NOT NULL, through_date date NOT NULL,
 as_of timestamptz NOT NULL,
 status text NOT NULL CHECK(status IN ('planned','running','paused','candidate_ready','failed','cancelled')),
 total_blocks integer NOT NULL CHECK(total_blocks>0),
 completed_blocks integer NOT NULL DEFAULT 0 CHECK(completed_blocks>=0 AND completed_blocks<=total_blocks),
 checkpoint jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(jsonb_typeof(checkpoint)='object' AND octet_length(checkpoint::text)<=65536),
 actor text NOT NULL CHECK(length(actor) BETWEEN 1 AND 200),
 error_code text,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(dataset_id,id),
 CHECK(through_date>=from_date AND through_date-from_date<=365)
);
CREATE INDEX IF NOT EXISTS model_backfill_operations_status ON model_backfill_operations(dataset_id,status,updated_at DESC);

-- +goose Down
DROP TABLE IF EXISTS model_backfill_operations;
DROP TABLE IF EXISTS analysis_assignment_audit;
DROP TABLE IF EXISTS model_input_receipts;
DROP TRIGGER IF EXISTS model_input_insert_v2 ON athlete_model_inputs;
DROP FUNCTION IF EXISTS rai_model_input_insert_v2();
ALTER TABLE analysis_model_channels DROP CONSTRAINT IF EXISTS analysis_model_channels_stack_check;
ALTER TABLE analysis_model_channels ADD CONSTRAINT analysis_model_channels_check CHECK(channel<>'active' OR stack_id='legacy');
ALTER TABLE athlete_model_inputs DROP CONSTRAINT IF EXISTS athlete_model_inputs_revokes_fk;
ALTER TABLE athlete_model_inputs DROP CONSTRAINT IF EXISTS athlete_model_inputs_revokes_check;
ALTER TABLE athlete_model_inputs DROP CONSTRAINT IF EXISTS athlete_model_inputs_payload_v2_check;
ALTER TABLE athlete_model_inputs DROP CONSTRAINT IF EXISTS athlete_model_inputs_schema_version_check;
ALTER TABLE athlete_model_inputs DROP CONSTRAINT IF EXISTS athlete_model_inputs_kind_check;
ALTER TABLE athlete_model_inputs DROP COLUMN IF EXISTS input_schema;
ALTER TABLE athlete_model_inputs DROP COLUMN IF EXISTS revokes_id;
