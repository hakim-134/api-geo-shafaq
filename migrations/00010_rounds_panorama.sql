-- +goose Up
-- The crowd circle (P1028 3.3): other players' guesses on the same panorama.
CREATE INDEX rounds_panorama_idx ON rounds (panorama_id);

-- +goose Down
DROP INDEX rounds_panorama_idx;
