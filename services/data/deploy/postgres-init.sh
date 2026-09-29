#!/bin/sh
# Роли PostgreSQL: владелец схемы для миграций и runtime-роль только с DML.
# Пароли читаются из docker secrets и не выводятся.
set -eu
migrator_password=$(cat /run/secrets/migration_password)
runtime_password=$(cat /run/secrets/data_runtime_password)
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  -v migrator_password="$migrator_password" -v runtime_password="$runtime_password" <<'SQL'
CREATE ROLE maxfleet_migrator LOGIN PASSWORD :'migrator_password' NOSUPERUSER NOCREATEDB NOCREATEROLE;
CREATE ROLE maxfleet_app LOGIN PASSWORD :'runtime_password' NOSUPERUSER NOCREATEDB NOCREATEROLE;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
ALTER SCHEMA public OWNER TO maxfleet_migrator;
GRANT CONNECT ON DATABASE maxfleet TO maxfleet_migrator, maxfleet_app;
GRANT USAGE ON SCHEMA public TO maxfleet_app;
ALTER DEFAULT PRIVILEGES FOR ROLE maxfleet_migrator IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO maxfleet_app;
ALTER DEFAULT PRIVILEGES FOR ROLE maxfleet_migrator IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO maxfleet_app;
SQL
