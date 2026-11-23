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

-- name: MergeChallenges :exec
UPDATE challenges SET created_by = @user_id WHERE created_by = @other_id;

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
       coalesce(max(g.total) FILTER (WHERE (g.rules ->> 'rounds')::int > 0
           AND g.map_id NOT IN (SELECT m.id FROM maps m WHERE m.slug LIKE 'miss-%')), 0)::int AS best,
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
-- Every street the player has been on: how many rounds, the closest guess (-1 when every round
-- there went without a pin), and that round's real place and first heading: the panorama the
-- street's row opens. Closest first. With @place only the rounds inside that place, as its
-- progress counts them (a long street runs through several tumanlar). The street's place is the
-- district the player met it in: the one of that closest round, as its result names it (the
-- smallest place its panorama is in, a mahalla never), not the city the street belongs to.
SELECT m.id, m.name_uz, m.name_ru,
       d.slug AS place_slug, d.name_uz AS place_uz, d.name_ru AS place_ru, d.name_en AS place_en,
       m.rounds, m.best_km, m.at_lat, m.at_lon, m.heading
FROM (
    SELECT s.id, s.name_uz, s.name_ru, s.place_id,
           count(*)::int AS rounds,
           coalesce(min(g.km), -1)::float8 AS best_km,
           -- the closest round (ties: the first played), one order for every column of it
           ((array_agg(p.id ORDER BY g.km NULLS LAST, g.game_id, g.n))[1])::bigint AS panorama_id,
           ((array_agg(ST_Y(coalesce(r.actual, p.pos)) ORDER BY g.km NULLS LAST, g.game_id, g.n))[1])::float8 AS at_lat,
           ((array_agg(ST_X(coalesce(r.actual, p.pos)) ORDER BY g.km NULLS LAST, g.game_id, g.n))[1])::float8 AS at_lon,
           ((array_agg(r.heading ORDER BY g.km NULLS LAST, g.game_id, g.n))[1])::float8 AS heading
    FROM guesses g
    JOIN rounds r ON r.game_id = g.game_id AND r.n = g.n
    JOIN panoramas p ON p.id = r.panorama_id
    JOIN streets s ON s.id = p.street_id
    WHERE g.user_id = @user_id AND g.guessed_at IS NOT NULL
      AND (sqlc.narg('place')::text IS NULL
           OR (SELECT f.id FROM places f WHERE f.slug = sqlc.narg('place')) = ANY (p.place_ids))
    GROUP BY s.id
) m
CROSS JOIN LATERAL (
    -- that panorama's district or city; the street's own place when it has none
    SELECT x.slug, x.name_uz, x.name_ru, x.name_en
    FROM places x
    WHERE x.id = ANY ((SELECT a.place_ids FROM panoramas a WHERE a.id = m.panorama_id) || m.place_id)
    ORDER BY x.kind IN ('district', 'city') DESC, x.size_km
    LIMIT 1
) d
ORDER BY nullif(m.best_km, -1) NULLS LAST, m.name_uz;

-- name: MissedStreets :many
-- The streets the player has been on and not learned, worst first: no pin at all, then the
-- farthest closest guess. A street whose panoramas are all gone can't be played, so it isn't here.
-- Its place is the district of the closest round, as in MyStreets.
SELECT m.id, m.name_uz, m.name_ru,
       d.slug AS place_slug, d.name_uz AS place_uz, d.name_ru AS place_ru, d.name_en AS place_en,
       m.rounds, m.best_km, m.at_lat, m.at_lon, m.heading
FROM (
    SELECT s.id, s.name_uz, s.name_ru, s.place_id,
           count(*)::int AS rounds,
           coalesce(min(g.km), -1)::float8 AS best_km,
           ((array_agg(p.id ORDER BY g.km NULLS LAST, g.game_id, g.n))[1])::bigint AS panorama_id,
           ((array_agg(ST_Y(coalesce(r.actual, p.pos)) ORDER BY g.km NULLS LAST, g.game_id, g.n))[1])::float8 AS at_lat,
           ((array_agg(ST_X(coalesce(r.actual, p.pos)) ORDER BY g.km NULLS LAST, g.game_id, g.n))[1])::float8 AS at_lon,
           ((array_agg(r.heading ORDER BY g.km NULLS LAST, g.game_id, g.n))[1])::float8 AS heading
    FROM guesses g
    JOIN rounds r ON r.game_id = g.game_id AND r.n = g.n
    JOIN panoramas p ON p.id = r.panorama_id
    JOIN streets s ON s.id = p.street_id
    WHERE g.user_id = @user_id AND g.guessed_at IS NOT NULL
      AND EXISTS (SELECT 1 FROM panoramas a
                  WHERE a.street_id = s.id AND a.status = 'active' AND a.provider = ANY (@providers::text[]))
    GROUP BY s.id
    HAVING coalesce(min(g.km), 'Infinity'::float8) > @learned_km::float8
) m
CROSS JOIN LATERAL (
    SELECT x.slug, x.name_uz, x.name_ru, x.name_en
    FROM places x
    WHERE x.id = ANY ((SELECT a.place_ids FROM panoramas a WHERE a.id = m.panorama_id) || m.place_id)
    ORDER BY x.kind IN ('district', 'city') DESC, x.size_km
    LIMIT 1
) d
ORDER BY nullif(m.best_km, -1) DESC NULLS FIRST, m.name_uz;

-- name: MapBests :many
-- The player's best game on each map (best per round, so 3-, 10- and endless games compare).
SELECT DISTINCT ON (m.id) m.slug, m.name_uz, m.name_ru, m.name_en,
       (count(*) OVER (PARTITION BY m.id))::int AS games,
       g.id AS game_id, g.total, g.played AS rounds
FROM games g
JOIN maps m ON m.id = g.map_id
WHERE g.user_id = @user_id AND g.status = 'finished' AND g.played > 0 AND m.slug NOT LIKE 'miss-%'
ORDER BY m.id, g.total::float8 / g.played DESC;
