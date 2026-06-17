# Vetka Backend Panel

Vetka Backend Panel is the central Go/PostgreSQL backend for managing Vetka nodes, users, node assignments, sync versions, and subscription output.

`vetka-node-agent` is a separate repository and is intentionally not implemented here. The Backend Panel and PostgreSQL database are the source of truth. Node Agent is only an executor and applied-state cache.

## Architecture

```text
Bot / Admin UI / API
        |
Vetka Backend Panel
        |
PostgreSQL = source of truth
        |
Node Manager
        |
Node Agent API
        |
Protocol Driver on node: Naive or Mieru
```

One node has exactly one protocol: `naive` or `mieru`. User assignments inherit the node protocol, and PostgreSQL enforces that invariant.

## Local Development

Copy `.env.example` to `.env`, adjust secrets, then start PostgreSQL and the backend:

```bash
docker compose up postgres
go run ./cmd/server
```

Or run both services through Compose:

```bash
docker compose up --build
```

The installer enables Docker in systemd autostart, and the Compose stack declares `restart: unless-stopped` for `postgres`, `backend`, and `caddy`. After a normal server reboot the stack should come back automatically. If an administrator intentionally stops a container, `unless-stopped` preserves that decision instead of forcing the container back up.

The default `docker-compose.yml` keeps both PostgreSQL and the backend on the internal Docker network only. Login uses `ADMIN_USERNAME` and `ADMIN_PASSWORD`. API endpoints use:

```http
Authorization: Bearer <ADMIN_API_TOKEN>
```

For HTTPS deployments, the repository also includes a `caddy` service and `Caddyfile`. The intended production split is:

- `panel.vetka.tech`: admin UI, login, nodes, users, API
- `sub2.vetka.tech`: subscription delivery only under `/sub/*`

In HTTPS mode only `80/443` should be public. Direct HTTP mode exposes `8080` only when it is explicitly enabled through an override file.

## Migrations

Migrations are applied automatically on server startup. You can also run:

```bash
make migrate-up
```

The SQL source for the first migration is in `migrations/001_initial.up.sql`; the application embeds the same schema in Go for simple MVP startup.

## Creating The First Node

Open `/nodes` and choose one of the two node lifecycle paths.

### Create Planned Node

Use this when the server does not have `vetka-node-agent` installed yet.

- Backend generates `NODE_ID` and `NODE_SECRET` if they are missing.
- Backend stores the node with `setup_state=planned`.
- Backend does not call `/status` during creation.
- The UI shows the install command for the future node.

### Adopt Existing Node

Use this when `vetka-node-agent` is already installed and reachable.

- Backend requires `node_id`, `node_secret`, `api_url`, and `protocol_type`.
- Backend calls `/status` and validates `node_id` and `protocol_type`.
- Backend bootstraps `desired_config_version` and `last_applied_version` from the remote node.
- The node is stored as `setup_state=connected`.

After creation the UI shows install environment values for the separate Node Agent:

```bash
NODE_ID="..."
NODE_SECRET="..."
PROTOCOL_TYPE="naive"
NODE_PORT="2222"
NODE_LISTEN_HOST="0.0.0.0"
BACKEND_PANEL_IP="<backend_ip>"
```

For production, the Node Agent port should be reachable only from the Backend Panel IP.

## Syncing Nodes

The Backend Panel sends the full desired state through:

```http
POST /v1/sync
Authorization: Bearer <NODE_SECRET>
X-Node-Id: <NODE_ID>
```

No per-user create/delete commands are sent to nodes. On sync success the panel updates `last_applied_version`; on failure it records `last_error` and a row in `node_sync_events`.

When onboarding an existing node, the Backend Panel first reads `/status` and aligns local `desired_config_version` and `last_applied_version` with the remote node before sending the next sync. If a sync hits `stale_version`, the panel refreshes the remote version and retries once with the next version number.

## Node Lifecycle

Node setup states:

- `planned`: backend metadata exists, but the node may not be installed or reachable yet.
- `connected`: backend successfully validated or synced the node.
- `unreachable`: backend could not reach the node during sync.
- `disabled`: node is intentionally disabled in the backend.

For a planned node, `Health`, `Status`, or `Sync` can promote it to `connected` once the agent becomes reachable and `/status` succeeds.

## Protocol Settings

Each node stores `protocol_settings` in PostgreSQL.

- Mieru settings: port range, protocol, MTU, multiplexing, handshake mode, traffic pattern, profile.
- Naive settings: port.

Defaults:

- Mieru ports: `2012-2022`
- Mieru protocol: `TCP`
- Mieru MTU: `1400`
- Mieru multiplexing: `MULTIPLEXING_HIGH`
- Mieru handshake mode: `HANDSHAKE_NO_WAIT`
- Mieru traffic pattern: empty
- Mieru profile: node name
- Naive port: `443`

The subscription builder now emits real `mierus://` sharing links for Mieru nodes using these settings, including repeated `port` and `protocol` pairs when multiple port ranges are configured.

## Users And Subscriptions

Create users in `/users`. The panel generates `subscription_token` and per-node protocol credentials. A user subscription is available at:

```text
https://sub2.vetka.tech/sub/<subscription_token>
```

Default subscription responses are JSON configs suitable for Karing and sing-box-style imports:

```text
/sub/<token>
/sub/<token>?format=json
/sub/<token>?format=karing
/sub/<token>?format=sing-box
```

Hiddify-oriented plain-text formats are also available:

```text
/sub/<token>?format=hiddify
/sub/<token>?format=hiddify-json
/sub/<token>?format=raw
/sub/<token>?format=mierus
/sub/<token>?format=naive
```

Disabled or expired users do not receive a subscription response and are excluded from node sync payloads.

`quota_mb` is currently metadata for `Subscription-Userinfo` only. `upload` and `download` stay `0`, and `total` is derived as `quota_mb * 1024 * 1024` when `quota_mb > 0`.

JSON subscriptions include a selector outbound tagged `proxy`, protocol-specific outbounds for assigned nodes, DNS config, and route rules compatible with a simple Karing import flow. Naive uses the configured node port and Mieru uses the first port from the configured range when rendered into JSON.

`format=hiddify` returns a plain-text subscription with one proxy link per line:

- Mieru nodes as `mierus://...`
- Naive nodes as `naive://...`

`format=hiddify-json` is an experimental Hiddify-style JSON export based on Hiddify's internal export shape. Use it only for testing and comparison.

`format=mierus` is kept as a backward-compatible Mieru-only alias. `format=naive` returns Naive-only links. `format=raw` is a diagnostic format and may include both `naive://` and legacy `naive+https://`.

Naive in Hiddify depends on the platform and build. Android may work; iOS may fail with `cronet: library not found`. Karing works with Naive through the default JSON format.

All `/sub/*` responses include:

- `Profile-Title`
- `Profile-Update-Interval`
- `Subscription-Userinfo`
- `Content-Disposition`

`Subscription-Userinfo` is emitted as:

```text
upload=0; download=0; total=<bytes>; expire=<unix>
```

`expire` always uses the exact Unix timestamp from `expires_at.UTC()`. The panel does not apply date-only or end-of-day expansion.

Expired user revoke:

- the subscription endpoint stops returning active configs immediately after `expires_at <= now()`
- the expiry reconciler periodically syncs affected nodes
- expired users are excluded from desired state
- Node Agent removes expired credentials on the next successful sync
- old cached client configs stop working after server-side credentials are removed
- the expiry reconciler syncs affected assigned nodes even if a node is hidden from generated subscriptions via `node.enabled=false`
- this is user-level revoke, not node-level emergency revoke

The default profile title is `Ветка VPN`.

## API For Future Telegram Bot

Implemented MVP endpoints:

```http
GET  /api/nodes
POST /api/nodes
GET  /api/nodes/{id}
PATCH /api/nodes/{id}
POST /api/nodes/{id}/health
POST /api/nodes/{id}/status
POST /api/nodes/{id}/sync
POST /api/nodes/sync-all

POST /api/users
GET  /api/users/{id}
PATCH /api/users/{id}
POST /api/users/{id}/enable
POST /api/users/{id}/disable
POST /api/users/{id}/assign-node
POST /api/users/{id}/unassign-node
POST /api/users/{id}/sync
POST /api/users/reconcile-expired
GET  /api/users/{id}/subscription
```

`GET /api/nodes` and `GET /api/nodes/{id}` return masked `node_secret`. `POST /api/nodes` returns the raw generated or submitted secret once so the admin can bootstrap a Node Agent.

Telegram Bot and payment logic are not part of this repository.

## Security Notes

`node_secret` is stored in PostgreSQL for the MVP. The UI masks it after creation and API responses should avoid exposing raw secrets outside trusted admin flows.

PostgreSQL should never be publicly reachable. Node Agent port `2222` should be open only for the Backend Panel IP.

If `NODE_SECRET` is exposed, rotate it and update the backend record before the next sync.

Admin UI should be served behind HTTPS in production. In HTTPS mode only `80/443` should be exposed. In direct HTTP mode `8080` should be exposed only when you explicitly enable it. Generated secrets should be stored securely and should not be committed to git.

The subscription domain should only expose `/sub/*`. Requests like `https://sub2.vetka.tech/login` should return `404`.

Disabling a node hides it from generated subscriptions. Existing client configs may keep working until the client refreshes the subscription. This patch does not force an empty revoke sync for `node.enabled=false`.

`node.enabled=false` is not an emergency revoke. It hides the node from generated subscriptions, and existing cached client configs may keep working until the client refreshes.

TODO: add encryption at rest for node secrets using `APP_SECRET`.

Never commit real `.env` files or production secrets.

## Installer

The repository includes:

- `backup.sh`: create, verify, and list sensitive backup archives containing the PostgreSQL dump and runtime configuration files.
- `restore.sh`: verify and restore a backup archive on the current or a new server.
- `install.sh`: install dependencies, generate secrets, create `.env`, manage `docker-compose.override.yml`, optionally enable Caddy HTTPS, and start the stack.
- `update.sh`: fetch, confirm target revision, reset to `origin/main`, rebuild containers in the same HTTPS or direct HTTP mode stored in `.env`, and check health.
- `uninstall.sh`: stop containers and optionally remove data and application files.

The installer keeps PostgreSQL private, can configure UFW, and supports either:

- HTTPS mode: public `80/443` through Caddy, no public backend `8080`
- Direct HTTP mode: public `8080` only when explicitly enabled

`update.sh` preserves the chosen install mode. PostgreSQL is never published publicly by the default compose stack.

Docker autostart and restart policy checks:

```bash
systemctl is-enabled docker
systemctl is-active docker

docker compose ps

docker inspect \
  --format '{{.Name}} -> {{.HostConfig.RestartPolicy.Name}}' \
  $(docker compose --profile https ps -aq)
```

Important environment variables:

```env
PANEL_PUBLIC_BASE_URL=https://panel.vetka.tech
SUBSCRIPTION_PUBLIC_BASE_URL=https://sub2.vetka.tech
SUBSCRIPTION_PROFILE_TITLE=Ветка VPN
SUBSCRIPTION_UPDATE_INTERVAL_HOURS=12
APP_TIMEZONE=Europe/Moscow
EXPIRY_RECONCILE_ENABLED=true
EXPIRY_RECONCILE_INTERVAL=1m
BACKUP_ENABLED=true
BACKUP_DIR=/var/backups/vetka-backend-panel
BACKUP_RETENTION_DAYS=14
BACKUP_ON_CALENDAR=*-*-* 03:30:00 UTC
BACKUP_BEFORE_UPDATE=true
```

Useful deployment commands:

```bash
cd /opt/vetka-backend-panel
docker compose ps
docker compose logs --tail=100 backend
docker compose exec -T postgres pg_isready -U vetka -d vetka_backend
curl -fsSL http://127.0.0.1:8080/health
```

## Backup Architecture

The PostgreSQL dump is the primary backup artifact for restoring the Backend Panel state. It contains users, subscription tokens, nodes, node secrets, assignments, protocol credentials, expiry state, sync versions, sync history, and migration state.

`backup.sh` stores:

- a PostgreSQL custom-format dump created through the Compose-managed `postgres` service
- `.env`, `Caddyfile`, and `docker-compose.override.yml` when those files exist
- a reference copy of `docker-compose.yml`
- `metadata.json`
- `SHA256SUMS`

The backup does not include `.git`, source code history, Docker images, PostgreSQL raw volumes, logs, or Caddy certificate volumes.

## Manual Backup

Create a backup:

```bash
cd /opt/vetka-backend-panel
./backup.sh create
```

Useful options:

```bash
./backup.sh create --output-dir /var/backups/vetka-backend-panel --retention-days 30
./backup.sh create --no-retention
./backup.sh list
```

The archive name looks like:

```text
vetka-backend-panel-20260613T033000Z-<short_git_sha>.tar.gz
```

## Automatic Backups

When `BACKUP_ENABLED=true`, the installer creates:

- `vetka-backend-backup.service`
- `vetka-backend-backup.timer`

The timer uses `BACKUP_ON_CALENDAR`, runs with `Persistent=true`, and stores archives in `BACKUP_DIR`. By default it creates a daily backup at `03:30 UTC` and keeps `14` days of project backup archives.

Check timer status with:

```bash
systemctl status vetka-backend-backup.timer
systemctl list-timers vetka-backend-backup.timer
systemctl start vetka-backend-backup.service
journalctl -u vetka-backend-backup.service
```

If `BACKUP_BEFORE_UPDATE=true`, `update.sh` creates a fresh backup before resetting to `origin/main`. Use `./update.sh --skip-backup` or `SKIP_BACKUP_BEFORE_UPDATE=true` only when you explicitly accept the risk.

## Verify Backup

Verification does not modify the system:

```bash
./backup.sh verify /path/to/vetka-backend-panel-....tar.gz
```

Verification checks:

- archive readability
- archive path safety
- required files
- `metadata.json`
- `SHA256SUMS`
- `database.dump` through `pg_restore --list`

`restore.sh --verify-only` runs the same integrity checks as `backup.sh verify`.

Archive verification does not require a running PostgreSQL service or a live Compose stack. It uses a suitable local `pg_restore` when available; otherwise it can fall back to the PostgreSQL Docker image declared in `docker-compose.yml`.

## Restore On The Same Server

Run:

```bash
cd /opt/vetka-backend-panel
./restore.sh --archive /path/to/vetka-backend-panel-....tar.gz
```

Without `--yes`, the script requires a literal `RESTORE` confirmation. Before a destructive restore it creates a safety backup of the current installation unless `--skip-pre-restore-backup` is explicitly set.

Supported restore modes:

```bash
./restore.sh --archive /path/to/backup.tar.gz --verify-only
./restore.sh --archive /path/to/backup.tar.gz --db-only --yes
./restore.sh --archive /path/to/backup.tar.gz --config-only --yes
```

Full restore restores `.env`, `Caddyfile`, and `docker-compose.override.yml` when they are present in the archive, but it does not automatically replace the current repository `docker-compose.yml` with the archived reference copy.

The backup timer is installed or updated only after the restore completes successfully and the internal readiness check passes.

## Restore On A New Server

Bootstrap Docker, Compose, `jq`, and `tar` first, then clone the repository and run restore. On a fresh Debian or Ubuntu host:

```bash
apt-get update
apt-get install -y ca-certificates curl git jq tar docker.io docker-compose-v2
systemctl enable --now docker
```

Then:

```bash
git clone https://github.com/Daniil-GR/vetka-backend-panel.git /opt/vetka-backend-panel
cd /opt/vetka-backend-panel

./restore.sh --archive /path/to/vetka-backend-panel-....tar.gz
```

The restore script recreates the PostgreSQL database from the dump, runs the project migrations, brings the Compose stack back up, and checks the internal `/ready` endpoint from inside the `backend` container. `/health` remains a liveness endpoint only.

## Backend IP Migration

After moving the Backend Panel to a new server or IP:

- keep the existing `nodeId` and `nodeSecret`
- allow the new Backend IP in every Node Agent `backendAllowedIps`
- update UFW or equivalent firewall rules for port `2222`
- only then run manual node sync

Restore does not automatically sync all nodes after recovery.

## Disaster Recovery Checklist

- create backups regularly and verify them
- copy backup archives to protected external storage
- keep the Git repository or release artifact available for rebuild
- restore the panel from the latest verified archive
- confirm PostgreSQL is healthy
- confirm backend `/health`
- confirm admin login
- update Node Agent allowlists if the Backend IP changed
- run targeted node sync only after agent-side allowlists are ready

## Security Considerations

Backup archives contain `.env`, admin credentials, subscription tokens, and node secrets. Store them in protected external storage with strict access control. The archive should remain `0600`, the backup directory `0700`, and temporary extraction directories `0700`.

The archive is not encrypted by `backup.sh`. Use encrypted external storage or encrypt the archive separately before moving it off-host.

Backups stored only on the same server do not protect against loss of that server. Copy them off-host.

Caddy certificates are not included by default. They should be obtained again after restore.

PostgreSQL is not published publicly. All backup and restore actions use the internal Compose-managed `postgres` container instead of opening `5432`.

Restore success does not require working public DNS. The primary post-restore readiness check is an internal request from inside the `backend` container to `http://127.0.0.1:8080/ready`. A public `PANEL_PUBLIC_BASE_URL/ready` check is useful, but it is only a secondary warning signal because DNS and HTTPS issuance may still be converging after migration.

## UI Language

The admin panel uses Russian by default. English can be selected from the sidebar language switcher, and the choice is stored in the `vetka_ui_language` browser cookie.

The UI locale only affects the server-rendered HTML interface. API responses, subscription formats, backend logs, protocol names, sync payloads, and stored database values do not change with the selected language.

## Future

Future integration with the main Vetka backend should treat `users + subscriptions` as the access source of truth. `subscriptions.expires_at` is the primary expiration timestamp, `users.subscription_expires_at` can stay a cache or summary, and payments or orders should remain history rather than active access state. The reserve panel should receive the exact subscription end timestamp from the main backend, preserve that exact moment, store absolute time as UTC or `timestamptz`, display it in `Europe/Moscow` when needed, and emit `Subscription-Userinfo expire` as a Unix timestamp. Current access should depend on an enabled user plus an active, not-yet-expired current subscription, while replaced or old subscriptions should not grant access.

### Squads

- users can be assigned to squads
- nodes can be assigned to squads
- effective access = enabled user + active squad membership + enabled nodes in squad
- per-user node access remains as a manual override

## Checks

```bash
go fmt ./...
go test ./...
go run ./cmd/server --help
git diff --check
```
