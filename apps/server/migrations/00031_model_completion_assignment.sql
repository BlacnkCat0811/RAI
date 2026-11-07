-- +goose Up
-- Active is complemented by one bounded previous stack for rollback. The
-- channel remains an explicit assignment, never a boolean.
ALTER TABLE analysis_model_channels DROP CONSTRAINT IF EXISTS analysis_model_channels_check;
ALTER TABLE analysis_model_channels DROP CONSTRAINT IF EXISTS analysis_model_channels_channel_check;
ALTER TABLE analysis_model_channels ADD CONSTRAINT analysis_model_channels_channel_check CHECK(channel IN ('active','candidate','previous'));
ALTER TABLE analysis_model_channels ADD CONSTRAINT analysis_model_channels_active_stack_check CHECK(channel<>'active' OR length(stack_id) BETWEEN 1 AND 96);
-- +goose Down
ALTER TABLE analysis_model_channels DROP CONSTRAINT IF EXISTS analysis_model_channels_active_stack_check;
ALTER TABLE analysis_model_channels DROP CONSTRAINT IF EXISTS analysis_model_channels_channel_check;
ALTER TABLE analysis_model_channels ADD CONSTRAINT analysis_model_channels_channel_check CHECK(channel IN ('active','candidate'));
