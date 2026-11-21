#!/usr/bin/env bash
#
# Runs the driver integration tests against real services. For each module
# with a docker-compose.yml, it starts the services, runs the module's tests
# once per entry in the table below with that entry's environment, stops the
# services, and removes their volumes. It fails if any test run fails or if
# any container, network, or volume from these runs is left behind.
#
# Usage:
#   scripts/integration.sh [module...]
#
# Without arguments every module in the table runs. Requires Docker with the
# compose plugin. The URLs match each driver's README and docker-compose.yml.

set -euo pipefail

repository_root=$(cd "$(dirname "$0")/.." && pwd)
cd "$repository_root"
export GOWORK=off

# module|environment for one test run. A module may appear more than once;
# its services start once for all of its runs.
runs=(
  "objex/drivers/s3|OBJEX_S3_ENDPOINT=http://localhost:9000"
  "rdb/drivers/postgres|RDB_POSTGRES_URL=postgres://rdbtest:rdbtest@localhost:55432/rdbtest?sslmode=disable"
  "rdb/drivers/mysql|RDB_MYSQL_URL=mysql://rdbtest:rdbtest@localhost:53306/rdbtest"
  "rdb/drivers/mysql|RDB_MYSQL_URL=mariadb://rdbtest:rdbtest@localhost:53307/rdbtest"
  "settings/drivers/consul|SETTINGS_CONSUL_ADDR=http://localhost:8500"
  "settings/drivers/vault|SETTINGS_VAULT_ADDR=http://localhost:8200"
  "stash/drivers/database|STASH_POSTGRES_URL=postgres://stashtest:stashtest@localhost:55436/stashtest?sslmode=disable STASH_MYSQL_URL=mysql://stashtest:stashtest@localhost:53316/stashtest"
  "stash/drivers/database|STASH_MYSQL_URL=mariadb://stashtest:stashtest@localhost:53317/stashtest"
  "stash/drivers/redis|STASH_REDIS_URL=redis://localhost:56380/0"
  "stash/drivers/valkey|STASH_VALKEY_URL=valkey://localhost:56390/0"
)

project_prefix=bkit-integration

selected() {
  [ "${#filter[@]}" -eq 0 ] && return 0
  local m
  for m in "${filter[@]}"; do
    [ "$m" = "$1" ] && return 0
  done
  return 1
}

leftovers() {
  {
    docker ps -aq --filter "label=com.docker.compose.project" --format '{{.Label "com.docker.compose.project"}}'
    docker network ls --format '{{.Label "com.docker.compose.project"}}'
    docker volume ls --format '{{.Label "com.docker.compose.project"}}'
  } | grep "^$project_prefix-" | LC_ALL=C sort -u || true
}

filter=("$@")
modules=()
for run in "${runs[@]}"; do
  module=${run%%|*}
  selected "$module" || continue
  case " ${modules[*]} " in
    *" $module "*) ;;
    *) modules+=("$module") ;;
  esac
done
if [ "${#filter[@]}" -gt 0 ] && [ "${#modules[@]}" -ne "${#filter[@]}" ]; then
  echo "integration.sh: unknown module in: ${filter[*]}" >&2
  exit 2
fi

failed=()
for module in "${modules[@]}"; do
  project="$project_prefix-$(printf '%s' "$module" | tr '/' '-')"
  compose=(docker compose -p "$project" -f "$module/docker-compose.yml")
  echo "==> $module: starting services"
  if ! "${compose[@]}" up -d --wait --quiet-pull; then
    "${compose[@]}" logs || true
    failed+=("$module (services did not become healthy)")
    "${compose[@]}" down -v --remove-orphans || true
    continue
  fi
  for run in "${runs[@]}"; do
    [ "${run%%|*}" = "$module" ] || continue
    read -r -a env <<<"${run#*|}"
    echo "==> $module: ${env[*]%%=*}"
    if ! (cd "$module" && env "${env[@]}" go test -race -count=1 ./...); then
      failed+=("$module (${run#*|})")
    fi
  done
  echo "==> $module: stopping services"
  "${compose[@]}" down -v --remove-orphans
done

left=$(leftovers)
if [ -n "$left" ]; then
  echo "ERROR: integration runs left Docker resources behind for: $left" >&2
  failed+=("leftover Docker resources")
fi

if [ "${#failed[@]}" -gt 0 ]; then
  echo "Failed integration runs:" >&2
  printf '  %s\n' "${failed[@]}" >&2
  exit 1
fi
echo "All integration runs passed."
