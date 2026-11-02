-- +goose Up
-- Challenge links (PLAN 8.3): a map, its rules and fixed panoramas; everyone with the link plays
-- the same streets, each in their own games row, and lands on one leaderboard. source is the game
-- it was made from (a room's: the room game), so asking twice gives the same link.
CREATE TABLE challenges (
    code       text   PRIMARY KEY,
    map_id     bigint NOT NULL REFERENCES maps (id),
    rules      jsonb  NOT NULL,
    size_km    double precision NOT NULL,
    seed       bigint NOT NULL,
    source     uuid   NOT NULL UNIQUE,
    created_by uuid   NOT NULL REFERENCES users (id),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE challenge_rounds (
    challenge   text   NOT NULL REFERENCES challenges (code) ON DELETE CASCADE,
    n           int    NOT NULL,
    panorama_id bigint NOT NULL REFERENCES panoramas (id),
    provider    text   NOT NULL,
    near        geometry(Point, 4326) NOT NULL,
    snap_km     double precision NOT NULL,
    heading     real   NOT NULL DEFAULT 0,
    PRIMARY KEY (challenge, n)
);

ALTER TABLE games ADD COLUMN challenge text REFERENCES challenges (code);
CREATE INDEX games_challenge_idx ON games (challenge, user_id) WHERE challenge IS NOT NULL;

-- +goose Down
DROP INDEX games_challenge_idx;
ALTER TABLE games DROP COLUMN challenge;
DROP TABLE challenge_rounds;
DROP TABLE challenges;
