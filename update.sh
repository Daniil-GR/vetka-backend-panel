#!/usr/bin/env bash
set -Eeuo pipefail

APP_DIR="/opt/vetka-backend-panel"
ENV_FILE="$APP_DIR/.env"
SKIP_BACKUP="${SKIP_BACKUP_BEFORE_UPDATE:-false}"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "${SCRIPT_DIR}/scripts/compose-runtime-common.sh"

usage() {
  cat <<'EOF'
Usage: update.sh [--skip-backup]
EOF
}

command_exists() {
  command -v "$1" >/dev/null 2>&1
}

ensure_docker_available() {
  if ! command_exists docker; then
    echo "Docker CLI is not installed or not available in PATH." >&2
    exit 1
  fi
  if command_exists systemctl; then
    systemctl enable --now docker
    if ! systemctl is-enabled docker >/dev/null 2>&1; then
      echo "Docker service is not enabled in systemd autostart." >&2
      exit 1
    fi
    if ! systemctl is-active docker >/dev/null 2>&1; then
      echo "Docker service is not running." >&2
      exit 1
    fi
  fi
  if ! docker info >/dev/null 2>&1; then
    echo "Docker daemon is not available." >&2
    exit 1
  fi
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --skip-backup)
      SKIP_BACKUP="true"
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      exit 1
      ;;
  esac
done

if [[ ! -d "$APP_DIR/.git" ]]; then
  echo "Repository not found at $APP_DIR" >&2
  exit 1
fi

ensure_docker_available

ENABLE_HTTPS="no"
BACKUP_BEFORE_UPDATE="true"
if [[ -f "$ENV_FILE" ]]; then
  ENABLE_HTTPS="$(awk -F= '$1=="ENABLE_HTTPS"{print $2}' "$ENV_FILE" | tail -n 1 | tr -d '\r')"
  BACKUP_BEFORE_UPDATE="$(awk -F= '$1=="BACKUP_BEFORE_UPDATE"{print $2}' "$ENV_FILE" | tail -n 1 | tr -d '\r')"
fi

if [[ "$BACKUP_BEFORE_UPDATE" == "true" && "$SKIP_BACKUP" != "true" ]]; then
  echo "Creating backup before update..."
  bash "$APP_DIR/backup.sh" create --quiet >/dev/null || {
    echo "Backup before update failed." >&2
    exit 1
  }
fi

git -C "$APP_DIR" fetch origin
CURRENT_COMMIT="$(git -C "$APP_DIR" rev-parse HEAD)"
TARGET_COMMIT="$(git -C "$APP_DIR" rev-parse origin/main)"

echo "Current commit: $CURRENT_COMMIT"
echo "Target commit:  $TARGET_COMMIT"
read -r -p "Reset to origin/main and rebuild containers? [y/N]: " answer || true
if [[ ! "$answer" =~ ^([Yy]|[Yy][Ee][Ss])$ ]]; then
  echo "Update cancelled."
  exit 0
fi

git -C "$APP_DIR" reset --hard origin/main
if [[ "$ENABLE_HTTPS" == "yes" ]]; then
  (cd "$APP_DIR" && docker compose --profile https up -d --build)
else
  (cd "$APP_DIR" && docker compose up -d --build)
fi
if ! verify_compose_runtime; then
  echo "ERROR: Compose runtime verification failed." >&2
  exit 1
fi
show_compose_status
if ! verify_postgres_ready; then
  exit 1
fi

echo "Update completed."
