-- +goose Up
ALTER TABLE /* TEMPLATE: schema */file_parts
 ADD COLUMN IF NOT EXISTS source_username TEXT;

-- +goose Down
ALTER TABLE /* TEMPLATE: schema */file_parts
 DROP COLUMN IF EXISTS source_username;
