-- +goose Up
-- Guests' names become their avatar's word and the number: "🦉_42" → "Ukki 42" (P1028 2.4).
-- Only names still as the game made them; a name a player chose stays. The words are
-- players.avatars' on 2026-10-29; 🦥 and 🐵 left the avatars (their words, "yalqov" and
-- "maymun", tease), so their guests are "Mehmon 42".
UPDATE users u
SET name = w.word || ' ' || (CASE WHEN right(u.name, 2)::int < 10 THEN right(u.name, 2)::int + 10 ELSE right(u.name, 2)::int END)::text
FROM (VALUES
    ('🐼', 'Panda'),
    ('🦊', 'Tulki'),
    ('🐨', 'Koala'),
    ('🐯', 'Yo‘lbars'),
    ('🦁', 'Sher'),
    ('🐸', 'Qurbaqa'),
    ('🐵', 'Mehmon'),
    ('🦉', 'Ukki'),
    ('🐧', 'Pingvin'),
    ('🐢', 'Toshbaqa'),
    ('🐙', 'Sakkizoyoq'),
    ('🦋', 'Kapalak'),
    ('🐝', 'Asalari'),
    ('🐞', 'Xonqizi'),
    ('🦄', 'Yakkashox'),
    ('🐳', 'Kit'),
    ('🐬', 'Delfin'),
    ('🦒', 'Jirafa'),
    ('🦓', 'Zebra'),
    ('🐘', 'Fil'),
    ('🦔', 'Tipratikan'),
    ('🦦', 'Suvsar'),
    ('🦥', 'Mehmon'),
    ('🐿️', 'Olmaxon'),
    ('🦩', 'Flamingo'),
    ('🦜', 'To‘tiqush'),
    ('🐰', 'Quyon'),
    ('🐻', 'Ayiq'),
    ('🐻‍❄️', 'Oqayiq'),
    ('🐹', 'Xomyak'),
    ('🐱', 'Mushuk'),
    ('🐶', 'Kuchukcha'),
    ('🐴', 'Ot'),
    ('🐫', 'Tuya'),
    ('🦌', 'Bug‘u'),
    ('🦚', 'Tovus'),
    ('🦭', 'Tyulen'),
    ('🐠', 'Baliqcha'),
    ('🦀', 'Qisqichbaqa'),
    ('🐌', 'Chig‘anoq'),
    ('🌵', 'Kaktus'),
    ('🌻', 'Kungaboqar'),
    ('🌷', 'Lola'),
    ('🍀', 'Beda'),
    ('🌳', 'Chinor'),
    ('🍉', 'Tarvuz'),
    ('🍎', 'Olma'),
    ('🍇', 'Uzum'),
    ('🍋', 'Limon'),
    ('🥝', 'Kivi'),
    ('🥕', 'Sabzi'),
    ('🌽', 'Makkajo‘xori'),
    ('🍄', 'Qo‘ziqorin'),
    ('⭐', 'Yulduz'),
    ('🌙', 'Oy'),
    ('⛰️', 'Tog‘'),
    ('🎈', 'Shar'),
    ('🪁', 'Varrak'),
    ('🚲', 'Velosiped'),
    ('🚀', 'Raketa'),
    ('⚽', 'To‘p'),
    ('🏀', 'Basketbol'),
    ('🎨', 'Rassom'),
    ('🧩', 'Jumboq'),
    ('📚', 'Kitob'),
    ('🎧', 'Quloqchin'),
    ('🧭', 'Kompas'),
    ('🗺️', 'Xarita')
) AS w(avatar, word)
WHERE u.avatar = w.avatar
  AND right(u.name, 2) ~ '^[0-9]{2}$'
  AND u.name = u.avatar || '_' || right(u.name, 2);

-- +goose Down
-- The old names can't be told from chosen ones that look the same: nothing to undo.
SELECT 1;
