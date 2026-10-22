#!/usr/bin/env bash
# Plays a whole game against a running API with the demo provider, the way the game does,
# and prints each answer. Never uses Yandex.
#
#   scripts/smoke.sh                      # http://localhost:8131
#   API=https://api.geo.shafaq.uz scripts/smoke.sh
set -euo pipefail
API=${API:-http://localhost:8131}
ORIGIN=${ORIGIN:-http://localhost:5173}
MAP=${MAP:-tashkent-streets}
jar=$(mktemp)
trap 'rm -f "$jar"' EXIT

post() { curl -sf -c "$jar" -b "$jar" -X POST -H Content-Type:application/json -H "Origin: $ORIGIN" "$API$1" ${2:+-d "$2"}; echo; }
get() { curl -sf -b "$jar" "$API$1"; echo; }
field() { python3 -c "import json,sys; print(json.load(sys.stdin)$1)"; }

echo "== session";            post /v1/session
rules='{"rounds":3,"timeSec":60,"movement":"move","hints":{"count":2,"per":"round","cost":0.2},"compass":true,"providers":["demo"],"scoreScale":1,"panoLabels":false,"mapLabels":true}'
game=$(post /v1/games "{\"map\":\"$MAP\",\"rules\":$rules}")
id=$(echo "$game" | field "['id']")
echo "== game $id on $MAP"
for n in 1 2 3; do
  echo "== round $n"
  round=$(post "/v1/games/$id/rounds"); echo "$round"
  again=$(post "/v1/games/$id/rounds" | field "['n']")
  [ "$again" = "$n" ] || { echo "asking twice skipped a round"; exit 1; }
  post "/v1/games/$id/rounds/$n/shown" '{}'
  if [ "$n" = 1 ]; then
    post "/v1/games/$id/rounds/$n/hint"
    post "/v1/games/$id/rounds/$n/hint"
    code=$(curl -s -o /dev/null -w '%{http_code}' -b "$jar" -X POST -H "Origin: $ORIGIN" "$API/v1/games/$id/rounds/$n/hint")
    [ "$code" = 409 ] || { echo "a third hint was allowed ($code)"; exit 1; }
  fi
  post "/v1/games/$id/rounds/$n/guess" '{"pos":{"lat":41.311,"lon":69.279}}'
done
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$jar" -X POST -H "Origin: $ORIGIN" "$API/v1/games/$id/rounds")
[ "$code" = 409 ] || { echo "a fourth round was started ($code)"; exit 1; }
echo "== the finished game"
get "/v1/games/$id" | python3 -c "
import json,sys
g=json.load(sys.stdin)
print(g['status'], g['total'], '/', g['max'])
for r in g['rounds']:
    res=r['result']; p=res['place']
    print(' ', r['n'], res['score'], round(res['km'],2), 'km', (p.get('street') or p.get('district') or p.get('city') or {}).get('uz'))"
echo "== me";                 get /v1/me
echo "== history";            get /v1/me/games | python3 -c "import sys; print(sys.stdin.read()[:300])"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$jar" -X POST -H "Origin: https://evil.example" -H Content-Type:application/json "$API/v1/games" -d '{}')
[ "$code" = 403 ] || { echo "a write from another site was allowed ($code)"; exit 1; }
echo "OK"
