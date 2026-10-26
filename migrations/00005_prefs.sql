-- +goose Up
-- The player's own preferences, kept on the server so every device gets them (the compass
-- style first). A JSON object: new preferences need no migration.
ALTER TABLE users ADD COLUMN prefs jsonb NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE users DROP COLUMN prefs;
