-- +goose Up
-- Daily caps (DAILY_CAPS): a provider at its cap gets no new rounds until the next day.
-- stop_told: the owner heard that the cap was reached that day.
ALTER TABLE provider_usage ADD COLUMN stop_told boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE provider_usage DROP COLUMN stop_told;
