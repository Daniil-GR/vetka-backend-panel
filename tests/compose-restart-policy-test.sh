#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck disable=SC1091
source "${ROOT_DIR}/scripts/compose-runtime-common.sh"
REAL_DOCKER_BIN="$(command -v docker || true)"

TMP_FILES=()
cleanup() {
  local file
  for file in "${TMP_FILES[@]:-}"; do
    [[ -n "$file" && -e "$file" ]] && rm -f -- "$file"
  done
}
trap cleanup EXIT

assert_contains() {
  local haystack="$1"
  local needle="$2"
  if [[ "$haystack" != *"$needle"* ]]; then
    echo "ASSERTION FAILED: expected to find '$needle'" >&2
    echo "$haystack" >&2
    exit 1
  fi
}

assert_file_contains() {
  local file="$1"
  local needle="$2"
  if ! grep -Fq -- "$needle" "$file"; then
    echo "ASSERTION FAILED: expected '$needle' in $file" >&2
    cat "$file" >&2
    exit 1
  fi
}

assert_success() {
  if ! "$@"; then
    echo "ASSERTION FAILED: command was expected to succeed: $*" >&2
    exit 1
  fi
}

assert_failure() {
  if "$@"; then
    echo "ASSERTION FAILED: command was expected to fail: $*" >&2
    exit 1
  fi
}

require_restart_policy() {
  local config_output="$1"
  local service_name="$2"
  local service_block

  service_block="$(
    awk -v service="$service_name" '
      $0 == "  " service ":" { capture=1; next }
      capture && $0 ~ /^  [A-Za-z0-9_-]+:/ { capture=0 }
      capture { print }
    ' <<<"$config_output"
  )"

  if [[ -z "$service_block" ]]; then
    echo "Service block not found in rendered compose config: $service_name" >&2
    exit 1
  fi

  if ! grep -q '^    restart: unless-stopped$' <<<"$service_block"; then
    echo "restart policy missing for service: $service_name" >&2
    echo "$service_block" >&2
    exit 1
  fi
}

run_config_regression_checks() {
  if [[ -z "$REAL_DOCKER_BIN" ]]; then
    echo "SKIP: docker is not available in PATH for compose config checks"
    return 0
  fi

  if ! "$REAL_DOCKER_BIN" compose version >/dev/null 2>&1; then
    echo "SKIP: docker compose plugin is not available for compose config checks"
    return 0
  fi

  local config_default config_https
  config_default="$("$REAL_DOCKER_BIN" compose config)"
  require_restart_policy "$config_default" postgres
  require_restart_policy "$config_default" backend

  config_https="$("$REAL_DOCKER_BIN" compose --profile https config)"
  require_restart_policy "$config_https" postgres
  require_restart_policy "$config_https" backend
  require_restart_policy "$config_https" caddy
}

new_mock_log() {
  MOCK_CALL_LOG="$(mktemp "${TMPDIR:-/tmp}/vetka-compose-test-XXXXXX.log")"
  TMP_FILES+=("$MOCK_CALL_LOG")
  export MOCK_CALL_LOG
}

reset_mock_state() {
  new_mock_log
  MOCK_PG_ISREADY="ok"
  MOCK_PS_default_postgres=""
  MOCK_PS_default_backend=""
  MOCK_PS_default_caddy=""
  MOCK_PS_https_postgres=""
  MOCK_PS_https_backend=""
  MOCK_PS_https_caddy=""
  MOCK_INSPECT_postgres_id=""
  MOCK_INSPECT_backend_id=""
  MOCK_INSPECT_caddy_id=""
}

mock_value() {
  local name="$1"
  printf '%s' "${!name-}"
}

log_mock_call() {
  printf '%s\n' "$*" >>"$MOCK_CALL_LOG"
}

docker() {
  if [[ "${1:-}" == "compose" ]]; then
    shift
    local profile="default"
    if [[ "${1:-}" == "--profile" ]]; then
      profile="$2"
      shift 2
    fi
    local subcommand="${1:-}"
    shift || true
    log_mock_call "compose|${profile}|${subcommand}|$*"

    case "$subcommand" in
      ps)
        if [[ "${1:-}" == "--all" && "${2:-}" == "--quiet" ]]; then
          local service="${3:-}"
          mock_value "MOCK_PS_${profile}_${service}"
          return 0
        fi
        return 0
        ;;
      logs)
        return 0
        ;;
      exec)
        if [[ "${1:-}" == "-T" && "${2:-}" == "postgres" && "${3:-}" == "pg_isready" ]]; then
          [[ "${MOCK_PG_ISREADY}" == "ok" ]]
          return
        fi
        echo "unexpected docker compose exec invocation: compose $subcommand $*" >&2
        return 1
        ;;
      *)
        echo "unexpected docker compose invocation: compose $subcommand $*" >&2
        return 1
        ;;
    esac
  fi

  if [[ "${1:-}" == "inspect" && "${2:-}" == "--format" ]]; then
    local format_string="${3:-}"
    local container_id="${4:-}"
    log_mock_call "inspect|${container_id}|${format_string}"
    if [[ -z "$container_id" ]]; then
      echo "mock docker inspect missing container ID" >&2
      return 1
    fi
    local inspect_value
    inspect_value="$(mock_value "MOCK_INSPECT_${container_id}")"
    if [[ -z "$inspect_value" ]]; then
      echo "mock docker inspect unknown container ID: $container_id" >&2
      return 1
    fi
    printf '%s' "$inspect_value"
    return 0
  fi

  echo "unexpected docker invocation: $*" >&2
  return 1
}

run_runtime_helper_unit_tests() {
  APP_DIR="${ROOT_DIR}"

  reset_mock_state
  ENABLE_HTTPS="no"
  MOCK_PS_default_postgres="postgres_id"
  MOCK_PS_default_backend="backend_id"
  MOCK_INSPECT_postgres_id="/postgres|unless-stopped|running"
  MOCK_INSPECT_backend_id="/backend|unless-stopped|running"
  assert_success verify_compose_runtime
  assert_success verify_postgres_ready

  reset_mock_state
  ENABLE_HTTPS="yes"
  MOCK_PS_https_postgres="postgres_id"
  MOCK_PS_https_backend="backend_id"
  MOCK_PS_https_caddy="caddy_id"
  MOCK_INSPECT_postgres_id="/postgres|unless-stopped|running"
  MOCK_INSPECT_backend_id="/backend|unless-stopped|running"
  MOCK_INSPECT_caddy_id="/caddy|unless-stopped|running"
  assert_success verify_compose_runtime
  assert_success verify_postgres_ready
  assert_file_contains "$MOCK_CALL_LOG" "compose|https|ps|--all --quiet caddy"
  assert_file_contains "$MOCK_CALL_LOG" "compose|https|exec|-T postgres pg_isready -U vetka -d vetka_backend"

  reset_mock_state
  ENABLE_HTTPS="no"
  MOCK_PS_default_postgres="postgres_id"
  MOCK_INSPECT_postgres_id="/postgres|unless-stopped|running"
  assert_failure verify_compose_runtime

  reset_mock_state
  ENABLE_HTTPS="no"
  MOCK_PS_default_postgres="postgres_id"
  MOCK_PS_default_backend="backend_id"
  MOCK_INSPECT_postgres_id="/postgres|unless-stopped|running"
  MOCK_INSPECT_backend_id="/backend|unless-stopped|exited"
  assert_failure verify_compose_runtime
  assert_file_contains "$MOCK_CALL_LOG" "compose|default|logs|--tail=50 backend"

  reset_mock_state
  ENABLE_HTTPS="yes"
  MOCK_PS_https_postgres="postgres_id"
  MOCK_PS_https_backend="backend_id"
  MOCK_INSPECT_postgres_id="/postgres|unless-stopped|running"
  MOCK_INSPECT_backend_id="/backend|unless-stopped|running"
  assert_failure verify_compose_runtime

  reset_mock_state
  ENABLE_HTTPS="no"
  MOCK_PS_default_postgres="postgres_id"
  MOCK_PS_default_backend="backend_id"
  MOCK_INSPECT_postgres_id="/postgres|unless-stopped|running"
  MOCK_INSPECT_backend_id="/backend|unless-stopped|running"
  assert_success verify_compose_runtime

  reset_mock_state
  ENABLE_HTTPS="no"
  MOCK_PS_default_postgres="postgres_id"
  MOCK_PS_default_backend="backend_id"
  MOCK_INSPECT_postgres_id="/postgres|no|running"
  MOCK_INSPECT_backend_id="/backend|unless-stopped|running"
  assert_failure verify_compose_runtime

  reset_mock_state
  ENABLE_HTTPS="no"
  MOCK_PS_default_postgres="postgres_id"
  MOCK_PS_default_backend="backend_id"
  MOCK_INSPECT_postgres_id="/postgres|unless-stopped|running"
  MOCK_INSPECT_backend_id="/backend|always|running"
  assert_failure verify_compose_runtime

  reset_mock_state
  ENABLE_HTTPS="no"
  MOCK_PS_default_postgres="postgres_id"
  MOCK_PS_default_backend="backend_id"
  MOCK_INSPECT_postgres_id="/postgres|unless-stopped|running"
  MOCK_INSPECT_backend_id="/backend|unless-stopped|running"
  MOCK_PG_ISREADY="fail"
  assert_failure verify_postgres_ready

  reset_mock_state
  ENABLE_HTTPS="no"
  MOCK_PS_default_postgres=$'postgres_id\nanother_postgres_id'
  MOCK_PS_default_backend="backend_id"
  MOCK_INSPECT_backend_id="/backend|unless-stopped|running"
  assert_failure verify_compose_runtime

  reset_mock_state
  ENABLE_HTTPS="no"
  MOCK_PS_default_postgres="postgres_id"
  MOCK_PS_default_backend="backend_id"
  MOCK_INSPECT_postgres_id="/postgres|unless-stopped|running"
  assert_failure verify_compose_runtime

  reset_mock_state
  ENABLE_HTTPS="no"
  MOCK_PS_default_postgres="postgres_id"
  MOCK_PS_default_backend="unknown_id"
  MOCK_INSPECT_postgres_id="/postgres|unless-stopped|running"
  assert_failure verify_compose_runtime
}

cd "$ROOT_DIR"
run_config_regression_checks
run_runtime_helper_unit_tests
echo "OK: compose restart policy config and runtime helper regressions passed"
