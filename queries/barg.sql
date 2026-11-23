-- name: EarnBarg :execrows
-- One entry, once: the key can't repeat, and after a merge the same reason and ref can't either.
INSERT INTO barg_ledger (user_id, amount, reason, ref, note, key)
SELECT @user_id, @amount, @reason, @ref, @note, @key
WHERE NOT EXISTS (SELECT 1 FROM barg_ledger l WHERE l.user_id = @user_id AND l.reason = @reason AND l.ref = @ref)
ON CONFLICT (key) DO NOTHING;

-- name: BargBalance :one
SELECT coalesce(sum(amount), 0)::int FROM barg_ledger WHERE user_id = @user_id;

-- name: BargHistory :many
-- The newest hundred entries, each with its place: a district's own; a street's district, the
-- one the player met it in as in their streets (their closest round there), or, with no round of
-- theirs left there, the one holding most of the street's panoramas.
WITH near AS MATERIALIZED (
    -- the panorama places of the player's closest round on every street they have been on: one
    -- pass over their rounds, however many entries ask
    SELECT DISTINCT ON (p.street_id) p.street_id, p.place_ids
    FROM guesses g
    JOIN rounds r ON r.game_id = g.game_id AND r.n = g.n
    JOIN panoramas p ON p.id = r.panorama_id
    WHERE g.user_id = @user_id AND g.guessed_at IS NOT NULL AND p.street_id IS NOT NULL
    ORDER BY p.street_id, g.km NULLS LAST, g.game_id, g.n
)
SELECT l.amount, l.reason, l.ref, l.note, l.created_at,
       coalesce(d.slug, '')::text AS place_slug, coalesce(d.name_uz, '')::text AS place_uz,
       coalesce(d.name_ru, '')::text AS place_ru, coalesce(d.name_en, '')::text AS place_en
FROM (SELECT * FROM barg_ledger b WHERE b.user_id = @user_id ORDER BY b.id DESC LIMIT 100) l
LEFT JOIN LATERAL (
    SELECT x.slug, x.name_uz, x.name_ru, x.name_en
    FROM places x
    WHERE l.reason = 'district' AND x.slug = l.ref
    UNION ALL
    (SELECT x.slug, x.name_uz, x.name_ru, x.name_en
     FROM streets s
     JOIN places x ON x.id = ANY (coalesce(
         (SELECT n.place_ids FROM near n WHERE n.street_id = s.id),
         (SELECT a.place_ids FROM panoramas a
          WHERE a.street_id = s.id AND a.place_ids <> '{}'
          GROUP BY a.place_ids ORDER BY count(*) DESC LIMIT 1)
     ) || s.place_id)
     -- a street's ref is its id; the CASE keeps the cast off every other entry's ref
     WHERE s.id = CASE WHEN l.reason = 'street' THEN l.ref::bigint END
     ORDER BY x.kind IN ('district', 'city') DESC, x.size_km
     LIMIT 1)
) d ON true
ORDER BY l.id DESC;

-- name: BargEarnedSince :one
-- How many earnings of this reason since a moment: the daily cap on challenge wins.
SELECT count(*)::int FROM barg_ledger
WHERE user_id = @user_id AND reason = @reason AND created_at >= @since;

-- name: PanoramaForBarg :one
SELECT p.place_ids, coalesce(p.street_id, 0)::bigint AS street_id, coalesce(s.name_uz, '')::text AS street
FROM panoramas p LEFT JOIN streets s ON s.id = p.street_id
WHERE p.id = @id;

-- name: LockUser :one
-- A purchase holds the player's row: two taps can't both spend the same Barg.
SELECT id FROM users WHERE id = @id FOR UPDATE;

-- name: OwnedItems :many
SELECT item FROM shop_owned WHERE user_id = @user_id;

-- name: AddOwned :exec
INSERT INTO shop_owned (user_id, item) VALUES (@user_id, @item) ON CONFLICT DO NOTHING;

-- name: WearItem :one
-- What the player wears, one for each kind: {"frame": "zar"}.
UPDATE users SET prefs = prefs || @prefs::jsonb WHERE id = @id RETURNING *;

-- name: TakeOffItem :one
UPDATE users SET prefs = prefs - @key::text WHERE id = @id RETURNING *;

-- name: MergeBarg :exec
UPDATE barg_ledger SET user_id = @keep_id WHERE user_id = @other_id;

-- name: MergeOwned :exec
INSERT INTO shop_owned (user_id, item, created_at)
SELECT @keep_id, o.item, o.created_at FROM shop_owned o WHERE o.user_id = @other_id
ON CONFLICT DO NOTHING;
