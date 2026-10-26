-- Live rooms: every player's game is a games row of its own (mode 'room'), written when the
-- player's first round in it ends. The room itself lives in the API and in Valkey.

-- name: CreateRoomGame :one
INSERT INTO games (user_id, map_id, mode, rules, size_km, seed, room_code, room_game, started_at)
VALUES (@user_id, @map_id, 'room', @rules, @size_km, @seed, @room_code, @room_game, @started_at)
RETURNING id;

-- name: CreateRoomRound :exec
-- A finished room round in one player's game, shown and with its deadline.
INSERT INTO rounds (game_id, n, panorama_id, provider, near, snap_km, heading, actual,
                    created_at, shown_at, deadline, replaced)
VALUES (@game_id, @n, @panorama_id, @provider,
        ST_SetSRID(ST_MakePoint(@near_lon::float8, @near_lat::float8), 4326), @snap_km, @heading,
        ST_SetSRID(ST_MakePoint(@actual_lon::float8, @actual_lat::float8), 4326),
        @created_at, @shown_at, @deadline, @replaced)
ON CONFLICT (game_id, n) DO NOTHING;

-- name: SaveRoomGuess :execrows
-- 0 rows: this round was saved before (a retry), so the game's total is not added twice.
INSERT INTO guesses (game_id, n, user_id, hints, pos, km, score, late, guessed_at)
VALUES (@game_id, @n, @user_id, @hints,
        CASE WHEN sqlc.narg('lat')::float8 IS NULL THEN NULL
             ELSE ST_SetSRID(ST_MakePoint(sqlc.narg('lon')::float8, sqlc.narg('lat')::float8), 4326) END,
        sqlc.narg('km'), @score, false, @guessed_at)
ON CONFLICT (game_id, n, user_id) DO NOTHING;

-- name: FinishRoomGames :exec
UPDATE games SET status = 'finished', finished_at = now()
WHERE id = ANY(@ids::uuid[]) AND status = 'playing';

-- name: RoomGameTable :many
-- Everyone's game of one room game, best first.
SELECT g.id, g.user_id, g.total, g.played, g.status,
       u.name, u.avatar, u.guest, u.created_at, u.banned_at
FROM games g
JOIN users u ON u.id = g.user_id
WHERE g.room_game = @room_game AND g.status <> 'abandoned'
ORDER BY g.total DESC, g.started_at;
