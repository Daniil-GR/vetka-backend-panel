#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "${SCRIPT_DIR}/scripts/backup-common.sh"

QUIET="${QUIET:-false}"
COMMAND="create"
ARCHIVE_PATH=""
OUTPUT_DIR=""
RETENTION_DAYS_OVERRIDE=""
NO_RETENTION="false"
TEMP_DIR=""

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
  ./backup.sh
  ./backup.sh create [--output-dir PATH] [--retention-days N] [--quiet] [--no-retention]
  ./backup.sh verify /path/to/archive.tar.gz [--quiet]
  ./backup.sh list [--output-dir PATH] [--quiet]
EOF
}

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      create|verify|list)
        COMMAND="$1"
        shift
        ;;
      --output-dir)
        OUTPUT_DIR="${2:-}"
        shift 2
        ;;
      --retention-days)
        RETENTION_DAYS_OVERRIDE="${2:-}"
        shift 2
        ;;
      --quiet)
        QUIET="true"
        shift
        ;;
      --no-retention)
        NO_RETENTION="true"
        shift
        ;;
      --help|-h)
        usage
        exit 0
        ;;
      *)
        if [[ "$COMMAND" == "verify" && -z "$ARCHIVE_PATH" ]]; then
          ARCHIVE_PATH="$1"
          shift
        else
          vetka_die "unknown argument: $1"
        fi
        ;;
    esac
  done
}

prepare_environment() {
  vetka_load_env_file "${VETKA_PROJECT_ROOT}/.env" preserve-existing
  vetka_apply_backup_defaults
  if [[ -n "$OUTPUT_DIR" ]]; then
    BACKUP_DIR="$OUTPUT_DIR"
  fi
  if [[ -n "$RETENTION_DAYS_OVERRIDE" ]]; then
    BACKUP_RETENTION_DAYS="$RETENTION_DAYS_OVERRIDE"
  fi
}

prepare_backup_dir() {
  prepare_environment
  BACKUP_DIR="$(vetka_assert_safe_backup_dir "$BACKUP_DIR")"
}

create_backup() {
  prepare_backup_dir
  vetka_require_commands docker jq tar sha256sum git hostname mktemp stat find curl realpath
  if ! vetka_acquire_maintenance_lock try; then
    vetka_die "maintenance operation already running"
  fi
  vetka_ensure_dir_mode "$BACKUP_DIR" 700
  vetka_compose up -d postgres >/dev/null
  vetka_wait_for_postgres || vetka_die "postgres did not become healthy for backup"

  local timestamp short_sha archive_name archive_path payload_root config_root reference_root postgres_version compose_project
  timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
  short_sha="$(vetka_git_short_commit)"
  archive_name="$(vetka_archive_basename "$timestamp" "$short_sha")"
  archive_path="${BACKUP_DIR}/${archive_name}"

  TEMP_DIR="$(mktemp -d "${BACKUP_DIR}/.tmp.XXXXXXXX")"
  chmod 700 "$TEMP_DIR"
  payload_root="${TEMP_DIR}/${archive_name%.tar.gz}"
  config_root="${payload_root}/config"
  reference_root="${payload_root}/reference"
  vetka_ensure_dir_mode "$payload_root" 700
  vetka_ensure_dir_mode "$config_root" 700
  vetka_ensure_dir_mode "$reference_root" 700

  vetka_log "Creating PostgreSQL dump"
  vetka_compose exec -T postgres sh -lc 'exec pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" --format=custom --no-owner --no-acl' > "${payload_root}/database.dump"
  chmod 600 "${payload_root}/database.dump"
  vetka_pg_restore_list_from_file "${payload_root}/database.dump"

  if [[ -f "${VETKA_PROJECT_ROOT}/.env" ]]; then
    cp "${VETKA_PROJECT_ROOT}/.env" "${config_root}/.env"
    chmod 600 "${config_root}/.env"
  fi
  if [[ -f "${VETKA_PROJECT_ROOT}/Caddyfile" ]]; then
    cp "${VETKA_PROJECT_ROOT}/Caddyfile" "${config_root}/Caddyfile"
    chmod 600 "${config_root}/Caddyfile"
  fi
  if [[ -f "${VETKA_PROJECT_ROOT}/docker-compose.override.yml" ]]; then
    cp "${VETKA_PROJECT_ROOT}/docker-compose.override.yml" "${config_root}/docker-compose.override.yml"
    chmod 600 "${config_root}/docker-compose.override.yml"
  fi
  cp "${VETKA_PROJECT_ROOT}/docker-compose.yml" "${reference_root}/docker-compose.yml"
  chmod 600 "${reference_root}/docker-compose.yml"

  postgres_version="$(vetka_postgres_version)"
  compose_project="$(vetka_compose_project_name)"

  local files_json
  files_json="$(
    cd "$payload_root"
    find . -type f ! -name 'metadata.json' ! -name 'SHA256SUMS' -printf '%P\n' | sort | jq -R . | jq -s .
  )"

  jq -n \
    --arg created_at_utc "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg git_commit "$(vetka_git_commit)" \
    --arg git_branch "$(vetka_git_branch)" \
    --arg hostname "$(hostname -f 2>/dev/null || hostname)" \
    --arg postgres_version "$postgres_version" \
    --arg compose_project "$compose_project" \
    --argjson files "$files_json" \
    '{
      format_version: 1,
      created_at_utc: $created_at_utc,
      git_commit: $git_commit,
      git_branch: $git_branch,
      hostname: $hostname,
      postgres_version: $postgres_version,
      compose_project: $compose_project,
      files: $files
    }' > "${payload_root}/metadata.json"
  chmod 600 "${payload_root}/metadata.json"

  (
    cd "$payload_root"
    find . -type f ! -name 'SHA256SUMS' -printf '%P\n' | sort | xargs sha256sum > SHA256SUMS
  )
  chmod 600 "${payload_root}/SHA256SUMS"

  tar -czf "$archive_path" -C "$TEMP_DIR" "$(basename "$payload_root")"
  chmod 600 "$archive_path"

  vetka_verify_archive_file_full "$archive_path"

  if [[ "$NO_RETENTION" != "true" ]]; then
    vetka_apply_retention "$BACKUP_DIR" "$BACKUP_RETENTION_DAYS"
  fi

  vetka_log "Backup created: $archive_path"
  printf '%s\n' "$archive_path"
}

verify_backup() {
  [[ -n "$ARCHIVE_PATH" ]] || vetka_die "verify requires an archive path"
  [[ -f "$ARCHIVE_PATH" ]] || vetka_die "archive not found: $ARCHIVE_PATH"
  prepare_environment
  vetka_require_commands jq tar sha256sum mktemp
  vetka_verify_archive_file_full "$ARCHIVE_PATH"
  vetka_log "Backup verified: $ARCHIVE_PATH"
}

list_backups() {
  prepare_backup_dir
  vetka_require_commands jq tar sha256sum stat realpath mktemp
  vetka_ensure_dir_mode "$BACKUP_DIR" 700

  shopt -s nullglob
  local archive status size_bytes size_human temp_dir payload_root metadata_entry metadata_file created_at git_commit
  for archive in "${BACKUP_DIR}/${VETKA_BACKUP_PREFIX}-"*.tar.gz; do
    status="ok"
    temp_dir="$(mktemp -d)"
    chmod 700 "$temp_dir"
    if vetka_verify_archive_file_basic "$archive" >/dev/null 2>&1; then
      metadata_entry="$(tar -tzf "$archive" | grep '/metadata.json$' | head -n 1 || true)"
      if [[ -n "$metadata_entry" ]]; then
        metadata_file="${temp_dir}/metadata.json"
        tar -xOf "$archive" "$metadata_entry" > "$metadata_file"
        created_at="$(jq -r '.created_at_utc // "unknown"' "$metadata_file")"
        git_commit="$(jq -r '.git_commit // "unknown"' "$metadata_file")"
      else
        created_at="unknown"
        git_commit="unknown"
      fi
    else
      status="invalid"
      created_at="unknown"
      git_commit="unknown"
    fi
    rm -rf "$temp_dir"
    size_bytes="$(stat -c %s "$archive")"
    size_human="$(vetka_human_size "$size_bytes")"
    printf '%s\t%s\t%s\t%s\t%s\n' "$(basename "$archive")" "$created_at" "$size_human" "$status" "$git_commit"
  done
}

main() {
  parse_args "$@"
  case "$COMMAND" in
    create)
      create_backup
      ;;
    verify)
      verify_backup
      ;;
    list)
      list_backups
      ;;
    *)
      vetka_die "unsupported command: $COMMAND"
      ;;
  esac
}

main "$@"
