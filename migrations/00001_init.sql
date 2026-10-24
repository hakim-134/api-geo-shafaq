-- +goose Up
CREATE EXTENSION IF NOT EXISTS postgis;

-- Places: country → region (viloyat) → district or city (tuman / shahar). From OSM admin
-- boundaries (ODbL). size_km is the diagonal of the bounding box: the scoring size D.
CREATE TABLE places (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    parent_id   bigint REFERENCES places (id),
    kind        text   NOT NULL CHECK (kind IN ('country', 'region', 'district', 'city')),
    slug        text   NOT NULL UNIQUE,
    name_uz     text   NOT NULL,
    name_ru     text   NOT NULL DEFAULT '',
    name_en     text   NOT NULL DEFAULT '',
    osm_id      bigint UNIQUE,
    geom        geometry(MultiPolygon, 4326) NOT NULL,
    geom_simple geometry(MultiPolygon, 4326) NOT NULL,
    size_km     double precision NOT NULL,
    outline_svg text   NOT NULL DEFAULT '',
    banner_url    text,
    banner_credit text,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX places_geom_idx ON places USING gist (geom);
CREATE INDEX places_parent_idx ON places (parent_id);

-- Named streets, for "you were on X street" and the streets-learned progress.
CREATE TABLE streets (
    id       bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    place_id bigint NOT NULL REFERENCES places (id),
    name_uz  text   NOT NULL,
    name_ru  text   NOT NULL DEFAULT '',
    UNIQUE (place_id, name_uz)
);

-- Points of interest shown after a round. Only metro stations for now.
CREATE TABLE pois (
    id      bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind    text   NOT NULL CHECK (kind IN ('metro')),
    name_uz text   NOT NULL,
    name_ru text   NOT NULL DEFAULT '',
    pos     geometry(Point, 4326) NOT NULL,
    UNIQUE (kind, name_uz)
);
CREATE INDEX pois_pos_idx ON pois USING gist (pos);

-- The panorama library. Panoramas belong to providers, not to maps: a map points at places,
-- and a round picks a panorama from here.
CREATE TABLE panoramas (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    provider   text   NOT NULL,
    ref        text   NOT NULL, -- the provider's id; for Yandex the library point "lat,lon"
    pos        geometry(Point, 4326) NOT NULL,
    heading    real,
    taken_at   date,
    street_id  bigint REFERENCES streets (id),
    place_ids  bigint[] NOT NULL DEFAULT '{}', -- every place it is inside, country down
    source     text   NOT NULL, -- v0_locations | v0_streets | pipeline | demo | google_scan
    status     text   NOT NULL DEFAULT 'active'
        CHECK (status IN ('candidate', 'active', 'rejected', 'gone')),
    checked_at timestamptz,     -- a viewer last opened it here
    fail_count int NOT NULL DEFAULT 0, -- viewers that could not open it since; 3 = gone
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, ref)
);
CREATE INDEX panoramas_pos_idx ON panoramas USING gist (pos);
CREATE INDEX panoramas_places_idx ON panoramas USING gin (place_ids);
CREATE INDEX panoramas_street_idx ON panoramas (street_id);

CREATE TABLE users (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text    NOT NULL,
    avatar       text    NOT NULL,
    guest        boolean NOT NULL DEFAULT true,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now()
);

-- Opaque session tokens; only the SHA-256 is stored.
CREATE TABLE sessions (
    token_hash   bytea PRIMARY KEY,
    user_id      uuid  NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    user_agent   text  NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL
);
CREATE INDEX sessions_user_idx ON sessions (user_id);
CREATE INDEX sessions_expires_idx ON sessions (expires_at);

-- Everything playable is a map: the official ones, and later the players' own.
CREATE TABLE maps (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    slug       text  NOT NULL UNIQUE,
    owner_id   uuid  REFERENCES users (id), -- NULL = official
    name_uz    text  NOT NULL,
    name_ru    text  NOT NULL DEFAULT '',
    name_en    text  NOT NULL DEFAULT '',
    desc_uz    text  NOT NULL DEFAULT '',
    desc_ru    text  NOT NULL DEFAULT '',
    desc_en    text  NOT NULL DEFAULT '',
    visibility text  NOT NULL DEFAULT 'public' CHECK (visibility IN ('public', 'unlisted', 'private')),
    rules      jsonb NOT NULL, -- default Rules
    pick       jsonb NOT NULL, -- how rounds are picked, see internal/game/pick.go
    sort       int   NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- What a map is made of: whole places, single streets or hand-picked panoramas.
CREATE TABLE map_sources (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    map_id      bigint NOT NULL REFERENCES maps (id) ON DELETE CASCADE,
    place_id    bigint REFERENCES places (id),
    street_id   bigint REFERENCES streets (id),
    panorama_id bigint REFERENCES panoramas (id),
    CHECK (num_nonnulls(place_id, street_id, panorama_id) = 1)
);
CREATE INDEX map_sources_map_idx ON map_sources (map_id);

CREATE TABLE games (
    id          uuid  PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid  NOT NULL REFERENCES users (id),
    map_id      bigint NOT NULL REFERENCES maps (id),
    mode        text  NOT NULL DEFAULT 'solo' CHECK (mode IN ('solo', 'room', 'challenge')),
    rules       jsonb NOT NULL,
    size_km     double precision NOT NULL, -- the map's D when the game started
    seed        bigint NOT NULL,
    status      text  NOT NULL DEFAULT 'playing' CHECK (status IN ('playing', 'finished', 'abandoned')),
    total       int   NOT NULL DEFAULT 0,
    hints_used  int   NOT NULL DEFAULT 0,
    started_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz
);
CREATE INDEX games_user_idx ON games (user_id, started_at DESC);
CREATE INDEX games_playing_idx ON games (started_at) WHERE status = 'playing';

CREATE TABLE rounds (
    game_id     uuid   NOT NULL REFERENCES games (id) ON DELETE CASCADE,
    n           int    NOT NULL,
    panorama_id bigint NOT NULL REFERENCES panoramas (id),
    provider    text   NOT NULL,
    near        geometry(Point, 4326) NOT NULL, -- the point given to the viewer
    snap_km     double precision NOT NULL,
    heading     real   NOT NULL DEFAULT 0,
    actual      geometry(Point, 4326),          -- the panorama the viewer opened
    created_at  timestamptz NOT NULL DEFAULT now(),
    shown_at    timestamptz,
    deadline    timestamptz,
    replaced    int NOT NULL DEFAULT 0,          -- panoramas swapped because the viewer failed
    PRIMARY KEY (game_id, n)
);

-- One row per player and round. Created by the first hint or the guess.
CREATE TABLE guesses (
    game_id    uuid NOT NULL,
    n          int  NOT NULL,
    user_id    uuid NOT NULL REFERENCES users (id),
    hints      jsonb NOT NULL DEFAULT '[]',
    pos        geometry(Point, 4326),
    km         double precision,
    score      int     NOT NULL DEFAULT 0,
    late       boolean NOT NULL DEFAULT false,
    guessed_at timestamptz,
    PRIMARY KEY (game_id, n, user_id),
    FOREIGN KEY (game_id, n) REFERENCES rounds (game_id, n) ON DELETE CASCADE
);
CREATE INDEX guesses_user_idx ON guesses (user_id);

-- Panorama views per provider and day. Yandex's free key allows ~100 a day; going over
-- again and again gets the key blocked for good.
CREATE TABLE provider_usage (
    day      date NOT NULL,
    provider text NOT NULL,
    views    int  NOT NULL DEFAULT 0,
    warned   boolean NOT NULL DEFAULT false,
    PRIMARY KEY (day, provider)
);

-- +goose Down
DROP TABLE provider_usage, guesses, rounds, games, map_sources, maps, sessions, users,
    panoramas, pois, streets, places;
