-- name: ChallengeBySource :one
SELECT * FROM challenges WHERE source = @source;

-- name: GetChallenge :one
SELECT * FROM challenges WHERE code = @code;

-- name: LockChallenge :one
SELECT * FROM challenges WHERE code = @code FOR UPDATE;

-- name: InsertChallenge :one
-- No row back: the code or the source is taken. The caller looks again.
INSERT INTO challenges (code, map_id, rules, size_km, seed, source, created_by)
VALUES (@code, @map_id, @rules, @size_km, @seed, @source, @created_by)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: CopyChallengeRounds :execrows
-- The rounds that were played in the source game (a room: in any of its players' rows), renumbered.
INSERT INTO challenge_rounds (challenge, n, panorama_id, provider, near, snap_km, heading)
SELECT @code, (row_number() OVER (ORDER BY x.n))::int, x.panorama_id, x.provider, x.near, x.snap_km, x.heading
FROM (
    SELECT DISTINCT ON (r.n) r.n, r.panorama_id, r.provider, r.near, r.snap_km, r.heading
    FROM rounds r
    JOIN games g ON g.id = r.game_id
    JOIN guesses gu ON gu.game_id = r.game_id AND gu.n = r.n AND gu.guessed_at IS NOT NULL
    WHERE g.id = @source OR g.room_game = @source
    ORDER BY r.n
) x;

-- name: SetChallengeRules :exec
UPDATE challenges SET rules = @rules WHERE code = @code;

-- name: LinkChallengeGames :exec
-- The source game's finished rows stand on the leaderboard from the start.
UPDATE games SET challenge = @code
WHERE (id = @source OR room_game = @source) AND status = 'finished' AND challenge IS NULL;

-- name: ChallengeRounds :many
SELECT cr.n, cr.panorama_id, cr.provider, cr.snap_km, cr.heading, p.ref,
       ST_Y(cr.near)::float8 AS near_lat, ST_X(cr.near)::float8 AS near_lon
FROM challenge_rounds cr
JOIN panoramas p ON p.id = cr.panorama_id
WHERE cr.challenge = @code
ORDER BY cr.n;

-- name: CreateChallengeGame :one
INSERT INTO games (user_id, map_id, mode, rules, size_km, seed, challenge)
VALUES (@user_id, @map_id, 'challenge', @rules, @size_km, @seed, @challenge)
RETURNING *;

-- name: PlayerChallengeGame :one
-- The player's game of a challenge: the finished one first (after a merge there can be two).
SELECT * FROM games
WHERE challenge = @challenge AND user_id = @user_id AND status <> 'abandoned'
ORDER BY status = 'finished' DESC, total DESC, started_at
LIMIT 1;

-- name: ChallengeBoard :many
-- Everyone who finished it, once each, best first; the earlier of equal scores above.
SELECT sqlc.embed(u), b.id AS game_id, b.total, b.hints_used, b.rules
FROM (
    SELECT DISTINCT ON (g.user_id) g.*
    FROM games g
    WHERE g.challenge = @challenge AND g.status = 'finished'
    ORDER BY g.user_id, g.total DESC, g.finished_at
) b
JOIN users u ON u.id = b.user_id
WHERE u.banned_at IS NULL
ORDER BY b.total DESC, b.finished_at, b.id;

-- name: ChallengeCrowd :one
-- How far the challenge's players guessed from this round's panorama, this player included, as a
-- room's ring does: guessed and not late.
SELECT coalesce(avg(g.km), 0)::float8 AS km, count(DISTINCT g.user_id)::int AS players
FROM rounds me
JOIN rounds r ON r.panorama_id = me.panorama_id
JOIN games x ON x.id = r.game_id AND x.challenge = @challenge
JOIN guesses g ON g.game_id = r.game_id AND g.n = r.n
WHERE me.game_id = @game_id AND me.n = @n
  AND g.guessed_at IS NOT NULL AND g.km IS NOT NULL AND NOT g.late;

-- name: ChallengeCreatorRounds :many
-- The challenger's scores, round by round: the squares on the card.
SELECT coalesce(gu.score, 0)::int AS score
FROM games g
JOIN guesses gu ON gu.game_id = g.id AND gu.user_id = g.user_id AND gu.guessed_at IS NOT NULL
WHERE g.challenge = @challenge AND g.user_id = @user_id AND g.status = 'finished'
ORDER BY g.total DESC, gu.n;
