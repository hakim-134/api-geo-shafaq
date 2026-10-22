-- name: UpsertPlace :one
-- geojson is the boundary as GeoJSON linework (the relation's ways); PostGIS builds the area.
INSERT INTO places (kind, slug, name_uz, name_ru, name_en, osm_id, geom, geom_simple, size_km)
SELECT @kind, @slug, @name_uz, @name_ru, @name_en, @osm_id, a.g,
       ST_Multi(ST_CollectionExtract(ST_MakeValid(ST_SimplifyPreserveTopology(a.g, @simplify::float8)), 3)),
       ST_DistanceSphere(ST_SetSRID(ST_MakePoint(ST_XMin(a.g), ST_YMin(a.g)), 4326),
                         ST_SetSRID(ST_MakePoint(ST_XMax(a.g), ST_YMax(a.g)), 4326)) / 1000
FROM (SELECT ST_Multi(ST_CollectionExtract(ST_MakeValid(ST_BuildArea(ST_SetSRID(ST_GeomFromGeoJSON(@geojson::text), 4326))), 3)) AS g) a
WHERE NOT ST_IsEmpty(a.g)
ON CONFLICT (osm_id) DO UPDATE SET
    kind = excluded.kind, slug = excluded.slug, name_uz = excluded.name_uz,
    name_ru = excluded.name_ru, name_en = excluded.name_en, geom = excluded.geom,
    geom_simple = excluded.geom_simple, size_km = excluded.size_km
RETURNING id;

-- name: DeletePlacesOutside :execrows
-- Drops places of neighbouring countries that came in with the shared border ways.
DELETE FROM places p
WHERE p.kind <> 'country'
  AND NOT EXISTS (SELECT 1 FROM places c
                  WHERE c.kind = 'country'
                    AND ST_Area(ST_Intersection(c.geom, p.geom)) > 0.5 * ST_Area(p.geom))
  AND NOT EXISTS (SELECT 1 FROM map_sources s WHERE s.place_id = p.id);

-- name: LinkPlaceParents :exec
-- Each place's parent is the smallest bigger place that holds it.
UPDATE places p
SET parent_id = (SELECT q.id FROM places q
                 WHERE q.id <> p.id AND q.size_km > p.size_km
                   AND ST_Contains(q.geom, ST_PointOnSurface(p.geom))
                 ORDER BY q.size_km LIMIT 1);

-- name: ListPlacesForOutline :many
SELECT id, ST_AsGeoJSON(geom_simple, 5)::text AS geojson FROM places;

-- name: SetPlaceOutline :exec
UPDATE places SET outline_svg = @outline_svg WHERE id = @id;

-- name: PlaceBySlug :one
SELECT id, kind, slug, name_uz, name_ru, name_en, size_km FROM places WHERE slug = @slug;

-- name: CountPlaces :many
SELECT kind, count(*)::int AS n FROM places GROUP BY kind ORDER BY kind;

-- name: PlacesAt :many
-- The places holding a point, smallest first.
SELECT id, kind, slug, name_uz, name_ru, name_en, size_km
FROM places
WHERE ST_Contains(geom, ST_SetSRID(ST_MakePoint(@lon::float8, @lat::float8), 4326))
ORDER BY size_km;

-- name: NearestStreet :one
-- The street a point is on: the nearest street panorama within @max_m metres.
SELECT s.id, s.name_uz, s.name_ru
FROM panoramas p
JOIN streets s ON s.id = p.street_id
WHERE ST_DWithin(p.pos::geography, ST_SetSRID(ST_MakePoint(@lon::float8, @lat::float8), 4326)::geography, @max_m::float8)
ORDER BY p.pos <-> ST_SetSRID(ST_MakePoint(@lon::float8, @lat::float8), 4326)
LIMIT 1;

-- name: NearestMetro :one
SELECT name_uz, name_ru,
       (ST_DistanceSphere(pos, ST_SetSRID(ST_MakePoint(@lon::float8, @lat::float8), 4326)) / 1000)::float8 AS km
FROM pois
WHERE kind = 'metro'
ORDER BY pos <-> ST_SetSRID(ST_MakePoint(@lon::float8, @lat::float8), 4326)
LIMIT 1;

-- name: UpsertStreet :one
INSERT INTO streets (place_id, name_uz, name_ru)
VALUES (@place_id, @name_uz, @name_ru)
ON CONFLICT (place_id, name_uz) DO UPDATE SET name_ru = excluded.name_ru
RETURNING id;

-- name: UpsertMetro :exec
INSERT INTO pois (kind, name_uz, name_ru, pos)
VALUES ('metro', @name_uz, @name_ru, ST_SetSRID(ST_MakePoint(@lon::float8, @lat::float8), 4326))
ON CONFLICT (kind, name_uz) DO UPDATE SET name_ru = excluded.name_ru, pos = excluded.pos;

-- name: PlaceShape :one
SELECT slug, ST_AsGeoJSON(geom_simple, 4)::text AS geojson, outline_svg FROM places WHERE slug = @slug;
