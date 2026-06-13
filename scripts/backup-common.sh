#!/usr/bin/env bash
set -Eeuo pipefail

if [[ -n "${BASH_SOURCE[0]:-}" ]]; then
  VETKA_SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
else
  VETKA_SCRIPT_DIR="$(pwd)"
fi
VETKA_PROJECT_ROOT="${VETKA_PROJECT_ROOT:-$(cd -- "${VETKA_SCRIPT_DIR}/.." && pwd)}"
VETKA_BACKUP_PREFIX="vetka-backend-panel"
VETKA_MAINTENANCE_LOCK_FILE="${VETKA_MAINTENANCE_LOCK_FILE:-/run/lock/vetka-backend-panel-maintenance.lock}"
VETKA_MAINTENANCE_LOCK_DIR="${VETKA_MAINTENANCE_LOCK_DIR:-${VETKA_MAINTENANCE_LOCK_FILE}.d}"
VETKA_SYSTEMD_SERVICE_FILE="${VETKA_SYSTEMD_SERVICE_FILE:-/etc/systemd/system/vetka-backend-backup.service}"
VETKA_SYSTEMD_TIMER_FILE="${VETKA_SYSTEMD_TIMER_FILE:-/etc/systemd/system/vetka-backend-backup.timer}"
VETKA_MAINTENANCE_LOCK_HELD="${VETKA_MAINTENANCE_LOCK_HELD:-false}"
VETKA_MAINTENANCE_LOCK_ACQUIRED="false"
VETKA_MAINTENANCE_LOCK_MODE=""

vetka_command_exists() {
  command -v "$1" >/dev/null 2>&1
}

vetka_log() {
  if [[ "${QUIET:-false}" != "true" ]]; then
    printf '[vetka] %s\n' "$*" >&2
  fi
}

vetka_warn() {
  printf '[vetka][warn] %s\n' "$*" >&2
}

vetka_die() {
  printf '[vetka][error] %s\n' "$*" >&2
  exit 1
}

vetka_require_commands() {
  local cmd
  for cmd in "$@"; do
    vetka_command_exists "$cmd" || vetka_die "required command not found: $cmd"
  done
}

vetka_has_control_chars() {
  [[ "$1" == *$'\n'* || "$1" == *$'\r'* || "$1" == *$'\t'* ]]
}

vetka_strip_outer_quotes() {
  local value="$1"
  value="${value#"${value%%[![:space:]]*}"}"
  if [[ "$value" =~ ^\"(.*)\"[[:space:]]*(#.*)?$ ]]; then
    printf '%s' "${BASH_REMATCH[1]}"
    return
  fi
  if [[ "$value" =~ ^\'(.*)\'[[:space:]]*(#.*)?$ ]]; then
    printf '%s' "${BASH_REMATCH[1]}"
    return
  fi
  value="${value%%$'\r'}"
  value="${value%"${value##*[![:space:]]}"}"
  if [[ "$value" =~ ^(.*[^[:space:]])[[:space:]]+#.*$ ]]; then
    value="${BASH_REMATCH[1]}"
  fi
  printf '%s' "$value"
}

vetka_is_allowed_env_key() {
  case "$1" in
    BACKUP_ENABLED|BACKUP_DIR|BACKUP_RETENTION_DAYS|BACKUP_ON_CALENDAR|BACKUP_BEFORE_UPDATE|ENABLE_HTTPS|POSTGRES_USER|POSTGRES_PASSWORD|POSTGRES_DB|PANEL_PUBLIC_BASE_URL|HTTP_ADDR|COMPOSE_PROJECT_NAME)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

vetka_allowed_env_keys() {
  printf '%s\n' \
    BACKUP_ENABLED \
    BACKUP_DIR \
    BACKUP_RETENTION_DAYS \
    BACKUP_ON_CALENDAR \
    BACKUP_BEFORE_UPDATE \
    ENABLE_HTTPS \
    POSTGRES_USER \
    POSTGRES_PASSWORD \
    POSTGRES_DB \
    PANEL_PUBLIC_BASE_URL \
    HTTP_ADDR \
    COMPOSE_PROJECT_NAME
}

vetka_clear_allowed_env_keys() {
  local key
  while IFS= read -r key; do
    unset "$key"
  done < <(vetka_allowed_env_keys)
}

vetka_load_env_file() {
  local env_file="$1"
  local mode="${2:-preserve-existing}"
  local line key raw_value value
  [[ -f "$env_file" ]] || return 0
  [[ "$mode" == "preserve-existing" || "$mode" == "overwrite" ]] || vetka_die "unsupported env load mode: $mode"
  if [[ "$mode" == "overwrite" ]]; then
    vetka_clear_allowed_env_keys
  fi

  while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line%$'\r'}"
    [[ "$line" =~ ^[[:space:]]*$ ]] && continue
    [[ "$line" =~ ^[[:space:]]*# ]] && continue
    [[ "$line" =~ ^[[:space:]]*export[[:space:]]+ ]] && line="${line#export }"
    if [[ ! "$line" =~ ^[[:space:]]*([A-Za-z_][A-Za-z0-9_]*)[[:space:]]*=(.*)$ ]]; then
      continue
    fi

    key="${BASH_REMATCH[1]}"
    raw_value="${BASH_REMATCH[2]}"
    vetka_is_allowed_env_key "$key" || continue
    if [[ "$mode" == "preserve-existing" && -n "${!key+x}" ]]; then
      continue
    fi
    value="$(vetka_strip_outer_quotes "$raw_value")"
    printf -v "$key" '%s' "$value"
    export "$key"
  done < "$env_file"
}

vetka_apply_backup_defaults() {
  BACKUP_ENABLED="${BACKUP_ENABLED:-true}"
  BACKUP_DIR="${BACKUP_DIR:-/var/backups/vetka-backend-panel}"
  BACKUP_RETENTION_DAYS="${BACKUP_RETENTION_DAYS:-14}"
  BACKUP_ON_CALENDAR="${BACKUP_ON_CALENDAR:-*-*-* 03:30:00 UTC}"
  BACKUP_BEFORE_UPDATE="${BACKUP_BEFORE_UPDATE:-true}"
  ENABLE_HTTPS="${ENABLE_HTTPS:-false}"
  POSTGRES_USER="${POSTGRES_USER:-vetka}"
  POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-vetka}"
  POSTGRES_DB="${POSTGRES_DB:-vetka_backend}"
  PANEL_PUBLIC_BASE_URL="${PANEL_PUBLIC_BASE_URL:-http://localhost:8080}"
  HTTP_ADDR="${HTTP_ADDR:-:8080}"
  COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-}"
}

vetka_archive_basename() {
  local timestamp="$1"
  local short_sha="$2"
  printf '%s-%s-%s.tar.gz' "$VETKA_BACKUP_PREFIX" "$timestamp" "$short_sha"
}

vetka_is_project_backup_name() {
  [[ "$1" =~ ^${VETKA_BACKUP_PREFIX}-[0-9]{8}T[0-9]{6}Z-[A-Za-z0-9._-]+\.tar\.gz$ ]]
}

vetka_git_commit() {
  if git -C "$VETKA_PROJECT_ROOT" rev-parse HEAD >/dev/null 2>&1; then
    git -C "$VETKA_PROJECT_ROOT" rev-parse HEAD
  else
    printf 'unknown'
  fi
}

vetka_git_short_commit() {
  if git -C "$VETKA_PROJECT_ROOT" rev-parse --short HEAD >/dev/null 2>&1; then
    git -C "$VETKA_PROJECT_ROOT" rev-parse --short HEAD
  else
    printf 'nogit'
  fi
}

vetka_git_branch() {
  if git -C "$VETKA_PROJECT_ROOT" rev-parse --abbrev-ref HEAD >/dev/null 2>&1; then
    git -C "$VETKA_PROJECT_ROOT" rev-parse --abbrev-ref HEAD
  else
    printf 'unknown'
  fi
}

vetka_human_size() {
  local size_bytes="$1"
  awk -v size="$size_bytes" '
    function human(x) {
      split("B KiB MiB GiB TiB", units, " ")
      i=1
      while (x >= 1024 && i < 5) { x/=1024; i++ }
      return sprintf("%.1f %s", x, units[i])
    }
    BEGIN { print human(size) }'
}

vetka_sorted_unique_lines() {
  LC_ALL=C sort -u
}

vetka_sorted_lines() {
  LC_ALL=C sort
}

vetka_sort_file_list_stream() {
  LC_ALL=C grep -v '^$' | LC_ALL=C sort
}

vetka_reject_duplicates_in_sorted_list() {
  local label="$1"
  local list="$2"
  if [[ -n "$list" ]] && [[ -n "$(printf '%s\n' "$list" | uniq -d)" ]]; then
    vetka_die "${label} contains duplicate entries"
  fi
}

vetka_ensure_dir_mode() {
  local dir="$1"
  local mode="$2"
  mkdir -p -- "$dir"
  chmod "$mode" -- "$dir"
}

vetka_compose() {
  (
    cd "$VETKA_PROJECT_ROOT"
    docker compose "$@"
  )
}

vetka_compose_up_stack() {
  if [[ "${ENABLE_HTTPS}" == "true" || "${ENABLE_HTTPS}" == "yes" ]]; then
    vetka_compose --profile https up -d --build "$@"
  else
    vetka_compose up -d --build "$@"
  fi
}

vetka_wait_for_postgres() {
  local attempt
  for attempt in $(seq 1 30); do
    if vetka_compose exec -T postgres sh -lc 'pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB"' >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  return 1
}

vetka_wait_for_backend_internal_ready() {
  local attempt
  for attempt in $(seq 1 30); do
    if vetka_compose exec -T backend sh -lc 'if command -v wget >/dev/null 2>&1; then wget -qO- http://127.0.0.1:8080/ready; elif command -v curl >/dev/null 2>&1; then curl -fsS http://127.0.0.1:8080/ready; else exit 127; fi' >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  return 1
}

vetka_public_ready_url() {
  printf '%s/ready' "${PANEL_PUBLIC_BASE_URL%/}"
}

vetka_wait_for_public_ready() {
  local url="$1"
  local attempt
  for attempt in $(seq 1 10); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 3
  done
  return 1
}

vetka_postgres_version() {
  vetka_compose exec -T postgres sh -lc 'postgres --version' 2>/dev/null | tr -d '\r'
}

vetka_compose_project_name() {
  if [[ -n "${COMPOSE_PROJECT_NAME:-}" ]]; then
    printf '%s' "$COMPOSE_PROJECT_NAME"
  else
    basename "$VETKA_PROJECT_ROOT"
  fi
}

vetka_pg_image() {
  local image
  image="$(awk '/image:[[:space:]]*postgres:/ { sub(/^[[:space:]]*image:[[:space:]]*/, "", $0); print; exit }' "${VETKA_PROJECT_ROOT}/docker-compose.yml" 2>/dev/null || true)"
  [[ -n "$image" ]] || image="postgres:16-alpine"
  printf '%s' "$image"
}

vetka_pg_restore_host_major() {
  local version
  vetka_command_exists pg_restore || return 1
  version="$(pg_restore --version 2>/dev/null || true)"
  [[ -n "$version" ]] || return 1
  vetka_postgres_major_from_version "$version"
}

vetka_can_verify_dump() {
  local expected_major host_major
  expected_major="$(vetka_expected_postgres_major)"
  if vetka_command_exists pg_restore; then
    host_major="$(vetka_pg_restore_host_major || true)"
    if [[ -n "$host_major" && ( "$expected_major" == "unknown" || "$host_major" == "$expected_major" ) ]]; then
      return 0
    fi
  fi
  vetka_command_exists docker
}

vetka_pg_restore_list_from_file() {
  local dump_file="$1"
  local expected_major host_major image

  if [[ -n "${VETKA_PG_RESTORE_LIST_HOOK:-}" ]]; then
    "${VETKA_PG_RESTORE_LIST_HOOK}" "$dump_file"
    return 0
  fi

  expected_major="$(vetka_expected_postgres_major)"
  if vetka_command_exists pg_restore; then
    host_major="$(vetka_pg_restore_host_major || true)"
    if [[ -n "$host_major" && ( "$expected_major" == "unknown" || "$host_major" == "$expected_major" ) ]]; then
      pg_restore --list "$dump_file" >/dev/null
      return 0
    fi
  fi

  vetka_command_exists docker || vetka_die "pg_restore verification requires host pg_restore or docker"
  image="$(vetka_pg_image)"
  docker run --rm -i --entrypoint pg_restore "$image" --list < "$dump_file" >/dev/null
}

vetka_validate_archive_member() {
  local member="$1"
  [[ -n "$member" ]] || return 1
  [[ "$member" != /* ]] || return 1
  [[ ! "$member" =~ ^[A-Za-z]:[/\\] ]] || return 1
  [[ "$member" != *\\* ]] || return 1
  [[ "$member" != *$'\n'* && "$member" != *$'\r'* ]] || return 1
  local part
  IFS='/' read -r -a parts <<< "$member"
  for part in "${parts[@]}"; do
    [[ -n "$part" ]] || return 1
    [[ "$part" != "." && "$part" != ".." ]] || return 1
  done
}

vetka_verify_archive_listing() {
  local archive_path="$1"
  local top_level=""
  local line type member current_top

  while IFS= read -r line || [[ -n "$line" ]]; do
    type="${line:0:1}"
    case "$type" in
      -|d) ;;
      *)
        vetka_die "archive contains unsupported tar entry type: $type"
        ;;
    esac
  done < <(tar -tvzf "$archive_path")

  while IFS= read -r member || [[ -n "$member" ]]; do
    [[ -n "$member" ]] || continue
    vetka_validate_archive_member "$member" || vetka_die "archive contains unsafe path: $member"
    current_top="${member%%/*}"
    if [[ -z "$top_level" ]]; then
      top_level="$current_top"
    elif [[ "$current_top" != "$top_level" ]]; then
      vetka_die "archive must contain exactly one top-level directory"
    fi
  done < <(tar -tzf "$archive_path")

  [[ -n "$top_level" ]] || vetka_die "archive is empty"
}

vetka_find_payload_root() {
  local extract_root="$1"
  local entry
  mapfile -t entries < <(find "$extract_root" -mindepth 1 -maxdepth 1 -type d | vetka_sorted_lines)
  [[ "${#entries[@]}" -eq 1 ]] || vetka_die "archive must extract to exactly one top-level directory"
  entry="${entries[0]}"
  printf '%s\n' "$entry"
}

vetka_verify_payload_tree() {
  local payload_root="$1"
  [[ -d "$payload_root" ]] || vetka_die "payload root missing"
  [[ -f "${payload_root}/database.dump" ]] || vetka_die "payload missing database.dump"
  [[ -f "${payload_root}/metadata.json" ]] || vetka_die "payload missing metadata.json"
  [[ -f "${payload_root}/SHA256SUMS" ]] || vetka_die "payload missing SHA256SUMS"
}

vetka_verify_metadata_file() {
  local payload_root="$1"
  local metadata_file="${payload_root}/metadata.json"
  jq -e '
    .format_version == 1 and
    (.created_at_utc | type == "string") and
    (.files | type == "array") and
    (all(.files[]; type == "string"))
  ' "$metadata_file" >/dev/null || vetka_die "metadata.json is invalid"
}

vetka_collect_payload_files() {
  local payload_root="$1"
  (
    cd "$payload_root"
    find . -type f -printf '%P\n' | vetka_sort_file_list_stream
  )
}

vetka_collect_expected_metadata_files() {
  local payload_root="$1"
  (
    cd "$payload_root"
    find . -type f ! -name 'metadata.json' ! -name 'SHA256SUMS' -printf '%P\n' | vetka_sort_file_list_stream
  )
}

vetka_verify_metadata_consistency() {
  local payload_root="$1"
  local metadata_list actual_list
  vetka_verify_metadata_file "$payload_root"

  metadata_list="$(jq -r '.files[]' "${payload_root}/metadata.json" | vetka_sort_file_list_stream)"
  actual_list="$(vetka_collect_expected_metadata_files "$payload_root")"

  vetka_reject_duplicates_in_sorted_list "metadata.json files" "$metadata_list"
  grep -Fxq "database.dump" <<< "$metadata_list" || vetka_die "metadata.json must list database.dump"
  [[ "$metadata_list" == "$actual_list" ]] || vetka_die "metadata.json files do not match payload files"
}

vetka_parse_checksum_paths() {
  local checksum_file="$1"
  local line path
  while IFS= read -r line || [[ -n "$line" ]]; do
    [[ -n "$line" ]] || continue
    if [[ ! "$line" =~ ^[[:xdigit:]]{64}[[:space:]]+[\ \*](.+)$ ]]; then
      vetka_die "invalid SHA256SUMS line format"
    fi
    path="${BASH_REMATCH[1]}"
    vetka_validate_archive_member "$path" || vetka_die "SHA256SUMS contains unsafe path: $path"
    printf '%s\n' "$path"
  done < "$checksum_file"
}

vetka_verify_checksum_file() {
  local payload_root="$1"
  local checksum_file="${payload_root}/SHA256SUMS"
  local checksum_list expected_list
  checksum_list="$(vetka_parse_checksum_paths "$checksum_file" | vetka_sort_file_list_stream)"
  expected_list="$(vetka_collect_payload_files "$payload_root" | grep -vx 'SHA256SUMS' | vetka_sort_file_list_stream)"

  vetka_reject_duplicates_in_sorted_list "SHA256SUMS" "$checksum_list"
  [[ "$checksum_list" == "$expected_list" ]] || vetka_die "SHA256SUMS does not match payload files"
  (
    cd "$payload_root"
    sha256sum -c SHA256SUMS >/dev/null
  )
}

vetka_verify_extracted_payload_basic() {
  local payload_root="$1"
  vetka_verify_payload_tree "$payload_root"
  vetka_verify_metadata_consistency "$payload_root"
  vetka_verify_checksum_file "$payload_root"
}

vetka_verify_extracted_payload_full() {
  local payload_root="$1"
  vetka_verify_extracted_payload_basic "$payload_root"
  vetka_pg_restore_list_from_file "${payload_root}/database.dump"
}

vetka_prepare_archive_extract() {
  local archive_path="$1"
  local extract_root="$2"
  vetka_verify_archive_listing "$archive_path"
  tar --no-same-owner --no-same-permissions -xzf "$archive_path" -C "$extract_root"
}

vetka_with_temp_dir() {
  local temp_dir callback
  temp_dir="$(mktemp -d "${TMPDIR:-/tmp}/vetka-archive-verify.XXXXXXXX")"
  chmod 700 "$temp_dir"
  callback="$1"
  shift
  (
    trap 'rm -rf "$temp_dir"' EXIT
    "$callback" "$temp_dir" "$@"
  )
}

vetka_verify_archive_in_temp_basic() {
  local temp_dir="$1"
  local archive_path="$2"
  local payload_root
  vetka_prepare_archive_extract "$archive_path" "$temp_dir"
  payload_root="$(vetka_find_payload_root "$temp_dir")"
  vetka_verify_extracted_payload_basic "$payload_root"
}

vetka_verify_archive_in_temp_full() {
  local temp_dir="$1"
  local archive_path="$2"
  local payload_root
  vetka_prepare_archive_extract "$archive_path" "$temp_dir"
  payload_root="$(vetka_find_payload_root "$temp_dir")"
  vetka_verify_extracted_payload_full "$payload_root"
}

vetka_verify_archive_file_basic() {
  local archive_path="$1"
  vetka_with_temp_dir vetka_verify_archive_in_temp_basic "$archive_path"
}

vetka_verify_archive_file_full() {
  local archive_path="$1"
  vetka_with_temp_dir vetka_verify_archive_in_temp_full "$archive_path"
}

vetka_postgres_major_from_version() {
  local version="$1"
  if [[ "$version" =~ ([0-9]+)(\.[0-9]+)? ]]; then
    printf '%s' "${BASH_REMATCH[1]}"
  else
    printf 'unknown'
  fi
}

vetka_expected_postgres_major() {
  local image_tag
  image_tag="$(awk '/image:[[:space:]]*postgres:/ { sub(/^.*postgres:/, "", $0); print; exit }' "${VETKA_PROJECT_ROOT}/docker-compose.yml" 2>/dev/null || true)"
  image_tag="${image_tag%%-*}"
  image_tag="${image_tag%%\"*}"
  image_tag="${image_tag%%\'*}"
  [[ -n "$image_tag" ]] && printf '%s' "$image_tag" || printf 'unknown'
}

vetka_prompt_restore_confirmation() {
  local answer
  read -r -p "This will overwrite the current installation. Type RESTORE to continue: " answer || true
  [[ "$answer" == "RESTORE" ]]
}

vetka_apply_retention() {
  local backup_dir="$1"
  local retention_days="$2"
  [[ "$retention_days" =~ ^[0-9]+$ ]] || vetka_die "retention days must be numeric"
  (( retention_days > 0 )) || return 0
  vetka_assert_safe_backup_dir "$backup_dir" >/dev/null
  find "$backup_dir" -maxdepth 1 -type f -name "${VETKA_BACKUP_PREFIX}-*.tar.gz" -mtime "+${retention_days}" -delete
}

vetka_assert_safe_backup_dir() {
  local requested="$1"
  local canonical current prefix
  [[ -n "$requested" ]] || vetka_die "backup directory must not be empty"
  vetka_require_commands realpath
  [[ "$requested" == /* ]] || vetka_die "backup directory must be an absolute path"
  vetka_has_control_chars "$requested" && vetka_die "backup directory contains unsupported control characters"
  [[ ! "$requested" =~ [[:space:]] ]] || vetka_die "backup directory must not contain whitespace"

  current="/"
  IFS='/' read -r -a parts <<< "${requested#/}"
  local part
  for part in "${parts[@]}"; do
    [[ -n "$part" ]] || continue
    prefix="${current%/}/$part"
    if [[ -e "$prefix" && -L "$prefix" ]]; then
      vetka_die "backup directory path must not traverse symlink components"
    fi
    current="$prefix"
  done

  canonical="$(realpath -m -- "$requested")"
  vetka_has_control_chars "$canonical" && vetka_die "backup directory resolves to an invalid path"
  [[ ! "$canonical" =~ [[:space:]] ]] || vetka_die "backup directory resolves to a path with whitespace"
  [[ "$canonical" == /* ]] || vetka_die "backup directory resolves outside the filesystem root"
  [[ "$canonical" != *"/../"* && "$canonical" != *"/.." && "$canonical" != "../"* ]] || vetka_die "backup directory must not contain parent traversal"

  case "$canonical" in
    /|/etc|/etc/*|/usr|/usr/*|/bin|/bin/*|/sbin|/sbin/*|/lib|/lib/*|/lib64|/lib64/*|/proc|/proc/*|/sys|/sys/*|/dev|/dev/*|/run|/run/*|/root|/root/*|/boot|/boot/*)
      vetka_die "backup directory is too broad or unsafe: $canonical"
      ;;
  esac
  case "$canonical" in
    /var/backups/*|/srv/backups/*|/mnt/*|/media/*) ;;
    *)
      vetka_die "backup directory must stay under an approved backup root: $canonical"
      ;;
  esac
  printf '%s\n' "$canonical"
}

vetka_validate_systemd_calendar() {
  local schedule="$1"
  vetka_has_control_chars "$schedule" && vetka_die "systemd calendar contains unsupported control characters"
  [[ -n "$schedule" ]] || vetka_die "systemd calendar must not be empty"
  if vetka_command_exists systemd-analyze; then
    systemd-analyze calendar "$schedule" >/dev/null 2>&1 || vetka_die "invalid systemd calendar expression: $schedule"
  fi
}

vetka_install_backup_units() {
  local app_dir="$1"
  local backup_dir="$2"
  local on_calendar="$3"
  local service_file="$VETKA_SYSTEMD_SERVICE_FILE"
  local timer_file="$VETKA_SYSTEMD_TIMER_FILE"

  [[ "$app_dir" == /* ]] || vetka_die "application directory must be absolute"
  [[ ! "$app_dir" =~ [[:space:]] ]] || vetka_die "application directory must not contain whitespace"
  vetka_assert_safe_backup_dir "$backup_dir" >/dev/null
  vetka_validate_systemd_calendar "$on_calendar"

  cat >"$service_file" <<EOF
[Unit]
Description=Vetka Backend Panel backup
After=docker.service
Requires=docker.service

[Service]
Type=oneshot
WorkingDirectory=$app_dir
ExecStart=$app_dir/backup.sh create --output-dir $backup_dir --quiet
User=root
Group=root
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
RestrictRealtime=true
LockPersonality=true
MemoryDenyWriteExecute=true

[Install]
WantedBy=multi-user.target
EOF

  cat >"$timer_file" <<EOF
[Unit]
Description=Vetka Backend Panel backup timer

[Timer]
OnCalendar=$on_calendar
Persistent=true
RandomizedDelaySec=5m
Unit=vetka-backend-backup.service

[Install]
WantedBy=timers.target
EOF

  if vetka_command_exists systemd-analyze; then
    systemd-analyze verify "$service_file" "$timer_file" >/dev/null 2>&1 || vetka_die "systemd unit verification failed"
  fi

  systemctl daemon-reload
  systemctl enable --now vetka-backend-backup.timer
}

vetka_remove_backup_units() {
  local service_file="$VETKA_SYSTEMD_SERVICE_FILE"
  local timer_file="$VETKA_SYSTEMD_TIMER_FILE"
  if vetka_command_exists systemctl; then
    systemctl disable --now vetka-backend-backup.timer >/dev/null 2>&1 || true
  fi
  rm -f "$service_file" "$timer_file"
  if vetka_command_exists systemctl; then
    systemctl daemon-reload >/dev/null 2>&1 || true
  fi
}

vetka_ensure_lock_parent() {
  local lock_file="$1"
  local parent
  parent="$(dirname "$lock_file")"
  mkdir -p -- "$parent"
}

vetka_acquire_maintenance_lock() {
  local mode="${1:-wait}"
  if [[ "${VETKA_MAINTENANCE_LOCK_HELD}" == "true" || "${VETKA_MAINTENANCE_LOCK_ACQUIRED}" == "true" ]]; then
    return 0
  fi
  vetka_ensure_lock_parent "$VETKA_MAINTENANCE_LOCK_FILE"
  if vetka_command_exists flock; then
    exec 211>"$VETKA_MAINTENANCE_LOCK_FILE"
    if [[ "$mode" == "try" ]]; then
      if ! flock -n 211; then
        exec 211>&-
        return 1
      fi
    else
      flock 211
    fi
    VETKA_MAINTENANCE_LOCK_MODE="flock"
  else
    while ! mkdir "$VETKA_MAINTENANCE_LOCK_DIR" 2>/dev/null; do
      [[ "$mode" == "try" ]] && return 1
      sleep 1
    done
    VETKA_MAINTENANCE_LOCK_MODE="mkdir"
  fi
  VETKA_MAINTENANCE_LOCK_ACQUIRED="true"
}

vetka_release_maintenance_lock() {
  if [[ "${VETKA_MAINTENANCE_LOCK_ACQUIRED}" == "true" ]]; then
    if [[ "${VETKA_MAINTENANCE_LOCK_MODE}" == "flock" ]]; then
      exec 211>&-
    elif [[ "${VETKA_MAINTENANCE_LOCK_MODE}" == "mkdir" ]]; then
      rmdir "$VETKA_MAINTENANCE_LOCK_DIR" >/dev/null 2>&1 || true
    fi
    VETKA_MAINTENANCE_LOCK_ACQUIRED="false"
    VETKA_MAINTENANCE_LOCK_MODE=""
  fi
}
