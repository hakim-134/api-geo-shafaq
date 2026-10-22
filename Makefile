# make up | setup | run | worker | test | gen | lint | smoke
OSM_PBF ?= data/osm/uzbekistan-latest.osm.pbf

up:            ## Postgres + PostGIS and Valkey, in Docker
	docker compose up -d db valkey

$(OSM_PBF):
	mkdir -p $(dir $@)
	curl -fSL -o $@ https://download.geofabrik.de/asia/uzbekistan-latest.osm.pbf

setup: up $(OSM_PBF)   ## an empty database → places, the v0 library, demo twins, maps
	go run ./cmd/tools setup -pbf $(OSM_PBF)

run:           ## the API on :8131
	go run ./cmd/api

worker:
	go run ./cmd/worker

test:          ## unit tests; integration tests too when TEST_DATABASE_URL is set
	go test ./...

gen:           ## after changing api/openapi.yaml, queries/ or migrations/
	go tool oapi-codegen -config api/oapi-codegen.yaml api/openapi.yaml
	sqlc generate

lint:
	gofmt -l . | grep -v '^_v0/' | (! grep .)
	go vet ./...

smoke:         ## play a demo game against the running API
	scripts/smoke.sh

.PHONY: up setup run worker test gen lint smoke
