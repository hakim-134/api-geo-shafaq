# api-geo-shafaq

The live-rooms API for [geo-shafaq](https://github.com/hakim-134/geo-shafaq), a GeoGuessr-style
game for Uzbekistan. It keeps each room in memory (players, timer, scores) and talks to the
browsers over a WebSocket. It serves no pages: the game itself is the `geo-shafaq` repo.

Continues UzbGuessr, which is based on [BeloGuessr](https://github.com/NotiLo-A/BeloGuessr)
by NotiLo-A.

| Path | What |
|---|---|
| `GET /ws` | WebSocket for a room: create, join, play |
| `GET /api/health` | `{"ok":true,"rooms":true}`; the menu uses it to switch "With friends" on |

## Run

Go 1.22+:

```bash
go run .        # http://localhost:8131
go test ./...
```

Pages may use the API from `https://geo.shafaq.uz` and from `http://localhost:*` /
`http://127.0.0.1:*`. To play from other devices on your network, add the address the
page is opened at: `go run . -origins 'http://10.42.0.1:*'`. The web domain is the
`webOrigin` constant in `main.go`.

## Docker

```bash
docker build -t api-geo-shafaq .
docker run -d --name api-geo-shafaq --restart unless-stopped -p 127.0.0.1:8131:8131 api-geo-shafaq
```

Behind nginx, `/ws` needs the upgrade headers (`Upgrade`, `Connection "upgrade"`,
`proxy_http_version 1.1`).

## How a room works

The host's browser picks each panorama with the same checks as a solo game and sends its
exact position; every player then opens that same panorama. Scores are computed here
(`score.go`, the same formula as the game's `js/game.js`). 2–12 players; after the first
guess the others get 45 seconds. Rooms nobody is connected to are removed after 15
minutes; a restart ends all rooms. Positions reach the browsers, so a player with DevTools
could cheat; it's a game for friends.

| File | What |
|---|---|
| `main.go` | Flags, routes, allowed web origin |
| `origins.go` | Origin allow-list for the WebSocket and CORS |
| `ws.go` | One connection: messages in and out |
| `hub.go` | All rooms, room codes, cleanup |
| `room.go` | One room: players, phases, timer |
| `score.go` | Distance and score |
