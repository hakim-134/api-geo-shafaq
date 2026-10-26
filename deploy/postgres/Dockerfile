# The server's database: PostGIS 17 with pgBackRest. Postgres archives every WAL segment to the
# backup repository as it fills, and backup.sh, started next to Postgres, takes the nightly
# backups and tests a restore every week. See docker-compose.yml (db) and README.md (Backups).
FROM postgis/postgis:17-3.6

RUN apt-get update \
    && apt-get install -y --no-install-recommends pgbackrest curl ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /var/lib/pgbackrest /var/log/pgbackrest /tmp/pgbackrest \
    && chown -R postgres:postgres /var/lib/pgbackrest /var/log/pgbackrest /tmp/pgbackrest

COPY pgbackrest.conf /etc/pgbackrest/pgbackrest.conf
# next to the image's own 10_postgis.sh, which runs first
COPY initdb/ /docker-entrypoint-initdb.d/
COPY backup.sh restore-test.sh start.sh /usr/local/bin/

ENTRYPOINT ["start.sh"]
CMD ["postgres", \
     "-c", "archive_mode=on", \
     "-c", "archive_command=pgbackrest --stanza=shafaq archive-push %p", \
     "-c", "archive_timeout=300"]
