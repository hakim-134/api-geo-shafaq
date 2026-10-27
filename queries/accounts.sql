-- name: IdentityOwner :one
SELECT * FROM identities WHERE provider = @provider AND subject = @subject;

-- name: UserIdentities :many
SELECT * FROM identities WHERE user_id = @user_id ORDER BY created_at;

-- name: AddIdentity :exec
INSERT INTO identities (provider, subject, user_id, name, username)
VALUES (@provider, @subject, @user_id, @name, @username);

-- name: TouchIdentity :exec
-- Each sign-in refreshes the name the provider shows, so the profile stays recognisable.
UPDATE identities SET name = @name, username = @username, last_used_at = now()
WHERE provider = @provider AND subject = @subject;

-- name: DeleteIdentity :execrows
-- Never the last one: an account without a way in would be lost.
DELETE FROM identities i
WHERE i.provider = @provider AND i.subject = @subject AND i.user_id = @user_id
  AND (SELECT count(*) FROM identities WHERE user_id = @user_id) > 1;

-- name: SetAccount :exec
UPDATE users SET guest = false WHERE id = @id;

-- name: SetProfile :one
UPDATE users SET name = @name, avatar = @avatar WHERE id = @id RETURNING *;

-- name: SetPrefs :one
-- Merges the given preferences into the player's.
UPDATE users SET prefs = prefs || @prefs::jsonb WHERE id = @id RETURNING *;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE token_hash = @token_hash;

-- name: DeleteUserSessions :execrows
DELETE FROM sessions WHERE user_id = @user_id;

-- name: CreateTelegramLogin :exec
INSERT INTO telegram_logins (code_hash, user_id, expires_at) VALUES (@code_hash, @user_id, @expires_at);

-- name: ConfirmTelegramLogin :execrows
-- The bot's side: who pressed Start. Once, and only while the code is fresh.
UPDATE telegram_logins
SET tg_id = @tg_id, tg_name = @tg_name, tg_username = @tg_username, confirmed_at = now()
WHERE code_hash = @code_hash AND confirmed_at IS NULL AND expires_at > now();

-- name: TakeTelegramLogin :one
-- The game's side: a confirmed code is used once, by the player who asked for it.
DELETE FROM telegram_logins
WHERE code_hash = @code_hash AND user_id = @user_id AND confirmed_at IS NOT NULL
RETURNING *;

-- name: TelegramLoginWaiting :one
SELECT (count(*) > 0)::boolean FROM telegram_logins
WHERE code_hash = @code_hash AND user_id = @user_id AND expires_at > now();

-- name: CreateMergeOffer :exec
INSERT INTO merge_offers (token_hash, user_id, other_id, expires_at)
VALUES (@token_hash, @user_id, @other_id, @expires_at);

-- name: TakeMergeOffer :one
DELETE FROM merge_offers
WHERE token_hash = @token_hash AND user_id = @user_id AND expires_at > now()
RETURNING *;

-- name: DeleteExpiredAuth :execrows
WITH a AS (DELETE FROM telegram_logins WHERE expires_at < now() - interval '1 hour' RETURNING 1),
     b AS (DELETE FROM merge_offers WHERE expires_at < now() RETURNING 1)
SELECT (SELECT count(*) FROM a) + (SELECT count(*) FROM b);

-- Merging moves everything of @other_id to @user_id; the caller deletes @other_id after.

-- name: MergeGames :exec
UPDATE games SET user_id = @user_id WHERE user_id = @other_id;

-- name: MergeGuesses :exec
UPDATE guesses SET user_id = @user_id WHERE user_id = @other_id;

-- name: MergeIdentities :exec
UPDATE identities SET user_id = @user_id WHERE user_id = @other_id;

-- name: MergeSessions :exec
-- The other account's phones and laptops stay signed in, now to this account.
UPDATE sessions SET user_id = @user_id WHERE user_id = @other_id;

-- name: MergeMaps :exec
UPDATE maps SET owner_id = @user_id WHERE owner_id = @other_id;

-- name: MergeBan :exec
-- A ban follows the person into the merged account.
UPDATE users u SET banned_at = o.banned_at, ban_reason = o.ban_reason
FROM users o
WHERE u.id = @user_id AND o.id = @other_id AND o.banned_at IS NOT NULL AND u.banned_at IS NULL;

-- name: DeleteUser :exec
DELETE FROM users WHERE id = @id;

-- name: CreateReport :exec
INSERT INTO reports (reporter_id, user_id, reason, name)
VALUES (@reporter_id, @user_id, @reason, @name)
ON CONFLICT (reporter_id, user_id, reason) DO NOTHING;

-- name: OpenReports :many
-- Newest first, one line per reported player and reason, with how many reported it.
SELECT r.user_id, r.reason, max(r.name)::text AS name, count(*)::int AS reporters,
       max(r.created_at)::timestamptz AS last_at, u.name AS current_name, u.banned_at
FROM reports r
JOIN users u ON u.id = r.user_id
WHERE r.resolved_at IS NULL
GROUP BY r.user_id, r.reason, u.name, u.banned_at
ORDER BY last_at DESC
LIMIT 100;

-- name: ResolveReports :execrows
UPDATE reports SET resolved_at = now() WHERE user_id = @user_id AND resolved_at IS NULL;

-- name: BanUser :execrows
UPDATE users SET banned_at = now(), ban_reason = @reason WHERE id = @id;

-- name: UnbanUser :execrows
UPDATE users SET banned_at = NULL, ban_reason = '' WHERE id = @id;

-- name: FindUsers :many
-- For the admin tools: by id prefix or by name.
SELECT * FROM users
WHERE id::text LIKE @q::text || '%' OR name = @q::text
ORDER BY last_seen_at DESC
LIMIT 20;

-- name: ProfileStats :one
-- Everything on the profile card, from finished games.
SELECT count(DISTINCT g.id)::int                              AS games,
       coalesce(max(g.total) FILTER (WHERE (g.rules ->> 'rounds')::int > 0), 0)::int AS best,
       coalesce(sum(gu.score), 0)::bigint                      AS score_sum,
       count(gu.*)::int                                        AS rounds,
       count(gu.*) FILTER (WHERE gu.score >= 4900)::int        AS perfect,
       coalesce(sum(gu.km), 0)::float8                         AS km_sum
FROM games g
LEFT JOIN guesses gu ON gu.game_id = g.id AND gu.user_id = g.user_id AND gu.guessed_at IS NOT NULL
WHERE g.user_id = @user_id AND g.status = 'finished';

-- name: StreetProgress :one
-- Streets been on, and streets learned: guessed within @learned_km at least once.
SELECT count(DISTINCT p.street_id)::int AS seen,
       count(DISTINCT p.street_id) FILTER (WHERE g.km <= @learned_km::float8)::int AS learned
FROM guesses g
JOIN rounds r ON r.game_id = g.game_id AND r.n = g.n
JOIN panoramas p ON p.id = r.panorama_id
WHERE g.user_id = @user_id AND g.guessed_at IS NOT NULL AND p.street_id IS NOT NULL;

-- name: MyStreets :many
-- Every street the player has been on: its district or city, how many rounds, and the closest
-- guess (-1 when every round there went without a pin). Closest first.
SELECT s.id, s.name_uz, s.name_ru,
       pl.name_uz AS place_uz, pl.name_ru AS place_ru, pl.name_en AS place_en,
       count(*)::int AS rounds,
       coalesce(min(g.km), -1)::float8 AS best_km
FROM guesses g
JOIN rounds r ON r.game_id = g.game_id AND r.n = g.n
JOIN panoramas p ON p.id = r.panorama_id
JOIN streets s ON s.id = p.street_id
JOIN places pl ON pl.id = s.place_id
WHERE g.user_id = @user_id AND g.guessed_at IS NOT NULL
GROUP BY s.id, pl.id
ORDER BY min(g.km) NULLS LAST, s.name_uz;

-- name: MapBests :many
-- The player's best game on each map (best per round, so 3-, 10- and endless games compare).
SELECT DISTINCT ON (m.id) m.slug, m.name_uz, m.name_ru, m.name_en,
       (count(*) OVER (PARTITION BY m.id))::int AS games,
       g.id AS game_id, g.total, g.played AS rounds
FROM games g
JOIN maps m ON m.id = g.map_id
WHERE g.user_id = @user_id AND g.status = 'finished' AND g.played > 0
ORDER BY m.id, g.total::float8 / g.played DESC;
