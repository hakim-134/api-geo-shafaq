-- +goose Up
-- Uzbek names OpenStreetMap has wrong (places.uzFixes): Urgut, Mo‘ynoq, Bo‘zatov, Qo‘shko‘pir,
-- Ellikqalʼa… and towns with "shahri". Slugs stay.
UPDATE places p SET name_uz = f.name
FROM (VALUES
    ('angren', 'Angren shahri'),
    ('bekobod', 'Bekobod shahri'),
    ('ohangaron', 'Ohangaron shahri'),
    ('nurafshon-toytepa', 'Nurafshon shahri'),
    ('bozataw-rayoni', 'Bo‘zatov tumani'),
    ('ellikqala-tumani', 'Ellikqalʼa tumani'),
    ('tuproqqala-tumani', 'Tuproqqalʼa tumani'),
    ('kegeili-tumani', 'Kegeyli tumani'),
    ('moynoq-tumani', 'Mo‘ynoq tumani'),
    ('qoshkopir-tumani', 'Qo‘shko‘pir tumani'),
    ('ugrut-tumani', 'Urgut tumani'),
    ('showot-tumani', 'Shovot tumani'),
    ('qonlikol-tumani', 'Qanliko‘l tumani')
) AS f(slug, name)
WHERE p.slug = f.slug;

-- +goose Down
-- The next places import writes the same names again: nothing to undo.
SELECT 1;
