-- name: CreateUser :one
INSERT INTO users (name, avatar, guest)
VALUES (@name, @avatar, true)
RETURNING *;

-- name: GetUser :one
SELECT * FROM users WHERE id = @id;

-- name: CreateSession :exec
INSERT INTO sessions (token_hash, user_id, user_agent, expires_at)
VALUES (@token_hash, @user_id, @user_agent, @expires_at);

-- name: SessionUser :one
-- The user of a live session.
SELECT u.*, s.last_used_at AS session_used_at
FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.token_hash = @token_hash AND s.expires_at > now();

-- name: TouchSession :exec
-- Slides the expiry; called at most once an hour per session.
UPDATE sessions SET last_used_at = now(), expires_at = @expires_at WHERE token_hash = @token_hash;

-- name: TouchUser :exec
UPDATE users SET last_seen_at = now() WHERE id = @id;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at < now();

-- name: UserStats :one
SELECT count(*)::int                                     AS games,
       coalesce(max(g.total), 0)::int                     AS best,
       coalesce(sum(g.total), 0)::bigint                  AS score_sum,
       coalesce(sum((g.rules ->> 'rounds')::int), 0)::bigint AS rounds_sum
FROM games g
WHERE g.user_id = @user_id AND g.status = 'finished';
