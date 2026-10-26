-- +goose Up
-- Live rooms (PLAN 5.1). A room's game is one games row per player (mode 'room'), so
-- history, profiles, progress per place and merging work for room games as for solo ones.
-- room_game ties the rows of one room game together, for its table of players.
ALTER TABLE games
    ADD COLUMN room_code text,
    ADD COLUMN room_game uuid;
CREATE INDEX games_room_idx ON games (room_game) WHERE room_game IS NOT NULL;

-- +goose Down
DROP INDEX games_room_idx;
ALTER TABLE games DROP COLUMN room_code, DROP COLUMN room_game;
