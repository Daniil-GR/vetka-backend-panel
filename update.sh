#!/usr/bin/env bash
set -euo pipefail

APP_DIR="/opt/vetka-backend-panel"
ENV_FILE="$APP_DIR/.env"

if [[ ! -d "$APP_DIR/.git" ]]; then
  echo "Repository not found at $APP_DIR" >&2
  exit 1
fi

ENABLE_HTTPS="no"
if [[ -f "$ENV_FILE" ]]; then
  ENABLE_HTTPS="$(awk -F= '$1=="ENABLE_HTTPS"{print $2}' "$ENV_FILE" | tail -n 1 | tr -d '\r')"
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
(cd "$APP_DIR" && docker compose ps)
(cd "$APP_DIR" && docker compose exec -T postgres pg_isready -U vetka -d vetka_backend)

echo "Update completed."
