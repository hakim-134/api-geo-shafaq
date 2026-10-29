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
SELECT id, kind, slug, name_uz, name_ru, name_en, size_km,
       coalesce((SELECT q.kind FROM places q WHERE q.id = places.parent_id), '')::text AS parent_kind
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

-- name: PlaceMapShapes :many
-- A place's children drawn in one frame, for the place map (P1028 4.1): Web Mercator, the
-- longer side 1000 wide, y down (ST_AsSVG turns y over), as SVG paths, each with a point
-- well inside it for its name. x0, y1 and k place other points in the same frame.
WITH kids AS (
    SELECT c.slug, ST_Transform(c.geom_simple, 3857) AS g
    FROM places c
    JOIN places p ON p.id = c.parent_id
    WHERE p.slug = @slug AND c.geom_simple IS NOT NULL
), box AS (
    SELECT ST_XMin(e) AS x0, ST_YMax(e) AS y1, ST_XMax(e) - ST_XMin(e) AS w, ST_YMax(e) - ST_YMin(e) AS h
    FROM (SELECT ST_Extent(g) AS e FROM kids) x
), f AS (
    SELECT x0, y1, w, h, 1000 / greatest(w, h) AS k FROM box
)
SELECT kids.slug,
       ST_AsSVG(ST_Scale(ST_Translate(kids.g, -f.x0, -f.y1), f.k, f.k), 0, 1)::text AS d,
       ((ST_X(m.c) - f.x0) * f.k)::float8 AS lx,
       ((f.y1 - ST_Y(m.c)) * f.k)::float8 AS ly,
       (f.w * f.k)::float8 AS width, (f.h * f.k)::float8 AS height,
       f.x0::float8 AS x0, f.y1::float8 AS y1, f.k::float8 AS k
FROM kids
CROSS JOIN f
CROSS JOIN LATERAL (SELECT (ST_MaximumInscribedCircle(kids.g)).center AS c) m
ORDER BY kids.slug;

-- name: PlacePoints :many
-- A point well inside each named place, in Web Mercator: marks on a place map.
SELECT slug, name_uz, name_ru, name_en, ST_X(c)::float8 AS x, ST_Y(c)::float8 AS y
FROM (
    SELECT slug, name_uz, name_ru, name_en,
           (ST_MaximumInscribedCircle(ST_Transform(geom_simple, 3857))).center AS c
    FROM places
    WHERE slug = ANY(@slugs::text[]) AND geom_simple IS NOT NULL
) p;

-- name: ListPlaces :many
-- The whole place tree, for the place picker.
SELECT id, coalesce(parent_id, 0)::bigint AS parent_id, kind, slug, name_uz, name_ru, name_en,
       size_km, outline_svg, coalesce(banner_url, '')::text AS banner_url,
       coalesce(banner_credit, '')::text AS banner_credit
FROM places
ORDER BY id;

-- name: PlaceCounts :many
-- Active panoramas and named streets with one, per place, on the given providers.
SELECT pid::bigint AS place_id, count(*)::int AS panoramas,
       count(DISTINCT p.street_id)::int AS streets
FROM panoramas p, unnest(p.place_ids) AS pid
WHERE p.status = 'active' AND p.provider = ANY (@providers::text[])
GROUP BY pid;

-- name: UserRoundPlaces :many
-- Every round the player guessed: the panorama's places and street, how far off the guess
-- was, and the places the guess landed in. For progress per place.
SELECT p.id AS panorama_id, p.place_ids, coalesce(p.street_id, 0)::bigint AS street_id, g.km,
       coalesce((SELECT array_agg(pl.id) FROM places pl WHERE ST_Contains(pl.geom, g.pos)),
                '{}')::bigint[] AS guess_places
FROM guesses g
JOIN rounds r ON r.game_id = g.game_id AND r.n = g.n
JOIN panoramas p ON p.id = r.panorama_id
WHERE g.user_id = @user_id AND g.guessed_at IS NOT NULL;
