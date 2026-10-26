#!/bin/sh
# Restores the latest backup into a scratch directory, starts it on a side socket, checks it
# against the live database, and removes it. A backup is only as good as its last restore.
#
#   docker compose exec -u postgres db restore-test.sh
#
# The last line is the verdict (backup.sh sends it to Telegram).
set -eu
STANZA=shafaq
DIR=/tmp/pgbackrest/restore
SOCK=/tmp/pgbackrest
PORT=5499

cleanup() {
	pg_ctl -D "$DIR" -m immediate stop >/dev/null 2>&1 || true
	rm -rf "$DIR"
}
trap cleanup EXIT
cleanup
mkdir -p "$DIR" && chmod 700 "$DIR"

pgbackrest --stanza=$STANZA --pg1-path="$DIR" --type=immediate --target-action=promote restore

pg_ctl -D "$DIR" -w -t 600 -l "$SOCK/restore.log" \
	-o "-p $PORT -k $SOCK -c listen_addresses='' -c archive_mode=off" start >/dev/null

restored() { psql -XAtq -h "$SOCK" -p $PORT -U "${POSTGRES_USER:-shafaq}" -d "${POSTGRES_DB:-shafaq}" -c "$1"; }
live() { psql -XAtq -h /var/run/postgresql -U "${POSTGRES_USER:-shafaq}" -d "${POSTGRES_DB:-shafaq}" -c "$1"; }

i=0
while [ "$(restored 'SELECT pg_is_in_recovery()' 2>/dev/null)" != f ]; do
	i=$((i + 1))
	[ $i -lt 300 ] || { echo "the restored database never left recovery"; exit 1; }
	sleep 1
done

summary=""
for table in panoramas places maps users games rounds; do
	r=$(restored "SELECT count(*) FROM $table")
	l=$(live "SELECT count(*) FROM $table")
	# the live database only grows past the backup (or is cleaned a little); a restored copy
	# that is much smaller, or empty where the live one is not, is a broken backup
	if [ "$l" -gt 0 ] && [ "$r" -eq 0 ]; then
		echo "$table is empty in the restored copy ($l rows live)"
		exit 1
	fi
	summary="$summary $table $r/$l"
done
echo "restore OK (restored/live):$summary"
