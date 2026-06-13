#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "${SCRIPT_DIR}/../scripts/backup-common.sh"

TEST_ROOT="$(mktemp -d)"
trap 'rm -rf "$TEST_ROOT"' EXIT

setup_jq_shim() {
  local shim_dir python_cmd
  if vetka_command_exists jq; then
    return 0
  fi

  if vetka_command_exists python; then
    python_cmd="python"
  elif vetka_command_exists python3; then
    python_cmd="python3"
  else
    printf 'SKIP: jq-dependent tests require python3 or python for jq shim\n'
    return 1
  fi

  shim_dir="${TEST_ROOT}/bin"
  mkdir -p "$shim_dir"
  cat > "${shim_dir}/jq" <<EOF
#!/usr/bin/env bash
set -Eeuo pipefail
"$python_cmd" - "\$@" <<'PY'
import json
import sys

args = sys.argv[1:]
raw = False
exit_mode = False
null_input = False
expr = None
files = []
i = 0
while i < len(args):
    arg = args[i]
    if arg == "-r":
        raw = True
    elif arg == "-e":
        exit_mode = True
    elif arg == "-n":
        null_input = True
    elif arg.startswith("-"):
        pass
    elif expr is None:
        expr = arg
    else:
        files.append(arg)
    i += 1

data = None
if not null_input:
    if files:
        with open(files[-1], "r", encoding="utf-8") as handle:
            data = json.load(handle)
    else:
        data = json.load(sys.stdin)

if expr is not None:
    expr = " ".join(expr.split())

def out(value):
    if raw and not isinstance(value, (dict, list)):
        sys.stdout.write("" if value is None else str(value))
    else:
        json.dump(value, sys.stdout, ensure_ascii=False)
    if raw:
        sys.stdout.write("\n")

if expr == '.format_version == 1 and (.created_at_utc | type == "string") and (.files | type == "array") and (all(.files[]; type == "string"))':
    ok = (
        isinstance(data, dict)
        and data.get("format_version") == 1
        and isinstance(data.get("created_at_utc"), str)
        and isinstance(data.get("files"), list)
        and all(isinstance(item, str) for item in data.get("files", []))
    )
    if not ok and exit_mode:
        sys.exit(1)
    out(ok)
elif expr == '.git_commit // "unknown"':
    out(data.get("git_commit", "unknown") if isinstance(data, dict) else "unknown")
elif expr == '.postgres_version // "unknown"':
    out(data.get("postgres_version", "unknown") if isinstance(data, dict) else "unknown")
elif expr == '.files[]':
    if not isinstance(data, dict) or not isinstance(data.get("files"), list):
        sys.exit(1)
    for item in data["files"]:
      sys.stdout.write(f"{item}\n")
else:
    sys.stderr.write(f"unsupported jq expression in test shim: {expr}\n")
    sys.exit(2)
PY
EOF
  chmod 755 "${shim_dir}/jq"
  export PATH="${shim_dir}:$PATH"
}

setup_jq_shim || true

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

assert_ok() {
  ( "$@" ) || fail "expected success: $*"
}

assert_fail() {
  if ( "$@" ) >/dev/null 2>&1; then
    fail "expected failure: $*"
  fi
}

make_temp_dir() {
  mktemp -d
}

test_validate_archive_member() {
  assert_ok vetka_validate_archive_member "vetka-backend-panel/database.dump"
  assert_fail vetka_validate_archive_member "/etc/passwd"
  assert_fail vetka_validate_archive_member "../escape"
  assert_fail vetka_validate_archive_member "config/../../etc/shadow"
  assert_fail vetka_validate_archive_member "C:/absolute/path"
}

test_backup_name() {
  assert_ok vetka_is_project_backup_name "vetka-backend-panel-20260613T033000Z-abc123.tar.gz"
  assert_fail vetka_is_project_backup_name "other-project-20260613T033000Z-abc123.tar.gz"
}

test_safe_backup_dir() {
  local tmpdir linkdir
  tmpdir="$(make_temp_dir)"
  trap 'rm -rf "$tmpdir"' RETURN

  [[ "$(vetka_assert_safe_backup_dir "/var/backups/vetka-backend-panel")" == "/var/backups/vetka-backend-panel" ]] || fail "canonical backup dir mismatch"
  assert_fail vetka_assert_safe_backup_dir "/"
  assert_fail vetka_assert_safe_backup_dir "/etc"
  assert_fail vetka_assert_safe_backup_dir "/etc/vetka-backups"
  assert_fail vetka_assert_safe_backup_dir "/var/backups/../../etc"
  assert_fail vetka_assert_safe_backup_dir "/tmp/with space"

  mkdir -p "${tmpdir}/parent"
  linkdir="${tmpdir}/parent/link"
  if ln -s /etc "$linkdir" 2>/dev/null && [[ -L "$linkdir" ]]; then
    assert_fail vetka_assert_safe_backup_dir "${linkdir}/vetka-backups"
  else
    printf 'SKIP: parent symlink backup-dir test (symlink unsupported)\n'
  fi
}

test_env_parser() {
  local tmpdir env_file pwned
  tmpdir="$(make_temp_dir)"
  trap 'rm -rf "$tmpdir"' RETURN
  env_file="${tmpdir}/.env"
  pwned="${tmpdir}/pwned"

  cat >"$env_file" <<EOF
# comments and whitespace
BACKUP_DIR=/var/backups/vetka-backend-panel
BACKUP_ON_CALENDAR=*-*-* 03:30:00 UTC
PANEL_PUBLIC_BASE_URL="https://panel.example.test"
HTTP_ADDR=':8080'
POSTGRES_USER = "vetka"
POSTGRES_PASSWORD='secret pass'
POSTGRES_DB=vetka_backend # comment
UNKNOWN_VAR=ignored
EVIL=\$(touch "$pwned")
BACKTICKS=\`touch "$pwned"\`
EOF

  unset BACKUP_DIR BACKUP_ON_CALENDAR PANEL_PUBLIC_BASE_URL HTTP_ADDR POSTGRES_USER POSTGRES_PASSWORD POSTGRES_DB UNKNOWN_VAR EVIL BACKTICKS || true
  vetka_load_env_file "$env_file"

  [[ "${BACKUP_DIR}" == "/var/backups/vetka-backend-panel" ]] || fail "BACKUP_DIR parsed incorrectly"
  [[ "${BACKUP_ON_CALENDAR}" == "*-*-* 03:30:00 UTC" ]] || fail "BACKUP_ON_CALENDAR parsed incorrectly"
  [[ "${PANEL_PUBLIC_BASE_URL}" == "https://panel.example.test" ]] || fail "PANEL_PUBLIC_BASE_URL parsed incorrectly"
  [[ "${HTTP_ADDR}" == ":8080" ]] || fail "HTTP_ADDR parsed incorrectly"
  [[ "${POSTGRES_USER}" == "vetka" ]] || fail "POSTGRES_USER parsed incorrectly"
  [[ "${POSTGRES_PASSWORD}" == "secret pass" ]] || fail "POSTGRES_PASSWORD parsed incorrectly"
  [[ "${POSTGRES_DB}" == "vetka_backend" ]] || fail "POSTGRES_DB parsed incorrectly"
  [[ ! -e "$pwned" ]] || fail "dotenv parser executed code"
  [[ -z "${UNKNOWN_VAR:-}" ]] || fail "dotenv parser should ignore unknown variables"
  [[ -z "${EVIL:-}" ]] || fail "dotenv parser should ignore non-whitelisted variables"
  [[ -z "${BACKTICKS:-}" ]] || fail "dotenv parser should ignore non-whitelisted variables"
}

test_env_preserve_and_overwrite() {
  local tmpdir env_file
  tmpdir="$(make_temp_dir)"
  trap 'rm -rf "$tmpdir"' RETURN
  env_file="${tmpdir}/.env"

  cat >"$env_file" <<'EOF'
BACKUP_DIR=/from-file
BACKUP_RETENTION_DAYS=14
EOF

  BACKUP_DIR="/from-env"
  BACKUP_RETENTION_DAYS="30"
  export BACKUP_DIR BACKUP_RETENTION_DAYS
  vetka_load_env_file "$env_file" preserve-existing
  [[ "$BACKUP_DIR" == "/from-env" ]] || fail "preserve-existing should keep environment override"
  [[ "$BACKUP_RETENTION_DAYS" == "30" ]] || fail "preserve-existing should keep environment override"

  vetka_load_env_file "$env_file" overwrite
  [[ "$BACKUP_DIR" == "/from-file" ]] || fail "overwrite should replace BACKUP_DIR"
  [[ "$BACKUP_RETENTION_DAYS" == "14" ]] || fail "overwrite should replace BACKUP_RETENTION_DAYS"

  ENABLE_HTTPS="yes"
  POSTGRES_PASSWORD="old-secret"
  export ENABLE_HTTPS POSTGRES_PASSWORD
  vetka_load_env_file "$env_file" overwrite
  [[ -z "${ENABLE_HTTPS+x}" ]] || fail "overwrite should clear missing ENABLE_HTTPS"
  [[ -z "${POSTGRES_PASSWORD+x}" ]] || fail "overwrite should clear missing POSTGRES_PASSWORD"
  vetka_apply_backup_defaults
  [[ "$ENABLE_HTTPS" == "false" ]] || fail "defaults should repopulate ENABLE_HTTPS after overwrite"
  [[ "$POSTGRES_PASSWORD" == "vetka" ]] || fail "defaults should repopulate POSTGRES_PASSWORD after overwrite"
}

make_valid_payload() {
  local root="$1"
  mkdir -p "${root}/payload/config" "${root}/payload/reference"
  printf 'dump' > "${root}/payload/database.dump"
  cat > "${root}/payload/metadata.json" <<'EOF'
{"format_version":1,"created_at_utc":"2026-06-13T03:30:00Z","files":["database.dump"]}
EOF
  (
    cd "${root}/payload"
    sha256sum database.dump metadata.json > SHA256SUMS
  )
}

write_checksums() {
  local payload_root="$1"
  (
    cd "$payload_root"
    find . -type f ! -name 'SHA256SUMS' -printf '%P\n' | sort | xargs sha256sum > SHA256SUMS
  )
}

test_archive_listing_validation() {
  local tmpdir archive_ok archive_traversal archive_symlink archive_hardlink
  tmpdir="$(make_temp_dir)"
  trap 'rm -rf "$tmpdir"' RETURN

  make_valid_payload "$tmpdir"
  archive_ok="${tmpdir}/ok.tar.gz"
  tar -czf "$archive_ok" -C "$tmpdir" payload
  assert_ok vetka_verify_archive_listing "$archive_ok"

  archive_traversal="${tmpdir}/traversal.tar.gz"
  tar -czf "$archive_traversal" -C "$tmpdir" --transform='s#^payload#../payload#' payload
  assert_fail vetka_verify_archive_listing "$archive_traversal"

  if ln -s database.dump "${tmpdir}/payload/symlink.dump" 2>/dev/null; then
    archive_symlink="${tmpdir}/symlink.tar.gz"
    tar -czf "$archive_symlink" -C "$tmpdir" payload
    if tar -tvzf "$archive_symlink" | grep -q '^l'; then
      assert_fail vetka_verify_archive_listing "$archive_symlink"
    else
      printf 'SKIP: symlink archive test (tar did not preserve symlink type)\n'
    fi
    rm -f "${tmpdir}/payload/symlink.dump"
  else
    printf 'SKIP: symlink archive test (symlink unsupported)\n'
  fi

  if ln "${tmpdir}/payload/database.dump" "${tmpdir}/payload/hardlink.dump" 2>/dev/null; then
    archive_hardlink="${tmpdir}/hardlink.tar.gz"
    tar -czf "$archive_hardlink" -C "$tmpdir" payload
    if tar -tvzf "$archive_hardlink" | grep -q '^h'; then
      assert_fail vetka_verify_archive_listing "$archive_hardlink"
    else
      printf 'SKIP: hardlink archive test (tar did not preserve hardlink type)\n'
    fi
  else
    printf 'SKIP: hardlink archive test (hardlink unsupported)\n'
  fi
}

test_payload_metadata_and_checksum_validation() {
  local tmpdir payload_root
  tmpdir="$(make_temp_dir)"
  trap 'rm -rf "$tmpdir"' RETURN
  make_valid_payload "$tmpdir"
  payload_root="${tmpdir}/payload"

  assert_ok vetka_verify_extracted_payload_basic "$payload_root"

  cat > "${payload_root}/metadata.json" <<'EOF'
{"format_version":2,"created_at_utc":"2026-06-13T03:30:00Z","files":["database.dump"]}
EOF
  write_checksums "$payload_root"
  assert_fail vetka_verify_extracted_payload_basic "$payload_root"

  cat > "${payload_root}/metadata.json" <<'EOF'
{"format_version":1,"created_at_utc":"2026-06-13T03:30:00Z","files":[]}
EOF
  write_checksums "$payload_root"
  assert_fail vetka_verify_extracted_payload_basic "$payload_root"

  cat > "${payload_root}/metadata.json" <<'EOF'
{"format_version":1,"created_at_utc":"2026-06-13T03:30:00Z","files":["database.dump"]}
EOF
  printf '0000000000000000000000000000000000000000000000000000000000000000  ../evil\n' > "${payload_root}/SHA256SUMS"
  assert_fail vetka_verify_extracted_payload_basic "$payload_root"

  write_checksums "$payload_root"
  printf 'extra\n' > "${payload_root}/unchecked.txt"
  assert_fail vetka_verify_extracted_payload_basic "$payload_root"

  rm -f "${payload_root}/unchecked.txt"
  write_checksums "$payload_root"
  sed -i '/metadata.json/d' "${payload_root}/SHA256SUMS"
  assert_fail vetka_verify_extracted_payload_basic "$payload_root"
}

test_invalid_dump_rejected_when_verifier_available() {
  local tmpdir payload_root
  tmpdir="$(make_temp_dir)"
  trap 'rm -rf "$tmpdir"' RETURN
  make_valid_payload "$tmpdir"
  payload_root="${tmpdir}/payload"
  printf 'not-a-pg-custom-dump\n' > "${payload_root}/database.dump"
  write_checksums "$payload_root"

  if vetka_can_verify_dump; then
    assert_fail vetka_verify_extracted_payload_full "$payload_root"
  else
    printf 'SKIP: invalid database.dump verification test (no pg_restore verifier available)\n'
  fi
}

test_failed_verification_cleans_temp_dir() {
  local tmpdir archive tmp_verify_root
  tmpdir="$(make_temp_dir)"
  trap 'rm -rf "$tmpdir"' RETURN
  tmp_verify_root="${tmpdir}/verify-tmp"
  mkdir -p "$tmp_verify_root"
  make_valid_payload "$tmpdir"
  archive="${tmpdir}/bad.tar.gz"
  printf 'broken\n' > "${tmpdir}/payload/database.dump"
  write_checksums "${tmpdir}/payload"
  printf '0000000000000000000000000000000000000000000000000000000000000000  database.dump\n' > "${tmpdir}/payload/SHA256SUMS"
  tar -czf "$archive" -C "$tmpdir" payload

  TMPDIR="$tmp_verify_root" assert_fail vetka_verify_archive_file_basic "$archive"
  if find "$tmp_verify_root" -mindepth 1 -maxdepth 1 -name 'vetka-archive-verify.*' | grep -q .; then
    fail "temporary verify directories should be cleaned up after failure"
  fi
}

test_restore_verify_only_rejects_corrupted_archive() {
  local tmpdir archive
  tmpdir="$(make_temp_dir)"
  trap 'rm -rf "$tmpdir"' RETURN
  make_valid_payload "$tmpdir"
  archive="${tmpdir}/corrupted.tar.gz"
  printf 'broken\n' > "${tmpdir}/payload/database.dump"
  printf '0000000000000000000000000000000000000000000000000000000000000000  database.dump\n' > "${tmpdir}/payload/SHA256SUMS"
  tar -czf "$archive" -C "$tmpdir" payload

  if ( cd "${SCRIPT_DIR}/.." && bash ./restore.sh --archive "$archive" --verify-only ) >/dev/null 2>&1; then
    fail "restore.sh --verify-only must reject corrupted archive"
  fi
}

test_restore_verify_only_cleans_temp_dir_on_success() {
  local tmpdir archive restore_tmp hook
  tmpdir="$(make_temp_dir)"
  trap 'rm -rf "$tmpdir"' RETURN
  restore_tmp="${tmpdir}/restore-tmp"
  mkdir -p "$restore_tmp"
  make_valid_payload "$tmpdir"
  archive="${tmpdir}/valid.tar.gz"
  hook="${tmpdir}/hook.sh"
  cat > "$hook" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
  chmod 755 "$hook"
  tar -czf "$archive" -C "$tmpdir" payload

  if ! (
    cd "${SCRIPT_DIR}/.."
    TMPDIR="$restore_tmp" VETKA_PG_RESTORE_LIST_HOOK="$hook" bash ./restore.sh --archive "$archive" --verify-only
  ) >/dev/null 2>&1; then
    fail "restore verify-only should succeed"
  fi
  if find "$restore_tmp" -mindepth 1 -maxdepth 1 -name 'vetka-restore.*' | grep -q .; then
    fail "restore temp directories should be cleaned after successful verify-only"
  fi
}

test_restore_verify_only_cleans_temp_dir_on_failure() {
  local tmpdir archive restore_tmp hook
  tmpdir="$(make_temp_dir)"
  trap 'rm -rf "$tmpdir"' RETURN
  restore_tmp="${tmpdir}/restore-tmp"
  mkdir -p "$restore_tmp"
  make_valid_payload "$tmpdir"
  archive="${tmpdir}/invalid.tar.gz"
  hook="${tmpdir}/hook.sh"
  cat > "$hook" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
  chmod 755 "$hook"
  printf 'broken\n' > "${tmpdir}/payload/database.dump"
  write_checksums "${tmpdir}/payload"
  tar -czf "$archive" -C "$tmpdir" payload

  if (
    cd "${SCRIPT_DIR}/.."
    TMPDIR="$restore_tmp" VETKA_PG_RESTORE_LIST_HOOK="$hook" bash ./restore.sh --archive "$archive" --verify-only
  ) >/dev/null 2>&1; then
    fail "restore verify-only should fail when dump verifier fails"
  fi
  if find "$restore_tmp" -mindepth 1 -maxdepth 1 -name 'vetka-restore.*' | grep -q .; then
    fail "restore temp directories should be cleaned after failed verify-only"
  fi
}

test_restore_verify_only_calls_full_verifier_once() {
  local tmpdir archive counter hook
  tmpdir="$(make_temp_dir)"
  trap 'rm -rf "$tmpdir"' RETURN
  make_valid_payload "$tmpdir"
  archive="${tmpdir}/valid.tar.gz"
  counter="${tmpdir}/counter"
  hook="${tmpdir}/hook.sh"
  printf '0\n' > "$counter"
  cat > "$hook" <<EOF
#!/usr/bin/env bash
set -Eeuo pipefail
count=\$(cat "$counter")
count=\$((count + 1))
printf '%s\n' "\$count" > "$counter"
EOF
  chmod 755 "$hook"
  tar -czf "$archive" -C "$tmpdir" payload

  if ! (
    cd "${SCRIPT_DIR}/.."
    VETKA_PG_RESTORE_LIST_HOOK="$hook" bash ./restore.sh --archive "$archive" --verify-only >/dev/null
  ); then
    fail "restore verify-only should succeed with hook verifier"
  fi
  [[ "$(tr -d '\r\n' < "$counter")" == "1" ]] || fail "full verifier should be called exactly once"
}

test_docker_pg_restore_verifier_command() {
  local tmpdir dump_file docker_log docker_stub pg_restore_stub
  tmpdir="$(make_temp_dir)"
  trap 'rm -rf "$tmpdir"' RETURN
  dump_file="${tmpdir}/database.dump"
  docker_log="${tmpdir}/docker.log"
  docker_stub="${tmpdir}/docker"
  pg_restore_stub="${tmpdir}/pg_restore"
  printf 'fake-dump' > "$dump_file"

  cat > "$docker_stub" <<EOF
#!/usr/bin/env bash
set -Eeuo pipefail
printf '%s\n' "\$*" > "$docker_log"
cat >/dev/null
EOF
  chmod 755 "$docker_stub"
  cat > "$pg_restore_stub" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
  chmod 755 "$pg_restore_stub"

  if ! (
    export PATH="${tmpdir}:$PATH"
    source "${SCRIPT_DIR}/../scripts/backup-common.sh"
    vetka_pg_restore_list_from_file "$dump_file"
  ); then
    fail "docker pg_restore verifier should succeed via docker stub"
  fi
  grep -q -- '--list' "$docker_log" || fail "docker pg_restore verifier must pass --list"
  if grep -q '/dev/stdin' "$docker_log"; then
    fail "docker pg_restore verifier must not pass /dev/stdin"
  fi
  if grep -qE -- '--mount|-v ' "$docker_log"; then
    fail "docker pg_restore verifier must not mount volumes"
  fi
}

test_restore_sql_uses_template0() {
  grep -q "CREATE DATABASE %I TEMPLATE template0" "${SCRIPT_DIR}/../restore.sh" || fail "restore should create database from template0"
}

test_maintenance_lock() {
  local tmpdir
  tmpdir="$(make_temp_dir)"
  trap 'rm -rf "$tmpdir"' RETURN

  export VETKA_MAINTENANCE_LOCK_FILE="${tmpdir}/maintenance.lock"
  export VETKA_MAINTENANCE_LOCK_DIR="${tmpdir}/maintenance.lock.d"
  # shellcheck disable=SC1091
  source "${SCRIPT_DIR}/../scripts/backup-common.sh"

  vetka_acquire_maintenance_lock try || fail "first maintenance lock acquisition should succeed"
  (
    export VETKA_MAINTENANCE_LOCK_FILE="${tmpdir}/maintenance.lock"
    export VETKA_MAINTENANCE_LOCK_DIR="${tmpdir}/maintenance.lock.d"
    # shellcheck disable=SC1091
    source "${SCRIPT_DIR}/../scripts/backup-common.sh"
    vetka_acquire_maintenance_lock try
  ) && fail "second maintenance lock acquisition should fail"
  vetka_release_maintenance_lock

  vetka_acquire_maintenance_lock try || fail "lock should be reusable after release"
  vetka_release_maintenance_lock
}

test_systemd_calendar_validation() {
  (
    systemd-analyze() { return 0; }
    export -f systemd-analyze
    vetka_validate_systemd_calendar "*-*-* 03:30:00 UTC"
  ) || fail "expected valid calendar"

  if (
    systemd-analyze() { return 1; }
    export -f systemd-analyze
    vetka_validate_systemd_calendar $'bad\ncalendar'
  ); then
    fail "expected invalid calendar rejection"
  fi
}

test_validate_archive_member
test_backup_name
test_safe_backup_dir
test_env_parser
test_env_preserve_and_overwrite
test_archive_listing_validation
test_payload_metadata_and_checksum_validation
test_invalid_dump_rejected_when_verifier_available
test_failed_verification_cleans_temp_dir
test_restore_verify_only_rejects_corrupted_archive
test_restore_verify_only_cleans_temp_dir_on_success
test_restore_verify_only_cleans_temp_dir_on_failure
test_restore_verify_only_calls_full_verifier_once
test_docker_pg_restore_verifier_command
test_restore_sql_uses_template0
test_maintenance_lock
test_systemd_calendar_validation

printf 'backup-common tests passed\n'
