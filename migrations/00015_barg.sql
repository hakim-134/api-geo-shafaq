-- +goose Up
-- Barg, the one currency (PLAN 9.3), as a ledger: a row is never changed or removed, a balance is
-- the sum of the rows. key makes every earning and spending happen once, however often it is asked.
-- 00014 is taken: the parked players' maps (branch cld20261103_maps) use it.
CREATE TABLE barg_ledger (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES users (id),
    amount     int  NOT NULL CHECK (amount <> 0),
    reason     text NOT NULL, -- street | district | challenge | buy
    ref        text NOT NULL, -- what for: a street's id, a place's slug, a challenge's code, an item
    note       text NOT NULL DEFAULT '', -- its name, for the player's history
    key        text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX barg_ledger_user_idx ON barg_ledger (user_id, id DESC);
CREATE INDEX barg_ledger_ref_idx ON barg_ledger (user_id, reason, ref);

-- +goose StatementBegin
CREATE FUNCTION barg_ledger_append_only() RETURNS trigger AS $$
BEGIN
    -- only one change exists: a merged guest's rows move to the account they merged into
    IF TG_OP = 'UPDATE' AND NEW.id = OLD.id AND NEW.amount = OLD.amount AND NEW.reason = OLD.reason
       AND NEW.ref = OLD.ref AND NEW.note = OLD.note AND NEW.key = OLD.key AND NEW.created_at = OLD.created_at THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'barg_ledger is append-only';
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
CREATE TRIGGER barg_ledger_no_edits BEFORE UPDATE OR DELETE ON barg_ledger
    FOR EACH ROW EXECUTE FUNCTION barg_ledger_append_only();

-- What a player owns from the shop (PLAN 9.4). What they wear is in users.prefs.
CREATE TABLE shop_owned (
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    item       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, item)
);

-- The streets already learned count from the start: the learning came first.
INSERT INTO barg_ledger (user_id, amount, reason, ref, note, key)
SELECT x.user_id, 5, 'street', x.street_id::text, s.name_uz, 'street:' || x.street_id || ':' || x.user_id
FROM (SELECT DISTINCT g.user_id, p.street_id
      FROM guesses g
      JOIN rounds r ON r.game_id = g.game_id AND r.n = g.n
      JOIN panoramas p ON p.id = r.panorama_id
      WHERE g.guessed_at IS NOT NULL AND g.km <= 0.5 AND p.street_id IS NOT NULL) x
JOIN streets s ON s.id = x.street_id;

-- +goose Down
DROP TABLE shop_owned;
DROP TABLE barg_ledger;
DROP FUNCTION barg_ledger_append_only();
