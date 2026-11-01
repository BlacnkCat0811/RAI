-- +goose Up
-- Expand the existing audit contract for recovery without a surviving source.
ALTER TABLE admin_operations DROP CONSTRAINT admin_operations_operation_type_check;
ALTER TABLE admin_operations ALTER COLUMN source_dataset DROP NOT NULL;
ALTER TABLE admin_operations ADD CONSTRAINT admin_operations_operation_type_check
    CHECK (operation_type IN ('new_dataset', 'disaster_recovery'));
ALTER TABLE admin_operations ADD CONSTRAINT admin_operations_source_check
    CHECK (operation_type = 'disaster_recovery' OR source_dataset IS NOT NULL);
-- +goose Down
-- Irreversible: committed recovery audit must survive.
