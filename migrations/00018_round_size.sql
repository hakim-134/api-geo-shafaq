-- +goose Up
-- Mixed maps (P1111 1.3): a round is scored by the size of the part it came from, not of
-- everything mixed. NULL: the game's own size (games.size_km), as every round before this.
ALTER TABLE rounds ADD COLUMN size_km double precision;
ALTER TABLE challenge_rounds ADD COLUMN size_km double precision;

-- +goose Down
ALTER TABLE challenge_rounds DROP COLUMN size_km;
ALTER TABLE rounds DROP COLUMN size_km;
