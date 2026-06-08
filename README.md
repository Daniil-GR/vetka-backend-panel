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

The UI listens on `http://localhost:8080` by default. Login uses `ADMIN_USERNAME` and `ADMIN_PASSWORD`. API endpoints use:

```http
Authorization: Bearer <ADMIN_API_TOKEN>
```

The default `docker-compose.yml` keeps PostgreSQL on the internal Docker network only. Do not publish PostgreSQL publicly from the default stack. If local direct access is needed for development, use a separate override file with a loopback-only PostgreSQL port binding rather than a public bind.

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

## Users And Subscriptions

Create users in `/users`. The panel generates `subscription_token` and per-node protocol credentials. A user subscription is available at:

```text
https://sub.vetka.tech/sub/<subscription_token>
```

Disabled or expired users do not receive a subscription response and are excluded from node sync payloads.

Naive and Mieru URI builders live in `internal/subscriptions`. The Naive URI format and Mieru share link are marked with TODOs until final client formats are confirmed.

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
GET  /api/users/{id}/subscription
```

`GET /api/nodes` and `GET /api/nodes/{id}` return masked `node_secret`. `POST /api/nodes` returns the raw generated or submitted secret once so the admin can bootstrap a Node Agent.

Telegram Bot and payment logic are not part of this repository.

## Security Notes

`node_secret` is stored in PostgreSQL for the MVP. The UI masks it after creation and API responses should avoid exposing raw secrets outside trusted admin flows.

PostgreSQL should not be publicly reachable. Node Agent port `2222` should be open only for the Backend Panel IP.

If `NODE_SECRET` is exposed, rotate it and update the backend record before the next sync.

TODO: add encryption at rest for node secrets using `APP_SECRET`.

Never commit real `.env` files or production secrets.

## Checks

```bash
go fmt ./...
go test ./...
go run ./cmd/server --help
git diff --check
```
