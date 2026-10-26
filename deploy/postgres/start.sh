#!/bin/sh
# Starts the backup loop next to Postgres (as the postgres user), then Postgres itself through
# the image's own entrypoint.
set -e
gosu postgres backup.sh &
exec docker-entrypoint.sh "$@"
