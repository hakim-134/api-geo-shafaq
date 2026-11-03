-- +goose Up
-- Maps by players (PLAN 8.1, 8.2, 8.5). A drawn area is a source like a place or a street, so a
-- map fills as the library grows. A public map waits in moderation: until a person approves it, it
-- plays by its link and isn't listed. Deleted maps stay: finished games point at them.
ALTER TABLE maps
    ADD COLUMN moderation text NOT NULL DEFAULT 'none'
        CHECK (moderation IN ('none', 'pending', 'approved', 'rejected')),
    ADD COLUMN moderation_note text NOT NULL DEFAULT '',
    ADD COLUMN tags text[] NOT NULL DEFAULT '{}',
    ADD COLUMN deleted_at timestamptz;

CREATE INDEX maps_owner_idx ON maps (owner_id, created_at DESC) WHERE owner_id IS NOT NULL;
CREATE INDEX maps_queue_idx ON maps (updated_at) WHERE moderation = 'pending';

ALTER TABLE map_sources ADD COLUMN area geometry(MultiPolygon, 4326);
ALTER TABLE map_sources DROP CONSTRAINT map_sources_check;
ALTER TABLE map_sources ADD CONSTRAINT map_sources_check
    CHECK (num_nonnulls(place_id, street_id, panorama_id, area) = 1);

CREATE TABLE map_likes (
    map_id     bigint NOT NULL REFERENCES maps (id) ON DELETE CASCADE,
    user_id    uuid   NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (map_id, user_id)
);

CREATE INDEX games_map_idx ON games (map_id, status);

-- +goose Down
DROP INDEX games_map_idx;
DROP TABLE map_likes;
DELETE FROM map_sources WHERE area IS NOT NULL;
ALTER TABLE map_sources DROP CONSTRAINT map_sources_check;
ALTER TABLE map_sources ADD CONSTRAINT map_sources_check
    CHECK (num_nonnulls(place_id, street_id, panorama_id) = 1);
ALTER TABLE map_sources DROP COLUMN area;
DROP INDEX maps_queue_idx;
DROP INDEX maps_owner_idx;
ALTER TABLE maps DROP COLUMN deleted_at, DROP COLUMN tags, DROP COLUMN moderation_note, DROP COLUMN moderation;
