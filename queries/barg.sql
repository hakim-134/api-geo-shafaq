-- name: EarnBarg :execrows
-- One entry, once: the key can't repeat, and after a merge the same reason and ref can't either.
INSERT INTO barg_ledger (user_id, amount, reason, ref, note, key)
SELECT @user_id, @amount, @reason, @ref, @note, @key
WHERE NOT EXISTS (SELECT 1 FROM barg_ledger l WHERE l.user_id = @user_id AND l.reason = @reason AND l.ref = @ref)
ON CONFLICT (key) DO NOTHING;

-- name: BargBalance :one
SELECT coalesce(sum(amount), 0)::int FROM barg_ledger WHERE user_id = @user_id;

-- name: BargHistory :many
SELECT amount, reason, ref, note, created_at
FROM barg_ledger WHERE user_id = @user_id
ORDER BY id DESC LIMIT 100;

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
