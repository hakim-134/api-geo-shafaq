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
| `cmd/worker` | background jobs (River, in Postgres): Yandex usage warning, cleanup; the players' Telegram bot |
| `cmd/tools` | `migrate`, `places`, `v0`, `demo`, `maps`, `tag`, `stats`, `google`, `setup`; `reports`, `ban`, `unban` (moderation) |
| `internal/game` | solo games: round picking, the clock, hints, scoring |
| `internal/maps` | maps (the official four for now) and the round picker |
| `internal/provider` | the panorama providers behind one interface: `yandex`, `google`, `demo`. `PROVIDERS` are what players get (and what the menu counts); `TEST_PROVIDERS` play only in games that name them; `DAILY_CAPS` are each one's daily budget |
| `internal/platform` | logging, and error tracking: Error lines also go to GlitchTip when `GLITCHTIP_DSN` is set |
| `deploy/` | the server: Caddy, Prometheus, Loki/Alloy, Grafana (dashboard), the db image with pgBackRest, `deploy.sh`, `env.example` |
| `internal/places` | the place tree from OSM, outlines, "where is this point" |
| `internal/score`, `internal/rules` | the score formula and the one rule set |
| `internal/players` | guests (🐼_42), accounts, sessions, merging, the name filter |
| `internal/auth` | checks Telegram's and Google's sign-in proofs (Mini App `initData`, Google ID tokens) |
| `internal/httpapi` | handlers for the generated routes, CORS, the write check, metrics |
| `internal/store` | sqlc output; never edit by hand |
| `data/v0` | v0's panorama pools, the seed of the library |

## Players and accounts

Everyone plays from the first visit as a guest (`POST /v1/session`). Signing in only adds a
way back from another device; nothing is lost:

- **Telegram, through the bot.** The game asks for a one-time code (`/v1/auth/telegram/start`)
  and opens `t.me/<bot>?start=login_<code>` (a link on a phone, a QR code on a laptop). The
  player presses Start; the worker's bot confirms the code; the game, asking every few seconds
  (`/v1/auth/telegram/check`), is signed in. Works on any address, `localhost` included.
- **Telegram Mini App.** Opened inside Telegram, the page has Telegram's signed `initData` and
  signs in by itself (`/v1/auth/telegram/webapp`).
- **Google.** Google's own button gives an ID token, checked against Google's keys
  (`/v1/auth/google`). Off until `GOOGLE_CLIENT_ID` is set; free, no Maps billing.

What a sign-in does: a new identity is added to the current player (a guest becomes an
account). An identity that already has an account: a guest moves into it with its games and
this device's session; an account gets a merge offer that the player confirms
(`/v1/auth/merge`), after which everything of the other account (games, identities, its
devices' sessions) belongs to this one. The last identity cannot be unlinked.

Names: 2–24 characters, checked by the name filter (`internal/players/names.go`: rude words in
Uzbek, Russian and English in either script or with digits for letters, and staff-like names).
A player can report another one from their page; `shafaq-tools reports` lists the reports,
`shafaq-tools ban <id|name> [reason]` stops that player from starting games (the ban follows
merges), `unban` lifts it.

The bot (`TELEGRAM_BOT_TOKEN`, by default the alert bot) also greets players who write to it
and answers `/id` with the chat's IDs, which is how the owner finds `TELEGRAM_ALERT_CHAT`. Only
one program may read a bot's messages: the laptop and the server must not share a bot token.

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
while the worker runs and send `/id`, in a private chat or a group, and it answers with the ID):

- **Ours**: every round handed out (`provider_usage`, the `shafaq_panorama_views_today` metric).
- **Yandex's own**, which is the one that decides a block (free terms §4.3): the worker reads it
  every 10 minutes from the Developer's Account API when `YANDEX_CABINET_KEY` is set
  (developer.tech.yandex.ru → Connect API → Developer's Account API; metric
  `shafaq_yandex_requests_today`). It is higher than ours: the browser also calls `locate`,
  and every step in a Move round loads a panorama.

Days run by Moscow time, as Yandex resets its limits. Buy the commercial license before launch.

## Daily caps and the guess map

Every provider can have a daily budget in rounds, `DAILY_CAPS` (`google=160,yandex=900`; the
default `google=160` keeps Google inside its free 5 000 panoramas a month). At its cap a provider
gets no new rounds until midnight, Moscow time:

- a round goes to another provider the game allows, if there is one;
- a game that already started still finishes on its provider (a few rounds over, never a game
  cut in half);
- a new game that only has capped providers is refused with `daily_cap`, and the game says
  "today's panoramas are used up, come back tomorrow".

The worker tells the owner at 80% of a cap and when it is reached (log and Telegram), once a
day each. The cap counts rounds handed out, as `provider_usage` does: for Yandex that is less
than Yandex's own count (see above), so a Yandex cap meant to stay under the free limit should
sit well below 100.

Each round also says which map it must be guessed on, `guessMap`, when the provider's terms
tie its panoramas to its own map: `yandex` (free terms §6.1.1), `google` (Maps terms
§3.2.3(e)). The web guesses on Yandex's map today; on Google day it switches by this field.

## The server

`docker-compose.yml` is the whole server: Caddy (HTTPS) in front of the game's files and the
API; `migrate`, `api`, `worker`; Postgres 17 + PostGIS with pgBackRest, and Valkey; Prometheus,
Loki with Alloy, Grafana and GlitchTip. Its settings and secrets are in `.env` next to it on the
server (`deploy/env.example`). Deploys are by hand, there is no CI: `deploy/deploy.sh` on the
server pulls `main`, rebuilds, migrates and restarts.

- **Metrics:** each binary serves Prometheus metrics on `METRICS_ADDR` (never public); Grafana's
  "Shafaq Geo" dashboard is provisioned from `deploy/grafana/`.
- **Logs:** JSON on stderr (`LOG_FORMAT=json`); Alloy ships every container's lines to Loki.
- **Errors:** every Error log line (a panic, a 5xx, a failed job) goes to GlitchTip when
  `GLITCHTIP_DSN` is set (`internal/platform/errors.go`).
- **Backups:** `deploy/postgres/`: WAL archived continuously, full backup on Sundays,
  differential on other nights, encrypted, to S3-compatible storage (`BACKUP_*`); every Sunday
  `restore-test.sh` restores the backup beside the live database and compares them. By hand:
  `docker compose exec -u postgres db restore-test.sh`.
- Health: `/v1/health`; readiness (database and Valkey): `/v1/ready`.

Data: places, streets and metro © OpenStreetMap contributors (ODbL).
