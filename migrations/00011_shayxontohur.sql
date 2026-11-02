-- +goose Up
-- The Uzbek spelling (places.uzFixes): OpenStreetMap writes "Shayhontohur".
UPDATE places SET name_uz = 'Shayxontohur tumani' WHERE slug = 'shayhontohur-tumani';

-- +goose Down
UPDATE places SET name_uz = 'Shayhontohur tumani' WHERE slug = 'shayhontohur-tumani';
