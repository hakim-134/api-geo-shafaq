-- +goose Up
-- Places and settings (PLAN 4.1–4.6): endless games end when the player says so, so a game
-- counts the rounds it played; best scores and averages use that, not the rules' count.
ALTER TABLE games ADD COLUMN played int NOT NULL DEFAULT 0;
UPDATE games g SET played = (SELECT count(*) FROM guesses gu
                             WHERE gu.game_id = g.id AND gu.user_id = g.user_id AND gu.guessed_at IS NOT NULL);

-- +goose Down
ALTER TABLE games DROP COLUMN played;
