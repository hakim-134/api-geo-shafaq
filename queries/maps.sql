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
-- map if it is inside one of the map's places, on one of its streets, or picked by hand.
SELECT p.id,
       p.provider,
       p.ref,
       ST_Y(p.pos)::float8 AS lat,
       ST_X(p.pos)::float8 AS lon,
       p.heading,
       p.street_id,
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
                AND (s.place_id = ANY (p.place_ids) OR s.street_id = p.street_id OR s.panorama_id = p.id));

-- name: MapPlaceExtent :one
-- The bounding box of a map's places: its size D for scoring.
SELECT coalesce(ST_XMin(e), 0)::float8 AS west, coalesce(ST_YMin(e), 0)::float8 AS south,
       coalesce(ST_XMax(e), 0)::float8 AS east, coalesce(ST_YMax(e), 0)::float8 AS north
FROM (SELECT ST_Extent(pl.geom)::geometry AS e
      FROM map_sources s JOIN places pl ON pl.id = s.place_id
      WHERE s.map_id = @map_id) x;

-- name: MapPlaces :many
-- A map's places with their outlines, largest first.
SELECT pl.id, pl.outline_svg
FROM map_sources s JOIN places pl ON pl.id = s.place_id
WHERE s.map_id = @map_id
ORDER BY pl.size_km DESC;
