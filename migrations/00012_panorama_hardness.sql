-- +goose Up
-- How hard a place is to guess, 0 to 1 (Qiyinlik picks by it; library.Tag fills it).
ALTER TABLE panoramas ADD COLUMN hardness real;

-- +goose Down
ALTER TABLE panoramas DROP COLUMN hardness;
