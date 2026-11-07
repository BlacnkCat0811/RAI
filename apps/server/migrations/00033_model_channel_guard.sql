-- +goose Up
-- Active assignment changes must go through the promotion/rollback transaction,
-- which sets a transaction-local capability. Direct SQL cannot silently move
-- the readable channel between stacks.
-- +goose StatementBegin
CREATE FUNCTION rai_model_channel_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.channel='active' AND current_setting('rai.model_assignment', true) IS DISTINCT FROM 'on' THEN
    RAISE EXCEPTION 'model_assignment_forbidden' USING ERRCODE='RA046';
  END IF;
  RETURN COALESCE(NEW, OLD);
END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION rai_model_channel_guard() FROM PUBLIC;
CREATE TRIGGER model_channel_guard BEFORE UPDATE OR DELETE ON analysis_model_channels
 FOR EACH ROW EXECUTE FUNCTION rai_model_channel_guard();
-- +goose Down
DROP TRIGGER IF EXISTS model_channel_guard ON analysis_model_channels;
DROP FUNCTION IF EXISTS rai_model_channel_guard();
