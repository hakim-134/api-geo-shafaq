-- name: UpsertPanorama :exec
INSERT INTO panoramas (provider, ref, pos, heading, taken_at, street_id, source, status)
VALUES (@provider, @ref, ST_SetSRID(ST_MakePoint(@lon::float8, @lat::float8), 4326),
        sqlc.narg('heading'), sqlc.narg('taken_at'), sqlc.narg('street_id'), @source, 'active')
ON CONFLICT (provider, ref) DO UPDATE SET
    pos = excluded.pos, heading = excluded.heading, taken_at = excluded.taken_at,
    street_id = coalesce(excluded.street_id, panoramas.street_id);

-- name: TagPanoramaPlaces :execrows
-- Every place a panorama is inside, biggest first.
UPDATE panoramas p
SET place_ids = coalesce((SELECT array_agg(pl.id ORDER BY pl.size_km DESC)
                          FROM places pl WHERE ST_Contains(pl.geom, p.pos)), '{}')
WHERE @retag::boolean OR p.place_ids = '{}';

-- name: RetireOutsidePanoramas :execrows
-- Panoramas outside every place (outside the country) never come up.
UPDATE panoramas SET status = 'rejected' WHERE place_ids = '{}' AND status = 'active';

-- name: PanoramaOpened :exec
UPDATE panoramas SET checked_at = now(), fail_count = 0 WHERE id = @id;

-- name: PanoramaFailed :exec
UPDATE panoramas
SET fail_count = fail_count + 1,
    status = CASE WHEN fail_count + 1 >= 3 THEN 'gone' ELSE status END
WHERE id = @id;

-- name: GetPanorama :one
SELECT id, provider, ref, ST_Y(pos)::float8 AS lat, ST_X(pos)::float8 AS lon, heading, status
FROM panoramas WHERE id = @id;

-- name: CountPanoramas :many
SELECT provider, source, status, count(*)::int AS n
FROM panoramas GROUP BY provider, source, status ORDER BY provider, source, status;

-- name: CopyPanoramasAsDemo :execrows
-- The demo provider gets a twin of every Yandex panorama, for development and tests.
INSERT INTO panoramas (provider, ref, pos, heading, street_id, place_ids, source, status)
SELECT 'demo', 'y' || p.id, p.pos, p.heading, p.street_id, p.place_ids, 'demo', 'active'
FROM panoramas p
WHERE p.provider = 'yandex'
ON CONFLICT (provider, ref) DO NOTHING;

-- name: CountProviderView :one
-- Counts a panorama view for the day; returns the day's total.
INSERT INTO provider_usage (day, provider, views)
VALUES (@day, @provider, 1)
ON CONFLICT (day, provider) DO UPDATE SET views = provider_usage.views + 1
RETURNING views;

-- name: UsageToWarn :many
-- Days at or over the warning threshold that nobody was told about yet.
SELECT day, provider, views FROM provider_usage
WHERE provider = @provider AND views >= @threshold AND NOT warned;

-- name: UsageWarned :one
-- Whether the owner already heard about the day.
SELECT EXISTS (SELECT 1 FROM provider_usage WHERE day = @day AND provider = @provider AND warned);

-- name: MarkUsageWarned :exec
-- The row may not exist yet: Yandex's own count can warn before we counted a view that day.
INSERT INTO provider_usage (day, provider, warned)
VALUES (@day, @provider, true)
ON CONFLICT (day, provider) DO UPDATE SET warned = true;

-- name: ScanPoints :many
-- The Yandex library points: where other providers are looked for.
SELECT id, source, street_id, ST_Y(pos)::float8 AS lat, ST_X(pos)::float8 AS lon
FROM panoramas WHERE provider = 'yandex' AND status = 'active' ORDER BY id;
