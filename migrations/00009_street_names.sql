-- +goose Up
-- Tashkent's streets, read once whole (P1028 2.4), the same fixes as data/v0/tashkent.json.
-- Doubles merge into the real street (their panoramas move to it), wrong spellings take
-- the name Yandex's map shows, and two names that are no street ("Srkver" is the Amir Temur
-- square, "Chkalovo" a crossroads) lose their street: their panoramas stay in the city
-- without one. Merged ids go from map_sources too, though no map uses a street yet.
CREATE TEMP TABLE street_merge (from_uz text, to_uz text) ON COMMIT DROP;
INSERT INTO street_merge VALUES
    ('Kipchak Street', 'Qipchoq ko‘chasi'),
    ('Chilonzor kochasi', 'Chilonzor ko‘chasi'),
    ('Zulfiyakhonim ko‘chasi', 'Zulfiyaxonim ko‘chasi'),
    ('Navo‘z ko‘chasi', 'Navro‘z ko‘chasi'),
    ('Beshyog‘och maydoni', 'Beshyog‘och ko‘chasi'),
    ('Quloqtepa ko‘chasi', 'Rixsiliy ko‘chasi');

UPDATE panoramas p SET street_id = t.id
FROM street_merge m
JOIN streets f ON f.name_uz = m.from_uz
JOIN streets t ON t.name_uz = m.to_uz AND t.place_id = f.place_id
WHERE p.street_id = f.id;

UPDATE map_sources ms SET street_id = t.id
FROM street_merge m
JOIN streets f ON f.name_uz = m.from_uz
JOIN streets t ON t.name_uz = m.to_uz AND t.place_id = f.place_id
WHERE ms.street_id = f.id;

UPDATE panoramas SET street_id = NULL
WHERE street_id IN (SELECT id FROM streets WHERE name_uz IN ('Srkver', 'Chkalovo'));
DELETE FROM map_sources
WHERE street_id IN (SELECT id FROM streets WHERE name_uz IN ('Srkver', 'Chkalovo'));

DELETE FROM streets
WHERE name_uz IN (SELECT from_uz FROM street_merge) OR name_uz IN ('Srkver', 'Chkalovo');

UPDATE streets s SET name_uz = f.name
FROM (VALUES
    ('Bogishamol ko‘chasi', 'Bog‘ishamol ko‘chasi'),
    ('Mirzo Golib ko‘chasi', 'Mirzo G‘olib ko‘chasi'),
    ('Bunyod ko‘cha', 'Bunyod ko‘chasi'),
    ('Geydar Aliev ko‘chasi', 'Haydar Aliyev ko‘chasi'),
    ('Muminov ko‘chasi', 'Mo‘minov ko‘chasi'),
    ('Gavxar ko‘chasi', 'Gavhar ko‘chasi'),
    (E'Chorbog‘ ​ko‘chasi', 'Chorbog‘ ko‘chasi'),
    ('Beshkayrogoch ko‘chasi', 'Beshqayrag‘och ko‘chasi'),
    ('Gazalkent ko‘chasi', 'G‘azalkent ko‘chasi'),
    ('Kukcha Darvoza ko‘chasi', 'Ko‘kcha Darvoza ko‘chasi'),
    ('Maksud Shayxzoda ko‘chasi', 'Maqsud Shayxzoda ko‘chasi'),
    ('Iftihor ko‘chasi', 'Iftixor ko‘chasi'),
    ('Yangi sirg‘ali ko‘chasi', 'Yangi Sirg‘ali ko‘chasi'),
    ('Kichik Halqa yo‘li', 'Kichik halqa yo‘li')
) AS f(old, name)
WHERE s.name_uz = f.old;

-- +goose Down
-- The merged and removed streets are gone; the next v0 import would write the old list
-- again only from an older data file. Nothing to undo.
SELECT 1;
