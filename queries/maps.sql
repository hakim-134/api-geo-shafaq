-- name: ListOfficialMaps :many
SELECT * FROM maps WHERE owner_id IS NULL AND visibility = 'public' ORDER BY sort, id;

-- name: GetMapBySlug :one
SELECT * FROM maps WHERE slug = @slug;

-- name: GetMap :one
SELECT * FROM maps WHERE id = @id;

-- name: UpsertOfficialMap :one
INSERT INTO maps (slug, name_uz, name_ru, name_en, desc_uz, desc_ru, desc_en, visibility, rules, pick, sort)
VALUES (@slug, @name_uz, @name_ru, @name_en, @desc_uz, @desc_ru, @desc_en, @visibility, @rules, @pick, @sort)
ON CONFLICT (slug) DO UPDATE SET
    name_uz = excluded.name_uz, name_ru = excluded.name_ru, name_en = excluded.name_en,
    desc_uz = excluded.desc_uz, desc_ru = excluded.desc_ru, desc_en = excluded.desc_en,
    visibility = excluded.visibility,
    rules = excluded.rules, pick = excluded.pick, sort = excluded.sort, updated_at = now()
RETURNING id;

-- name: UpsertPlaceMap :one
-- A place's own map, made the first time someone picks the place. Unlisted: the menu shows
-- the official maps; places are reached through the place picker.
INSERT INTO maps (slug, name_uz, name_ru, name_en, desc_uz, desc_ru, desc_en, visibility, rules, pick, sort)
VALUES (@slug, @name_uz, @name_ru, @name_en, @desc_uz, @desc_ru, @desc_en, 'unlisted', @rules, @pick, 1000)
ON CONFLICT (slug) DO UPDATE SET
    name_uz = excluded.name_uz, name_ru = excluded.name_ru, name_en = excluded.name_en,
    desc_uz = excluded.desc_uz, desc_ru = excluded.desc_ru, desc_en = excluded.desc_en,
    pick = excluded.pick, updated_at = now()
RETURNING id;

-- name: ClearMapSources :exec
DELETE FROM map_sources WHERE map_id = @map_id;

-- name: AddMapPlace :exec
INSERT INTO map_sources (map_id, place_id)
SELECT @map_id, id FROM places WHERE slug = @place_slug;

-- name: MapCandidates :many
-- Every active panorama of a map, with the keys the picker groups by. A panorama is in the
-- map if it is inside one of the map's places or its drawn area, on one of its streets, or picked
-- by hand.
SELECT p.id,
       p.provider,
       p.ref,
       ST_Y(p.pos)::float8 AS lat,
       ST_X(p.pos)::float8 AS lon,
       p.heading,
       p.street_id,
       coalesce(p.hardness, 0.5)::float8 AS hardness,
       coalesce((SELECT pl.id FROM places pl
                  WHERE pl.id = ANY (p.place_ids) AND pl.kind IN ('district', 'city')
                  ORDER BY pl.size_km LIMIT 1), 0)::bigint AS district_id,
       coalesce((SELECT pl.id FROM places pl
                  WHERE pl.id = ANY (p.place_ids) AND pl.kind IN ('region', 'city') AND pl.parent_id IN
                        (SELECT c.id FROM places c WHERE c.kind = 'country')
                  LIMIT 1), 0)::bigint AS region_id
FROM panoramas p
WHERE p.status = 'active'
  AND (@streets_only::boolean = false OR p.street_id IS NOT NULL)
  AND EXISTS (SELECT 1 FROM map_sources s
              WHERE s.map_id = @map_id
                AND (s.place_id = ANY (p.place_ids) OR s.street_id = p.street_id OR s.panorama_id = p.id
                     OR ST_Covers(s.area, p.pos)));

-- name: MapPlaceExtent :one
-- The bounding box of a map's places and drawn areas: its size D for scoring.
SELECT coalesce(ST_XMin(e), 0)::float8 AS west, coalesce(ST_YMin(e), 0)::float8 AS south,
       coalesce(ST_XMax(e), 0)::float8 AS east, coalesce(ST_YMax(e), 0)::float8 AS north
FROM (SELECT ST_Extent(u.g)::geometry AS e
      FROM (SELECT pl.geom AS g FROM map_sources s JOIN places pl ON pl.id = s.place_id WHERE s.map_id = @map_id
            UNION ALL
            SELECT s.area FROM map_sources s WHERE s.map_id = @map_id AND s.area IS NOT NULL) u) x;

-- name: MapPlaces :many
-- A map's places with their outlines, largest first.
SELECT pl.id, pl.outline_svg
FROM map_sources s JOIN places pl ON pl.id = s.place_id
WHERE s.map_id = @map_id
ORDER BY pl.size_km DESC;

-- ---------- players' maps ----------

-- name: CreatePlayerMap :one
INSERT INTO maps (slug, owner_id, name_uz, visibility, moderation, tags, rules, pick, sort)
VALUES (@slug, @owner_id, @name_uz, @visibility, @moderation, @tags, @rules, @pick, 2000)
RETURNING id;

-- name: AddMapStreet :exec
INSERT INTO map_sources (map_id, street_id) VALUES (@map_id, @street_id);

-- name: AddMapArea :exec
-- A finger's loop may cross itself: made valid, only its surfaces kept.
INSERT INTO map_sources (map_id, area)
VALUES (@map_id, ST_Multi(ST_CollectionExtract(ST_MakeValid(ST_SetSRID(ST_GeomFromGeoJSON(@area::text), 4326)), 3)));

-- name: PreviewSources :one
-- What a map made of these would hold, on the players' providers. @area is GeoJSON or ''.
SELECT count(*)::int AS panoramas, count(DISTINCT p.street_id)::int AS streets
FROM panoramas p
WHERE p.status = 'active' AND p.provider = ANY (@providers::text[])
  AND (p.place_ids && coalesce((SELECT array_agg(pl.id) FROM places pl WHERE pl.slug = ANY (@places::text[])), '{}')
       OR p.street_id = ANY (@streets::bigint[])
       OR (@area::text <> '' AND ST_Covers(
             ST_MakeValid(ST_SetSRID(ST_GeomFromGeoJSON(nullif(@area::text, '')), 4326)), p.pos)));

-- name: CountPlayerMaps :one
SELECT count(*)::int FROM maps
WHERE owner_id = @owner_id AND deleted_at IS NULL AND slug NOT LIKE 'miss-%';

-- name: MyMaps :many
SELECT m.*,
       (SELECT count(*) FROM games g WHERE g.map_id = m.id)::int AS plays,
       (SELECT count(*) FROM map_likes l WHERE l.map_id = m.id)::int AS likes
FROM maps m
WHERE m.owner_id = @owner_id AND m.deleted_at IS NULL AND m.slug NOT LIKE 'miss-%'
ORDER BY m.created_at DESC
LIMIT 100;

-- name: SearchMaps :many
-- The listed maps: official ones, and players' public ones a person has approved. Most played
-- first. Names compare with every apostrophe as the game writes it, o‘.
SELECT m.*,
       (SELECT count(*) FROM games g WHERE g.map_id = m.id)::int AS plays,
       (SELECT count(*) FROM map_likes l WHERE l.map_id = m.id)::int AS likes,
       count(*) OVER ()::int AS total
FROM maps m
WHERE m.deleted_at IS NULL AND m.visibility = 'public'
  AND (m.owner_id IS NULL OR m.moderation = 'approved')
  AND (@q::text = '' OR translate(m.name_uz, '''’ʻʼ`', '‘‘‘‘‘') ILIKE '%' || @q::text || '%'
       OR m.name_ru ILIKE '%' || @q::text || '%' OR m.name_en ILIKE '%' || @q::text || '%')
  AND (@tag::text = '' OR @tag::text = ANY (m.tags))
ORDER BY plays DESC, likes DESC, m.sort, m.created_at DESC
LIMIT @lim OFFSET @off;

-- name: MapStats :one
SELECT (SELECT count(*) FROM games g WHERE g.map_id = @map_id)::int AS plays,
       (SELECT count(*) FROM map_likes l WHERE l.map_id = @map_id)::int AS likes,
       EXISTS (SELECT 1 FROM map_likes l WHERE l.map_id = @map_id AND l.user_id = @user_id) AS liked;

-- name: MapBoard :many
-- The map's best players, once each, by points per round: a 3-round game and a 10-round game
-- compare. Games shorter than @min_rounds don't count: one lucky round is not a record.
SELECT sqlc.embed(u), b.id AS game_id, b.total, b.played, (b.total::float8 / b.played)::float8 AS per_round
FROM (
    SELECT DISTINCT ON (g.user_id) g.*
    FROM games g
    WHERE g.map_id = @map_id AND g.status = 'finished' AND g.played >= @min_rounds::int AND g.played > 0
    ORDER BY g.user_id, g.total::float8 / g.played DESC, g.finished_at
) b
JOIN users u ON u.id = b.user_id
WHERE u.banned_at IS NULL
ORDER BY per_round DESC, b.finished_at
LIMIT 10;

-- name: MapAreaShape :one
-- The map's drawn area as GeoJSON, '' when it has none: the map page's small picture.
SELECT coalesce(ST_AsGeoJSON(ST_Union(s.area), 5), '')::text FROM map_sources s WHERE s.map_id = @map_id;

-- name: MapStreetPoints :many
-- Where a map's picked streets run, as their panoramas: the map page's small picture.
SELECT ST_Y(p.pos)::float8 AS lat, ST_X(p.pos)::float8 AS lon
FROM map_sources s
JOIN panoramas p ON p.street_id = s.street_id
WHERE s.map_id = @map_id AND p.status = 'active' AND p.provider = ANY (@providers::text[])
LIMIT 400;

-- name: LikeMap :exec
INSERT INTO map_likes (map_id, user_id) VALUES (@map_id, @user_id) ON CONFLICT DO NOTHING;

-- name: UnlikeMap :exec
DELETE FROM map_likes WHERE map_id = @map_id AND user_id = @user_id;

-- name: UpdatePlayerMap :exec
UPDATE maps SET name_uz = @name_uz, name_ru = '', name_en = '', visibility = @visibility,
    moderation = @moderation, moderation_note = @moderation_note, tags = @tags, updated_at = now()
WHERE id = @id;

-- name: DeletePlayerMap :exec
UPDATE maps SET deleted_at = now(), updated_at = now() WHERE id = @id;

-- name: ModerationQueue :many
-- Oldest first, with who made it and how much it holds.
SELECT m.slug, m.name_uz, m.tags, m.updated_at, u.name AS owner_name,
       (SELECT count(*) FROM map_sources s WHERE s.map_id = m.id)::int AS sources
FROM maps m
JOIN users u ON u.id = m.owner_id
WHERE m.moderation = 'pending' AND m.deleted_at IS NULL
ORDER BY m.updated_at
LIMIT 100;

-- name: ModerateMap :execrows
UPDATE maps SET moderation = @moderation, moderation_note = @moderation_note, updated_at = now()
WHERE slug = @slug AND moderation = 'pending' AND deleted_at IS NULL;

-- name: LibraryPoints :many
-- The library's panoramas in view, thinned to one per grid cell: where a map maker can find streets.
SELECT DISTINCT ON (floor(ST_X(p.pos) / @cell::float8), floor(ST_Y(p.pos) / @cell::float8))
       ST_Y(p.pos)::float8 AS lat, ST_X(p.pos)::float8 AS lon
FROM panoramas p
WHERE p.status = 'active' AND p.provider = ANY (@providers::text[])
  AND p.pos && ST_MakeEnvelope(@west::float8, @south::float8, @east::float8, @north::float8, 4326)
LIMIT 800;

-- name: NamedStreets :many
-- Every named street with panoramas on the players' providers, for the map maker.
SELECT s.id, s.name_uz, s.name_ru, count(p.id)::int AS panoramas
FROM streets s
JOIN panoramas p ON p.street_id = s.id AND p.status = 'active' AND p.provider = ANY (@providers::text[])
GROUP BY s.id
ORDER BY s.name_uz;

-- ---------- streets I miss ----------

-- name: UpsertMissMap :one
INSERT INTO maps (slug, owner_id, name_uz, name_ru, name_en, visibility, rules, pick, sort)
VALUES (@slug, @owner_id, @name_uz, @name_ru, @name_en, 'private', @rules, @pick, 3000)
ON CONFLICT (slug) DO UPDATE SET name_uz = excluded.name_uz, name_ru = excluded.name_ru,
    name_en = excluded.name_en, pick = excluded.pick, updated_at = now()
RETURNING id;

-- name: AddMissedStreets :exec
-- The player's map of missed streets: every street been on and not yet guessed within @learned_km.
INSERT INTO map_sources (map_id, street_id)
SELECT @map_id, p.street_id
FROM guesses g
JOIN rounds r ON r.game_id = g.game_id AND r.n = g.n
JOIN panoramas p ON p.id = r.panorama_id
WHERE g.user_id = @user_id AND g.guessed_at IS NOT NULL AND p.street_id IS NOT NULL
GROUP BY p.street_id
HAVING coalesce(min(g.km), 'Infinity'::float8) > @learned_km::float8;
