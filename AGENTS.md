# AGENTS.md — for Codex working on api-geo-shafaq (the server)

Codex reads this file on every run in this repo. Claude Code leads the work and sends you
bounded tasks with a short brief; this file is the context the brief leaves out. The brief
wins where it says something more specific.

<!-- The shared part below is the same in geo-shafaq/AGENTS.md. Change both together. -->

## The project

GeoShafaq (folders say "Shafaq Geo"): you land on a street in Uzbekistan, look around in a
Yandex panorama, and guess on the map where you are. Alone, or live with friends. Mostly
played **on phones**. The owner is the CEO and designer: they don't read the code, they play
the game and judge it. Fewer than 10 players today, all friends; built to grow to thousands.

- `geo-shafaq`: the game, a static SvelteKit + TypeScript app (Svelte 5 runes).
- `api-geo-shafaq`: the server, Go + Postgres/PostGIS + Redis; the contract between them is
  `api-geo-shafaq/api/openapi.yaml`.
- `_geoshafaq_claude`: the knowledge base (Claude's). Read it, never change it.

All three sit next to each other in `~/131/github.com/hakim-134/`. You may read any of them.

## Hard rules (breaking one by accident is the usual failure)

1. **No git writes.** No `git add`, `commit`, `push`, `stash`, `reset`, `checkout`, no new
   branches. Read-only git (`status`, `diff`, `log`, `show`) is fine. Claude commits.
2. **Keys never go into git or into tracked files.** The repos are public. Keys live in
   gitignored files only (`.env.local`, `config.js`, `.envrc`). Never print one in code,
   tests, fixtures or comments.
3. **Players see Yandex only**: Yandex panoramas and Yandex's map. No other map, tile or
   provider anywhere a player looks. The drawn `demo` panoramas are for development only and
   play only where `TEST_PROVIDERS` enables them.
4. **`KADASTR:`**: everything that exists because of the cadastre's mahalla borders carries
   a `KADASTR:` comment, so it can all be removed in one go. Keep that when you touch it.
5. **No Docker, no containers, no databases of your own.** Claude runs Docker and the
   integration tests.
6. **Stay inside the brief.** Touch only the files it names or clearly needs. Don't touch
   `_v0/` (the old version, kept as history), generated files by hand, or the OpenAPI
   contract unless the brief says so.
7. **Remove before adding.** When a new thing replaces an old one, the old one goes in the
   same change: its code, its text, its styles, its API field. No dead code, no leftovers.
8. **Domains** (`geo.shafaq.uz`, `api.geo.shafaq.uz`) live in config only; don't spread them.

## Who plays, and what the game must feel like

People of Uzbekistan, most of them Muslims: honest, simple people. The game never mentions
religion and stays **decent and clean**: no gambling, nothing vulgar, no tricks, no pressure
to keep playing, real numbers only.

- **Uzbek first** (Latin script), then Russian and English. Every new player-visible text in
  all three. Write `o‘` and `g‘` with U+2018 (‘), never `'` or `’`.
- **A game, not a form.** Clear, quick, fun. One screen, one job, one main button.
- **Show, don't type.** A place is shown by its face: the parent's whole shape with the place
  filled in (place-in-parent), never a sign or a banner.
- **One map, one behaviour.** A tap on a map zooms in, in place; a map never leads to
  another page with another map.

## How to finish a task

- Run the checks the brief names, and the repo's own checks below. If one can't run in your
  sandbox (no network, no sockets), say which and why; don't pretend it passed.
- Reply short, in this order: **what changed** (files, one line each) · **checks** (command →
  result) · **left or doubtful** (anything you skipped, guessed or would ask the owner).
- If the brief is wrong or the task is harder than it looks, say so and do the closest
  sound thing. Don't fake it, don't silently widen it.

<!-- End of the shared part. -->

## This repo: api-geo-shafaq

- **Read first**: `README.md` (layout, how a round works, rooms, places, Barg, Yandex usage).
- **The contract** `api/openapi.yaml` is the one source of truth with the game. Change it only
  when the brief says so; then `make gen` (oapi-codegen + sqlc). Generated code
  (`internal/store` is sqlc output) is never edited by hand. SQL lives in `queries/` and
  `migrations/`; a new migration is a new file, an applied one is never edited.
- **Go**: inside your sandbox `go` is not on PATH; use `/snap/go/current/bin/go`.
- **Checks**: `go build ./...`, `go vet ./...`, `gofmt -l .` (empty, `_v0/` aside) and
  `go test ./...` (the unit tests). Your sandbox can't open sockets, so the integration tests
  (they need Postgres) don't run there: write them when the brief asks, say they are
  unrun, and Claude runs `make test` with the database after you.
- **Risky ground**: scoring (`internal/score`, `internal/rules`), Barg (the in-game currency:
  a player must never earn twice), live rooms (`internal/room`, state in Valkey), and
  migrations. Change these with care and name the edge cases in your reply.
- **Yandex quota**: the free key allows ~100 panorama views a day. Nothing you write may call
  Yandex in a loop, in tests, or on start-up.
