-- +goose Up
-- Accounts (PLAN 3.2–3.7): sign-in identities on the same users as guests, the Telegram bot's
-- sign-in requests, merges waiting for a yes, reports and bans.

ALTER TABLE users
    ADD COLUMN banned_at  timestamptz,
    ADD COLUMN ban_reason text NOT NULL DEFAULT '';

-- A way to sign in. A user has any number; a guest has none.
CREATE TABLE identities (
    provider     text NOT NULL CHECK (provider IN ('telegram', 'google')),
    subject      text NOT NULL,           -- Telegram user id, Google "sub"
    user_id      uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         text NOT NULL DEFAULT '', -- shown on the profile so the player knows which one it is
    username     text NOT NULL DEFAULT '', -- @username or the e-mail address
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, subject)
);
CREATE INDEX identities_user_idx ON identities (user_id);

-- "Sign in with Telegram" through the bot: the game opens t.me/<bot>?start=login_<code>, the
-- bot fills in who pressed Start, the game that asked collects it. Only the code's hash is kept.
CREATE TABLE telegram_logins (
    code_hash  bytea PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE, -- the player who asked
    tg_id      bigint,
    tg_name    text NOT NULL DEFAULT '',
    tg_username text NOT NULL DEFAULT '',
    confirmed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);

-- An account that signed in with an identity of another account: both are the same person,
-- so the other one moves into this one once the player says yes.
CREATE TABLE merge_offers (
    token_hash bytea PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE, -- keeps everything
    other_id   uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE, -- moves in, then goes
    expires_at timestamptz NOT NULL
);

CREATE TABLE reports (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    reporter_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    reason      text NOT NULL CHECK (reason IN ('name', 'cheating', 'other')),
    name        text NOT NULL, -- the name at the time of the report
    created_at  timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz,
    UNIQUE (reporter_id, user_id, reason)
);
CREATE INDEX reports_open_idx ON reports (created_at) WHERE resolved_at IS NULL;

-- +goose Down
DROP TABLE reports, merge_offers, telegram_logins, identities;
ALTER TABLE users DROP COLUMN ban_reason, DROP COLUMN banned_at;
