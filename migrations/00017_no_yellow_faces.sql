-- +goose Up
-- The faces that vanish on the gold coin go (🌙 ⭐ 🍋 🌽 🌻: yellow on gold, a blank coin). Each
-- player who has one gets a random face of those left (players.avatars on 2026-11-23), and, if
-- the name is still the one the game made from the old face ("Oy 42"), the new face's word with
-- the same number ("Tulki 42"). A name the player chose stays.
WITH faces(n, avatar, word) AS (
    VALUES
        (1, '🐼', 'Panda'),
        (2, '🦊', 'Tulki'),
        (3, '🐨', 'Koala'),
        (4, '🐯', 'Yo‘lbars'),
        (5, '🦁', 'Sher'),
        (6, '🐸', 'Qurbaqa'),
        (7, '🦉', 'Ukki'),
        (8, '🐧', 'Pingvin'),
        (9, '🐢', 'Toshbaqa'),
        (10, '🐙', 'Sakkizoyoq'),
        (11, '🦋', 'Kapalak'),
        (12, '🐝', 'Asalari'),
        (13, '🐞', 'Xonqizi'),
        (14, '🦄', 'Yakkashox'),
        (15, '🐳', 'Kit'),
        (16, '🐬', 'Delfin'),
        (17, '🦒', 'Jirafa'),
        (18, '🦓', 'Zebra'),
        (19, '🐘', 'Fil'),
        (20, '🦔', 'Tipratikan'),
        (21, '🦦', 'Suvsar'),
        (22, '🐿️', 'Olmaxon'),
        (23, '🦩', 'Flamingo'),
        (24, '🦜', 'To‘tiqush'),
        (25, '🐰', 'Quyon'),
        (26, '🐻', 'Ayiq'),
        (27, '🐻‍❄️', 'Oqayiq'),
        (28, '🐹', 'Xomyak'),
        (29, '🐱', 'Mushuk'),
        (30, '🐶', 'Kuchukcha'),
        (31, '🐴', 'Ot'),
        (32, '🐫', 'Tuya'),
        (33, '🦌', 'Bug‘u'),
        (34, '🦚', 'Tovus'),
        (35, '🦭', 'Tyulen'),
        (36, '🐠', 'Baliqcha'),
        (37, '🦀', 'Qisqichbaqa'),
        (38, '🐌', 'Chig‘anoq'),
        (39, '🌵', 'Kaktus'),
        (40, '🌷', 'Lola'),
        (41, '🍀', 'Beda'),
        (42, '🌳', 'Chinor'),
        (43, '🍉', 'Tarvuz'),
        (44, '🍎', 'Olma'),
        (45, '🍇', 'Uzum'),
        (46, '🥝', 'Kivi'),
        (47, '🥕', 'Sabzi'),
        (48, '🍄', 'Qo‘ziqorin'),
        (49, '⛰️', 'Tog‘'),
        (50, '🎈', 'Shar'),
        (51, '🪁', 'Varrak'),
        (52, '🚲', 'Velosiped'),
        (53, '🚀', 'Raketa'),
        (54, '⚽', 'To‘p'),
        (55, '🏀', 'Basketbol'),
        (56, '🎨', 'Rassom'),
        (57, '🧩', 'Jumboq'),
        (58, '📚', 'Kitob'),
        (59, '🎧', 'Quloqchin'),
        (60, '🧭', 'Kompas'),
        (61, '🗺️', 'Xarita')
), old(avatar, word) AS (
    VALUES ('🌙', 'Oy'), ('⭐', 'Yulduz'), ('🍋', 'Limon'), ('🌽', 'Makkajo‘xori'), ('🌻', 'Kungaboqar')
), moved AS MATERIALIZED (
    -- random() once per player, not once for all
    SELECT u.id, u.name, o.word AS old_word,
           1 + floor(random() * (SELECT count(*) FROM faces))::int AS n
    FROM users u
    JOIN old o ON o.avatar = u.avatar
)
UPDATE users u
SET avatar = f.avatar,
    name = CASE WHEN m.name ~ ('^' || m.old_word || ' [0-9]{2}$')
                THEN f.word || ' ' || right(m.name, 2)
                ELSE m.name END
FROM moved m
JOIN faces f ON f.n = m.n
WHERE u.id = m.id;

-- +goose Down
-- Which face a player had before can't be told: nothing to undo.
SELECT 1;
