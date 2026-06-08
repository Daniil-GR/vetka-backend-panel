#!/usr/bin/env bash
set -euo pipefail

APP_DIR="/opt/vetka-backend-panel"

if [[ ! -d "$APP_DIR" ]]; then
  echo "Application directory not found: $APP_DIR"
  exit 0
fi

(cd "$APP_DIR" && docker compose down || true)

read -r -p "Remove PostgreSQL data volume as well? [y/N]: " remove_data || true
if [[ "$remove_data" =~ ^([Yy]|[Yy][Ee][Ss])$ ]]; then
  (cd "$APP_DIR" && docker compose down -v || true)
fi

read -r -p "Remove application directory and env files? [y/N]: " remove_files || true
if [[ "$remove_files" =~ ^([Yy]|[Yy][Ee][Ss])$ ]]; then
  rm -rf "$APP_DIR"
fi

echo "Uninstall completed."
