#!/bin/sh
# The backup loop, next to Postgres in the db container. Postgres itself archives every WAL
# segment (archive_command), so a restore can reach any moment; this takes the base backups:
# a full one on Sundays, a differential one every other night, at BACKUP_HOUR (UTC). After
# the Sunday backup it restores it into a scratch directory and checks it (restore-test.sh).
#
# A failure goes to the log (Loki) and to the owner on Telegram; a tested restore too, once a
# week, so silence never means "fine" for long.
set -u
STANZA=shafaq
HOUR=${BACKUP_HOUR:-1}

say() { echo "backup: $*"; }

tell() {
	[ -n "${TELEGRAM_ALERT_TOKEN:-}" ] && [ -n "${TELEGRAM_ALERT_CHAT:-}" ] || return 0
	curl -fsS -m 20 "https://api.telegram.org/bot${TELEGRAM_ALERT_TOKEN}/sendMessage" \
		--data-urlencode "chat_id=${TELEGRAM_ALERT_CHAT}" \
		--data-urlencode "text=Shafaq Geo (${ENVIRONMENT:-server}): $*" >/dev/null || say "telegram failed"
}

fail() {
	say "FAILED: $*"
	tell "⚠️ backup failed: $*"
}

backup() {
	say "$1 backup starting"
	if pgbackrest --stanza=$STANZA --type="$1" backup; then
		say "$1 backup done"
	else
		fail "the $1 backup"
		return 1
	fi
}

until pg_isready -q -h /var/run/postgresql -U "$POSTGRES_USER"; do sleep 5; done
sleep 5 # the image's first start restarts Postgres once after its init scripts
until pg_isready -q -h /var/run/postgresql -U "$POSTGRES_USER"; do sleep 5; done

pgbackrest --stanza=$STANZA stanza-create || fail "stanza-create (is the repository reachable?)"
pgbackrest --stanza=$STANZA check || fail "check: WAL archiving does not reach the repository"
# a new server gets its first full backup at once, not on Sunday
if pgbackrest --stanza=$STANZA --output=json info | grep -q '"backup":\[\]'; then
	backup full
fi

while true; do
	now=$(date -u +%s)
	next=$(date -u -d "today ${HOUR}:00" +%s)
	[ "$next" -gt "$now" ] || next=$(date -u -d "tomorrow ${HOUR}:00" +%s)
	sleep $((next - now))

	if [ "$(date -u +%u)" = 7 ]; then
		if backup full; then
			if out=$(restore-test.sh 2>&1); then
				say "$out"
				tell "✅ weekly backup restored and checked: $(echo "$out" | tail -1)"
			else
				echo "$out"
				fail "the weekly restore test: $(echo "$out" | tail -1)"
			fi
		fi
	else
		backup diff
	fi
done
