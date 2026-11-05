-- +goose Up
-- Independent control-plane lineage; immutable v3 facts/receipts are unchanged.
CREATE TABLE workout_sync_completeness (
 dataset_id uuid NOT NULL REFERENCES datasets(id),
 dataset_epoch uuid NOT NULL,
 collector_epoch uuid NOT NULL,
 generation bigint NOT NULL CHECK(generation>0),
 manifest_id uuid NOT NULL,
 stream_set_version integer NOT NULL CHECK(stream_set_version=1),
 window_from_us bigint NOT NULL,
 known_cutoff_us bigint NOT NULL CHECK(known_cutoff_us>window_from_us),
 fact_revision bigint NOT NULL CHECK(fact_revision>=0),
 commit_rev bigint NOT NULL CHECK(commit_rev>fact_revision),
 request_sha256 bytea NOT NULL CHECK(octet_length(request_sha256)=32),
 head_state jsonb NOT NULL CHECK(jsonb_typeof(head_state)='object' AND octet_length(head_state::text)<=2097152),
 inventory jsonb NOT NULL CHECK(jsonb_typeof(inventory)='array' AND jsonb_array_length(inventory)<=256),
 request_bytes bytea NOT NULL CHECK(octet_length(request_bytes) BETWEEN 1 AND 16777216),
 receipt bytea NOT NULL CHECK(octet_length(receipt) BETWEEN 1 AND 8192),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(dataset_id,collector_epoch,generation),
 UNIQUE(dataset_id,manifest_id)
);
CREATE INDEX workout_completeness_fact_revision ON change_events(dataset_id,commit_rev DESC)
 WHERE entity_kind IN ('workout_bundle','workout','workout_child_delete');
-- +goose StatementBegin
CREATE FUNCTION rai_workout_completeness_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'workout_completeness_immutable' USING ERRCODE='RA045'; END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION rai_workout_completeness_immutable() FROM PUBLIC;
CREATE TRIGGER workout_completeness_immutable BEFORE UPDATE OR DELETE ON workout_sync_completeness
 FOR EACH ROW EXECUTE FUNCTION rai_workout_completeness_immutable();
-- +goose StatementBegin
CREATE FUNCTION rai_workout_completeness_dirty() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN PERFORM public.rai_readiness_dirty(NEW.dataset_id); RETURN NEW; END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION rai_workout_completeness_dirty() FROM PUBLIC;
CREATE TRIGGER workout_completeness_dirty AFTER INSERT ON workout_sync_completeness
 FOR EACH ROW EXECUTE FUNCTION rai_workout_completeness_dirty();

-- +goose Down
DROP TABLE workout_sync_completeness;
DROP FUNCTION rai_workout_completeness_dirty();
DROP FUNCTION rai_workout_completeness_immutable();
DROP INDEX workout_completeness_fact_revision;
