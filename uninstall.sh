#!/usr/bin/env bash
set -Eeuo pipefail

APP_DIR="/opt/vetka-backend-panel"
ENV_FILE="$APP_DIR/.env"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "${SCRIPT_DIR}/scripts/backup-common.sh"

BACKUP_DIR="/var/backups/vetka-backend-panel"
if [[ -f "$ENV_FILE" ]]; then
  BACKUP_DIR="$(awk -F= '$1=="BACKUP_DIR"{print substr($0, index($0,$2))}' "$ENV_FILE" | tail -n 1 | tr -d '\r')"
  BACKUP_DIR="${BACKUP_DIR:-/var/backups/vetka-backend-panel}"
fi

if [[ ! -d "$APP_DIR" ]]; then
  echo "Application directory not found: $APP_DIR"
  exit 0
fi

vetka_remove_backup_units

(cd "$APP_DIR" && docker compose down || true)

read -r -p "Remove PostgreSQL data volume as well? [y/N]: " remove_data || true
if [[ "$remove_data" =~ ^([Yy]|[Yy][Ee][Ss])$ ]]; then
  (cd "$APP_DIR" && docker compose down -v || true)
fi

read -r -p "Remove application directory and env files? [y/N]: " remove_files || true
if [[ "$remove_files" =~ ^([Yy]|[Yy][Ee][Ss])$ ]]; then
  rm -rf "$APP_DIR"
fi

read -r -p "Delete all backup archives too? [y/N]: " remove_backups || true
if [[ "$remove_backups" =~ ^([Yy]|[Yy][Ee][Ss])$ ]]; then
  if [[ -d "$BACKUP_DIR" ]]; then
    BACKUP_DIR="$(vetka_assert_safe_backup_dir "$BACKUP_DIR")"
    find "$BACKUP_DIR" -maxdepth 1 -type f -name 'vetka-backend-panel-*.tar.gz' -delete
  fi
fi

echo "Uninstall completed."
