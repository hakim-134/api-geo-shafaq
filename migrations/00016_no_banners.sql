-- +goose Up
-- A place's face is its picture inside its parent (P1111 1.2), not a photo: the banners go.
ALTER TABLE places DROP COLUMN banner_url, DROP COLUMN banner_credit;

-- +goose Down
ALTER TABLE places ADD COLUMN banner_url text, ADD COLUMN banner_credit text;
