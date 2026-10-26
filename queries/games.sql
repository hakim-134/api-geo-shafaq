-- name: CreateGame :one
INSERT INTO games (user_id, map_id, mode, rules, size_km, seed)
VALUES (@user_id, @map_id, 'solo', @rules, @size_km, @seed)
RETURNING *;

-- name: GetGame :one
SELECT * FROM games WHERE id = @id;

-- name: LockGame :one
SELECT * FROM games WHERE id = @id FOR UPDATE;

-- name: FinishGame :exec
UPDATE games SET status = 'finished', finished_at = now() WHERE id = @id;

-- name: AddToGame :exec
-- A guessed round: its score, and one more round played.
UPDATE games SET total = total + @score, hints_used = hints_used + @hints, played = played + 1 WHERE id = @id;

-- name: AbandonGame :exec
UPDATE games SET status = 'abandoned', finished_at = now() WHERE id = @id;

-- name: AddHintToGame :exec
UPDATE games SET hints_used = hints_used + 1 WHERE id = @id;

-- name: AbandonStaleGames :execrows
UPDATE games SET status = 'abandoned'
WHERE status = 'playing' AND started_at < now() - make_interval(hours => @older_than_hours::int);

-- name: ListUserGames :many
-- Newest first; @before is the started_at of the last game on the previous page.
SELECT g.id, g.status, g.started_at, g.finished_at, g.total, g.rules, g.played,
       m.slug AS map_slug, m.name_uz, m.name_ru, m.name_en,
       (SELECT count(*) FROM games x WHERE x.room_game = g.room_game)::int AS players
FROM games g
JOIN maps m ON m.id = g.map_id
WHERE g.user_id = @user_id AND g.status = 'finished'
  AND (sqlc.narg('before')::timestamptz IS NULL OR g.started_at < sqlc.narg('before'))
ORDER BY g.started_at DESC
LIMIT @lim;

-- name: CreateRound :exec
INSERT INTO rounds (game_id, n, panorama_id, provider, near, snap_km, heading, created_at)
VALUES (@game_id, @n, @panorama_id, @provider,
        ST_SetSRID(ST_MakePoint(@near_lon::float8, @near_lat::float8), 4326), @snap_km, @heading, @created_at);

-- name: ReplaceRoundPanorama :exec
UPDATE rounds
SET panorama_id = @panorama_id, provider = @provider,
    near = ST_SetSRID(ST_MakePoint(@near_lon::float8, @near_lat::float8), 4326),
    snap_km = @snap_km, heading = @heading, replaced = replaced + 1, created_at = @created_at
WHERE game_id = @game_id AND n = @n;

-- name: ShowRound :exec
UPDATE rounds
SET shown_at = @shown_at, deadline = @deadline,
    actual = ST_SetSRID(ST_MakePoint(@actual_lon::float8, @actual_lat::float8), 4326)
WHERE game_id = @game_id AND n = @n;

-- name: GameRounds :many
-- Every round of a game with the player's guess, oldest first.
SELECT r.n, r.panorama_id, r.provider, r.snap_km, r.heading, r.replaced,
       ST_Y(r.near)::float8 AS near_lat, ST_X(r.near)::float8 AS near_lon,
       ST_Y(coalesce(r.actual, p.pos))::float8 AS actual_lat,
       ST_X(coalesce(r.actual, p.pos))::float8 AS actual_lon,
       p.ref AS pano_ref,
       r.created_at, r.shown_at, r.deadline,
       g.hints, g.guessed_at, g.km, g.score, g.late,
       (g.pos IS NOT NULL)::boolean AS has_guess,
       coalesce(ST_Y(g.pos), 0)::float8 AS guess_lat, coalesce(ST_X(g.pos), 0)::float8 AS guess_lon
FROM rounds r
JOIN panoramas p ON p.id = r.panorama_id
LEFT JOIN guesses g ON g.game_id = r.game_id AND g.n = r.n AND g.user_id = @user_id
WHERE r.game_id = @game_id
ORDER BY r.n;

-- name: UpsertHints :exec
INSERT INTO guesses (game_id, n, user_id, hints)
VALUES (@game_id, @n, @user_id, @hints)
ON CONFLICT (game_id, n, user_id) DO UPDATE SET hints = excluded.hints;

-- name: SaveGuess :exec
INSERT INTO guesses (game_id, n, user_id, pos, km, score, late, guessed_at)
VALUES (@game_id, @n, @user_id,
        CASE WHEN sqlc.narg('lat')::float8 IS NULL THEN NULL
             ELSE ST_SetSRID(ST_MakePoint(sqlc.narg('lon')::float8, sqlc.narg('lat')::float8), 4326) END,
        sqlc.narg('km'), @score, @late, now())
ON CONFLICT (game_id, n, user_id) DO UPDATE SET
    pos = excluded.pos, km = excluded.km, score = excluded.score, late = excluded.late,
    guessed_at = excluded.guessed_at;
