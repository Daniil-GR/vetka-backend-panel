#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "${SCRIPT_DIR}/scripts/backup-common.sh"

ARCHIVE_PATH=""
ASSUME_YES="false"
DB_ONLY="false"
CONFIG_ONLY="false"
SKIP_PRE_RESTORE_BACKUP="false"
VERIFY_ONLY="false"
QUIET="${QUIET:-false}"
TEMP_DIR=""
PAYLOAD_ROOT=""
INTERNAL_HEALTH_STATUS="not checked"
PUBLIC_HEALTH_STATUS="not checked"

cleanup() {
  if [[ -n "${TEMP_DIR:-}" && -d "${TEMP_DIR:-}" ]]; then
    rm -rf "$TEMP_DIR"
  fi
  vetka_release_maintenance_lock
}
trap cleanup EXIT

usage() {
  cat <<'EOF'
Usage:
  ./restore.sh --archive /path/to/backup.tar.gz [--yes] [--db-only] [--config-only] [--skip-pre-restore-backup] [--verify-only]
EOF
}

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --archive)
        ARCHIVE_PATH="${2:-}"
        shift 2
        ;;
      --yes)
        ASSUME_YES="true"
        shift
        ;;
      --db-only)
        DB_ONLY="true"
        shift
        ;;
      --config-only)
        CONFIG_ONLY="true"
        shift
        ;;
      --skip-pre-restore-backup)
        SKIP_PRE_RESTORE_BACKUP="true"
        shift
        ;;
      --verify-only)
        VERIFY_ONLY="true"
        shift
        ;;
      --quiet)
        QUIET="true"
        shift
        ;;
      --help|-h)
        usage
        exit 0
        ;;
      *)
        vetka_die "unknown argument: $1"
        ;;
    esac
  done
}

assert_mode_flags() {
  if [[ "$DB_ONLY" == "true" && "$CONFIG_ONLY" == "true" ]]; then
    vetka_die "--db-only and --config-only cannot be used together"
  fi
}

extract_archive() {
  TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/vetka-restore.XXXXXXXX")"
  chmod 700 "$TEMP_DIR"
  vetka_prepare_archive_extract "$ARCHIVE_PATH" "$TEMP_DIR"
  PAYLOAD_ROOT="$(vetka_find_payload_root "$TEMP_DIR")"
}

compare_postgres_major() {
  local payload_root="$1"
  local backup_version backup_major current_major
  backup_version="$(jq -r '.postgres_version // "unknown"' "${payload_root}/metadata.json")"
  backup_major="$(vetka_postgres_major_from_version "$backup_version")"
  current_major="$(vetka_expected_postgres_major)"
  if [[ "$backup_major" != "unknown" && "$current_major" != "unknown" && "$backup_major" != "$current_major" ]]; then
    vetka_die "backup PostgreSQL major version ${backup_major} does not match current compose major version ${current_major}"
  fi
}

backup_existing_state_if_needed() {
  if [[ "$SKIP_PRE_RESTORE_BACKUP" == "true" ]]; then
    vetka_warn "Skipping pre-restore backup by explicit request"
    return
  fi
  if [[ ! -f "${VETKA_PROJECT_ROOT}/.env" ]]; then
    return
  fi
  vetka_log "Creating pre-restore safety backup"
  VETKA_MAINTENANCE_LOCK_HELD=true bash "${VETKA_PROJECT_ROOT}/backup.sh" create --quiet >/dev/null || vetka_die "pre-restore backup failed; rerun with --skip-pre-restore-backup only if you are sure"
}

backup_current_file_if_present() {
  local target="$1"
  if [[ -f "$target" ]]; then
    cp -p "$target" "${target}.before-restore-$(date -u +%Y%m%dT%H%M%SZ)"
  fi
}

restore_config_files() {
  local payload_root="$1"
  local config_dir="${payload_root}/config"

  if [[ ! -d "$config_dir" ]]; then
    vetka_warn "archive has no config directory"
    return
  fi

  if [[ -f "${config_dir}/.env" ]]; then
    backup_current_file_if_present "${VETKA_PROJECT_ROOT}/.env"
    cp "${config_dir}/.env" "${VETKA_PROJECT_ROOT}/.env"
    chmod 600 "${VETKA_PROJECT_ROOT}/.env"
  fi
  if [[ -f "${config_dir}/Caddyfile" ]]; then
    backup_current_file_if_present "${VETKA_PROJECT_ROOT}/Caddyfile"
    cp "${config_dir}/Caddyfile" "${VETKA_PROJECT_ROOT}/Caddyfile"
  fi
  if [[ -f "${config_dir}/docker-compose.override.yml" ]]; then
    backup_current_file_if_present "${VETKA_PROJECT_ROOT}/docker-compose.override.yml"
    cp "${config_dir}/docker-compose.override.yml" "${VETKA_PROJECT_ROOT}/docker-compose.override.yml"
  fi
}

restore_database() {
  local payload_root="$1"
  local dump_file="${payload_root}/database.dump"

  vetka_load_env_file "${VETKA_PROJECT_ROOT}/.env" overwrite
  vetka_apply_backup_defaults
  vetka_require_commands docker curl

  vetka_compose stop backend caddy >/dev/null 2>&1 || true
  vetka_compose up -d postgres >/dev/null
  vetka_wait_for_postgres || vetka_die "postgres did not become healthy"

  vetka_compose exec -T postgres psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d postgres --set=role_name="$POSTGRES_USER" --set=role_password="$POSTGRES_PASSWORD" --set=db_name="$POSTGRES_DB" <<'SQL'
SELECT format('ALTER ROLE %I WITH PASSWORD %L', :'role_name', :'role_password') \gexec
SELECT pg_terminate_backend(pid)
FROM pg_stat_activity
WHERE datname = :'db_name'
  AND pid <> pg_backend_pid();
SELECT format('DROP DATABASE IF EXISTS %I', :'db_name') \gexec
SELECT format('CREATE DATABASE %I TEMPLATE template0', :'db_name') \gexec
SQL

  vetka_compose exec -T postgres sh -lc 'tmp="$(mktemp)"; trap "rm -f \"$tmp\"" EXIT; cat >"$tmp"; pg_restore --no-owner --no-acl --exit-on-error --single-transaction -U "$POSTGRES_USER" -d "$POSTGRES_DB" "$tmp"' < "$dump_file"
}

run_post_restore_startup() {
  local run_migrations="${1:-true}"
  vetka_load_env_file "${VETKA_PROJECT_ROOT}/.env" overwrite
  vetka_apply_backup_defaults
  if [[ "$run_migrations" == "true" ]]; then
    vetka_compose run --rm backend --migrate-up >/dev/null
  fi
  vetka_compose_up_stack >/dev/null
  vetka_wait_for_postgres || vetka_die "postgres did not become healthy after restore"
  vetka_wait_for_backend_internal_ready || vetka_die "internal backend /ready check failed"
  INTERNAL_HEALTH_STATUS="OK"

  local health_url
  health_url="$(vetka_public_ready_url)"
  if vetka_wait_for_public_ready "$health_url"; then
    PUBLIC_HEALTH_STATUS="OK"
  else
    PUBLIC_HEALTH_STATUS="warning"
    vetka_warn "public backend /ready check did not succeed yet: $health_url"
  fi
  vetka_compose ps
}

verify_only_mode() {
  vetka_log "Archive verification completed: $ARCHIVE_PATH"
}

main() {
  parse_args "$@"
  assert_mode_flags
  [[ -n "$ARCHIVE_PATH" ]] || vetka_die "--archive is required"
  [[ -f "$ARCHIVE_PATH" ]] || vetka_die "archive not found: $ARCHIVE_PATH"

  vetka_load_env_file "${VETKA_PROJECT_ROOT}/.env" preserve-existing
  vetka_apply_backup_defaults
  vetka_require_commands jq tar sha256sum realpath mktemp

  local payload_root backup_commit current_commit
  extract_archive
  payload_root="$PAYLOAD_ROOT"
  vetka_verify_extracted_payload_full "$payload_root"
  compare_postgres_major "$payload_root"

  backup_commit="$(jq -r '.git_commit // "unknown"' "${payload_root}/metadata.json")"
  current_commit="$(vetka_git_commit)"
  if [[ "$backup_commit" != "unknown" && "$current_commit" != "unknown" && "$backup_commit" != "$current_commit" ]]; then
    vetka_warn "backup git commit (${backup_commit}) differs from current repository commit (${current_commit})"
  fi

  if [[ "$VERIFY_ONLY" == "true" ]]; then
    verify_only_mode "$payload_root"
    exit 0
  fi

  if ! vetka_acquire_maintenance_lock try; then
    vetka_die "maintenance operation already running"
  fi

  if [[ "$ASSUME_YES" != "true" ]]; then
    vetka_prompt_restore_confirmation || vetka_die "restore cancelled"
  fi

  backup_existing_state_if_needed

  if [[ "$DB_ONLY" != "true" ]]; then
    restore_config_files "$payload_root"
    vetka_load_env_file "${VETKA_PROJECT_ROOT}/.env" overwrite
    vetka_apply_backup_defaults
    BACKUP_DIR="$(vetka_assert_safe_backup_dir "$BACKUP_DIR")"
    vetka_ensure_dir_mode "$BACKUP_DIR" 700
  fi

  if [[ "$CONFIG_ONLY" != "true" ]]; then
    vetka_require_commands docker curl
    restore_database "$payload_root"
  fi

  if [[ "$CONFIG_ONLY" == "true" ]]; then
    run_post_restore_startup false
  else
    run_post_restore_startup true
  fi

  if [[ "$DB_ONLY" != "true" ]] && vetka_command_exists systemctl; then
    if [[ "${BACKUP_ENABLED}" == "true" ]]; then
      vetka_install_backup_units "$VETKA_PROJECT_ROOT" "$BACKUP_DIR" "$BACKUP_ON_CALENDAR"
    else
      vetka_remove_backup_units
    fi
  fi

  vetka_log "Restore completed successfully."
  vetka_log "Internal backend health: ${INTERNAL_HEALTH_STATUS}"
  vetka_log "Public URL health: ${PUBLIC_HEALTH_STATUS}"
  vetka_log "Reminder: allow the new Backend IP in backendAllowedIps and UFW on every Node Agent before running node sync."
}

main "$@"
