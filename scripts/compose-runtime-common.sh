#!/usr/bin/env bash

compose_profile_args() {
  COMPOSE_ARGS=(docker compose)
  if [[ "${ENABLE_HTTPS:-no}" == "yes" ]]; then
    COMPOSE_ARGS+=(--profile https)
  fi
}

expected_compose_services() {
  EXPECTED_SERVICES=(postgres backend)
  if [[ "${ENABLE_HTTPS:-no}" == "yes" ]]; then
    EXPECTED_SERVICES+=(caddy)
  fi
}

print_service_logs_for_debug() {
  local service="$1"
  if ! (cd "$APP_DIR" && "${COMPOSE_ARGS[@]}" logs --tail=50 "$service") >&2; then
    echo "WARNING: failed to collect recent logs for service '$service'." >&2
  fi
}

verify_compose_runtime() {
  local service
  local -a container_ids=()
  local inspect_output container_name restart_policy runtime_status

  compose_profile_args
  expected_compose_services

  for service in "${EXPECTED_SERVICES[@]}"; do
    mapfile -t container_ids < <(cd "$APP_DIR" && "${COMPOSE_ARGS[@]}" ps --all --quiet "$service")

    if [[ "${#container_ids[@]}" -eq 0 ]]; then
      echo "ERROR: expected compose service '$service' has no container." >&2
      return 1
    fi

    if [[ "${#container_ids[@]}" -ne 1 ]]; then
      echo "ERROR: expected exactly one compose container for service '$service', found ${#container_ids[@]}." >&2
      printf 'Container IDs: %s\n' "${container_ids[*]}" >&2
      return 1
    fi

    if ! inspect_output="$(docker inspect --format '{{.Name}}|{{.HostConfig.RestartPolicy.Name}}|{{.State.Status}}' "${container_ids[0]}")"; then
      echo "ERROR: failed to inspect compose service '$service' container '${container_ids[0]}'." >&2
      return 1
    fi
    IFS='|' read -r container_name restart_policy runtime_status <<<"$inspect_output"

    if [[ "$restart_policy" != "unless-stopped" ]]; then
      echo "ERROR: compose service '$service' container '${container_name#/}' restart policy is '$restart_policy', expected 'unless-stopped'." >&2
      return 1
    fi

    if [[ "$runtime_status" != "running" ]]; then
      echo "ERROR: compose service '$service' container '${container_name#/}' is not running: status=$runtime_status." >&2
      print_service_logs_for_debug "$service"
      return 1
    fi
  done
}

verify_postgres_ready() {
  compose_profile_args
  if ! (cd "$APP_DIR" && "${COMPOSE_ARGS[@]}" exec -T postgres pg_isready -U vetka -d vetka_backend); then
    echo "ERROR: PostgreSQL readiness check failed." >&2
    return 1
  fi
}

show_compose_status() {
  compose_profile_args
  (cd "$APP_DIR" && "${COMPOSE_ARGS[@]}" ps)
}
