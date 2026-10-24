# api-geo-shafaq

The server of [geo-shafaq](https://github.com/hakim-134/geo-shafaq), a street-level geography
game for Uzbekistan. It picks every round's panorama, keeps the clock and scores the guesses;
the game only shows what it is sent. It serves no pages.

Based on [BeloGuessr](https://github.com/NotiLo-A/BeloGuessr) by NotiLo-A. The first version
(the live-rooms server) is kept in `_v0/` and as the git tag `v0`.

## Run locally

Go 1.26+, Docker, and a Postgres (16 or newer, with PostGIS) and a Redis (or Valkey). On the
owner's laptop those are the machine's shared `postgres` and `redis` containers (Docker network
`infra`); the API gets the database `shafaq_lcl` and Redis database 1.

```bash
cp .envrc.example .envrc && direnv allow    # PGPASSWORD: the Postgres password
make up        # the database shafaq_lcl with PostGIS, in the `postgres` container
make setup     # downloads the OSM extract, then places, the panorama library and the maps
make run       # the API on :8131
make smoke     # plays a whole demo game against it
```

Or the server in Docker, rebuilt from this tree: `make lcl` (`docker-compose.lcl.yml`: one
container, migrations at start, then the worker and the API on :8131).

`make test` runs the unit tests, and the integration tests too when `TEST_DATABASE_URL` is set
(each test gets its own throwaway database).

## The contract

`api/openapi.yaml` is the one source of truth between the two repos. Change it first, then
`make gen` here (server types) and `npm run gen` in geo-shafaq (client types). SQL lives in
`queries/` and `migrations/`; `make gen` also runs sqlc.

## Layout

| Where | What |
|---|---|
| `cmd/api` | the HTTP API |
| `cmd/worker` | background jobs (River, in Postgres): Yandex usage warning, cleanup |
| `cmd/tools` | `migrate`, `places`, `v0`, `demo`, `maps`, `tag`, `stats`, `google`, `setup` |
| `internal/game` | solo games: round picking, the clock, hints, scoring |
| `internal/maps` | maps (the official four for now) and the round picker |
| `internal/provider` | the panorama providers behind one interface: `yandex`, `google`, `demo` |
| `internal/places` | the place tree from OSM, outlines, "where is this point" |
| `internal/score`, `internal/rules` | the score formula and the one rule set |
| `internal/players` | guests (🐼_42) and sessions |
| `internal/httpapi` | handlers for the generated routes, CORS, the write check, metrics |
| `internal/store` | sqlc output; never edit by hand |
| `data/v0` | v0's panorama pools, the seed of the library |

## How a round works

1. `POST /v1/games/{id}/rounds`: the server picks a panorama from the map's places (a street or
   district first, weighted, so small places still come up; no repeats; not too close to an
   earlier round) and sends only what the viewer needs. Yandex gets a point to search near; our
   own and demo panoramas get an opaque image URL with no position.
2. `.../shown`: the panorama is on screen and the clock starts (at the latest a minute after
   step 1). For Yandex the browser reports the panorama it actually opened; it must be within
   the map's snap distance, and the round is scored against it.
3. `.../hint`: the district or region, and a circle that holds the place; each next hint halves it.
4. `.../guess`: scored here, `5000 · e^(−10·d / (D·scale))`, D = the map's size. A guess more
   than 3 s after the deadline scores 0.

A viewer that cannot open a panorama calls `.../fail`; the round gets another one, and a
panorama that fails three times is retired.

## Yandex usage

The free key allows about 100 panorama views a day and no paid products; going over again and
again gets the key blocked for good. Two counts, one warning a day at 80% (in the log, and on
Telegram if `TELEGRAM_ALERT_TOKEN`/`_CHAT` are set; to find the chat ID, write to the bot
while the worker runs, or send `/id` in a group, and it answers with the ID):

- **Ours**: every round handed out (`provider_usage`, the `shafaq_panorama_views_today` metric).
- **Yandex's own**, which is the one that decides a block (free terms §4.3): the worker reads it
  every 10 minutes from the Developer's Account API when `YANDEX_CABINET_KEY` is set
  (developer.tech.yandex.ru → Connect API → Developer's Account API; metric
  `shafaq_yandex_requests_today`). It is higher than ours: the browser also calls `locate`,
  and every step in a Move round loads a panorama.

Days run by Moscow time, as Yandex resets its limits. Buy the commercial license before launch.

## Docker

`docker compose up -d --build` runs everything as on the server, its own Postgres + PostGIS and
Valkey included: `migrate` once, then `api` and `worker`. Settings come from the environment (`.envrc.example` lists them; secrets never go into
the repo). Prometheus metrics are on `METRICS_ADDR` (private), health on `/v1/health`, readiness
on `/v1/ready`.

Data: places, streets and metro © OpenStreetMap contributors (ODbL).
